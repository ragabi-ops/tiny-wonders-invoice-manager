package payments

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/documents"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/idempotency"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/pdf"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose the payment endpoints.
type Handlers struct {
	svc         *Service
	idempotency *idempotency.Store
	pool        *db.Pool
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the payment HTTP layer.
func NewHandlers(svc *Service, store *idempotency.Store, pool *db.Pool, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, idempotency: store, pool: pool, currentUser: currentUser}
}

// CustomerRoutes mounts the per-customer views of payment data: the open
// balance and the document/payment timeline plan.md 8 asks a customer page to
// show. They live here because this package owns the numbers.
func (h *Handlers) CustomerRoutes(r chi.Router) {
	r.Get("/{id}/balance", h.customerBalance)
	r.Get("/{id}/timeline", h.customerTimeline)
	r.Get("/{id}/outstanding", h.customerOutstanding)
}

func (h *Handlers) customerBalance(w http.ResponseWriter, r *http.Request) {
	balance, err := h.svc.CustomerBalance(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, balance)
}

func (h *Handlers) customerTimeline(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r.URL.Query().Get("limit"), 100)
	if err != nil {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"limit": "ערך לא תקין"}))
		return
	}

	entries, err := h.svc.CustomerTimeline(r.Context(), chi.URLParam(r, "id"), limit)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"entries": entries})
}

// customerOutstanding lists what the customer still owes, so the UI can offer
// those documents when allocating a payment.
func (h *Handlers) customerOutstanding(w http.ResponseWriter, r *http.Request) {
	outstanding, err := h.svc.Outstanding(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"documents": outstanding})
}

// ReadRoutes mounts endpoints any authenticated role may read.
func (h *Handlers) ReadRoutes(r chi.Router) {
	r.Get("/", h.list)
	r.Get("/methods", h.methods)
	r.Get("/by-method", h.byMethod)
	r.Get("/{id}", h.get)
}

// WriteRoutes mounts endpoints that change data, plus the receipt preview.
//
// The preview writes nothing, but it is a POST because it carries the payment
// the operator has filled in, and it sits with the write routes because only
// someone who may record a payment has any use for it.
func (h *Handlers) WriteRoutes(r chi.Router) {
	r.Post("/", h.create)
	r.Post("/preview", h.previewReceipt)
	r.Post("/{id}/receipt", h.issueReceipt)
}

// previewReceipt renders the receipt a payment would produce, before recording
// it. Receipts are never drafts — a payment and its receipt commit together —
// so this is the only chance to look at one before it becomes immutable.
func (h *Handlers) previewReceipt(w http.ResponseWriter, r *http.Request) {
	var body Input
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	body.Normalize()
	if problems := body.Validate(); len(problems) > 0 {
		httpx.WriteError(w, r, httpx.ValidationError(problems))
		return
	}

	data, filename, err := h.svc.PreviewReceipt(r.Context(), body)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if _, err := w.Write(data); err != nil {
		httpx.Logger(r).Error("write receipt preview", "error", err)
	}
}

type methodOption struct {
	Method string `json:"method"`
	Hebrew string `json:"hebrew"`
}

// methods lists the closed set of payment methods, so the UI never hard-codes
// them.
func (h *Handlers) methods(w http.ResponseWriter, r *http.Request) {
	options := make([]methodOption, 0, len(AllMethods))
	for _, method := range AllMethods {
		options = append(options, methodOption{Method: string(method), Hebrew: method.HebrewName()})
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"methods": options})
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	params := ListParams{
		CustomerID:      query.Get("customer_id"),
		ActivityID:      query.Get("activity_id"),
		Method:          Method(query.Get("method")),
		FromDate:        query.Get("from"),
		ToDate:          query.Get("to"),
		OnlyUnreceipted: query.Get("unreceipted") == "true",
	}
	if params.Method != "" && !params.Method.Valid() {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"method": "אמצעי תשלום לא מוכר"}))
		return
	}

	var err error
	if params.Limit, err = intParam(query.Get("limit"), 50); err != nil {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"limit": "ערך לא תקין"}))
		return
	}
	if params.Offset, err = intParam(query.Get("offset"), 0); err != nil {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"offset": "ערך לא תקין"}))
		return
	}

	page, err := h.svc.List(r.Context(), params)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, page)
}

func (h *Handlers) byMethod(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	totals, err := h.svc.TotalsByMethod(r.Context(), query.Get("from"), query.Get("to"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"totals": totals})
}

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) {
	payment, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, payment)
}

// create records a payment and, in the primary flow, issues its receipt. It
// accepts an idempotency key so a retry after a timeout does not record the
// money twice (plan.md 12, 17).
func (h *Handlers) create(w http.ResponseWriter, r *http.Request) {
	var body Input
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	body.Normalize()
	if problems := body.Validate(); len(problems) > 0 {
		httpx.WriteError(w, r, httpx.ValidationError(problems))
		return
	}

	actor := h.currentUser(r)
	ctx := r.Context()

	const scope = "payments.create"
	key, hasKey := idempotency.Normalize(r.Header.Get(documents.IdempotencyHeader))

	// The fingerprint is the request itself: the same key with different money
	// is a client bug, not a retry.
	canonical, err := json.Marshal(body)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	fingerprint := idempotency.Fingerprint(canonical)

	if hasKey {
		record, err := h.idempotency.Lookup(ctx, h.pool, scope, key, fingerprint)
		switch {
		case errors.Is(err, idempotency.ErrKeyReused):
			httpx.WriteError(w, r, httpx.ErrConflict.
				WithMessage("מפתח הבקשה כבר שימש לבקשה אחרת.").WithCause(err))
			return
		case err != nil:
			httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
			return
		case record != nil:
			w.Header().Set("Idempotent-Replay", "true")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(record.Status)
			if _, err := w.Write(record.Body); err != nil {
				httpx.Logger(r).Error("write idempotent replay", "error", err)
			}
			return
		}
	}

	payment, err := h.svc.Create(ctx, actor, body, httpx.RequestID(ctx))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}

	encoded, err := json.Marshal(payment)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}

	if hasKey {
		if err := h.idempotency.Save(ctx, h.pool, scope, key, fingerprint, actor.ID,
			idempotency.Record{Status: http.StatusCreated, Body: encoded, EntityID: payment.ID}); err != nil &&
			!errors.Is(err, idempotency.ErrConcurrentReplay) {
			httpx.Logger(r).Error("save idempotency key", "error", err)
		}
	}

	httpx.WriteJSON(w, r, http.StatusCreated, payment)
}

// issueReceipt issues the receipt for a payment recorded without one.
func (h *Handlers) issueReceipt(w http.ResponseWriter, r *http.Request) {
	payment, err := h.svc.IssueReceipt(r.Context(), h.currentUser(r),
		chi.URLParam(r, "id"), httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, payment)
}

func intParam(raw string, def int) (int, error) {
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, errors.New("invalid integer parameter")
	}
	return value, nil
}

// translate maps domain errors onto the API error catalogue.
func translate(err error) error {
	var blocked *documents.ErrComplianceBlocked
	if errors.As(err, &blocked) {
		return httpx.ErrComplianceBlocked.WithMessage(blocked.Reason).WithCause(err)
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.ErrNotFound.WithMessage("התשלום לא נמצא.")
	case errors.Is(err, ErrReceiptExists):
		return httpx.ErrConflict.WithMessage("כבר הופקה קבלה עבור תשלום זה.")
	case errors.Is(err, ErrOverAllocated):
		return httpx.ValidationError(map[string]string{
			"allocations": "סכום ההקצאות גדול מסכום התשלום",
		})
	case errors.Is(err, ErrDocumentOverpaid):
		return httpx.ValidationError(map[string]string{
			"allocations": "סכום ההקצאה גדול מהיתרה הפתוחה של המסמך",
		})
	case errors.Is(err, ErrCustomerHidden):
		return httpx.ValidationError(map[string]string{"customer_id": "הלקוח נמצא בארכיון"})
	case errors.Is(err, documents.ErrNotFound):
		return httpx.ValidationError(map[string]string{"allocations": "אחד המסמכים לא נמצא"})
	case errors.Is(err, documents.ErrNotIssued):
		return httpx.ValidationError(map[string]string{
			"allocations": "ניתן לשייך תשלום רק למסמך שהופק",
		})
	case errors.Is(err, pdf.ErrRendererUnavailable):
		// The money is not lost: the payment can be recorded without a receipt
		// and the receipt issued once the renderer is back.
		return httpx.ErrInternal.
			WithMessage("שירות הפקת ה־PDF אינו זמין, ולכן לא הופקה קבלה. אפשר לרשום את התשלום ללא קבלה ולהפיק אותה מאוחר יותר.").
			WithCause(err)
	default:
		return httpx.ErrInternal.WithCause(err)
	}
}
