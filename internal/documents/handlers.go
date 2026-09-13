package documents

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/idempotency"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/money"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/pdf"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/storage"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// IdempotencyHeader is the header a client uses to make a retry safe.
const IdempotencyHeader = "Idempotency-Key"

// Handlers expose the document endpoints.
type Handlers struct {
	svc         *Service
	idempotency *idempotency.Store
	pool        *db.Pool
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the document HTTP layer.
func NewHandlers(svc *Service, store *idempotency.Store, pool *db.Pool, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, idempotency: store, pool: pool, currentUser: currentUser}
}

// ReadRoutes mounts endpoints any authenticated role may read.
func (h *Handlers) ReadRoutes(r chi.Router) {
	r.Get("/", h.list)
	r.Get("/types", h.types)
	r.Get("/sequences", h.sequences)
	r.Get("/{id}", h.get)
	r.Get("/{id}/snapshot", h.snapshot)
	r.Get("/{id}/pdf", h.downloadPDF)
	r.Get("/{id}/preview", h.preview)
}

// WriteRoutes mounts endpoints that change data.
func (h *Handlers) WriteRoutes(r chi.Router) {
	r.Post("/", h.createDraft)
	r.Put("/{id}", h.updateDraft)
	r.Delete("/{id}", h.deleteDraft)
	r.Post("/{id}/issue", h.issue)
	r.Post("/{id}/cancel", h.cancel)
}

type typeOption struct {
	Type   string `json:"type"`
	Hebrew string `json:"hebrew"`
}

// types lists the document types, so the UI never hard-codes them and never
// offers one this deployment may not issue.
func (h *Handlers) types(w http.ResponseWriter, r *http.Request) {
	options := make([]typeOption, 0, len(AllTypes))
	for _, documentType := range AllTypes {
		options = append(options, typeOption{
			Type:   string(documentType),
			Hebrew: documentType.HebrewName(),
		})
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"types": options})
}

func (h *Handlers) sequences(w http.ResponseWriter, r *http.Request) {
	states, err := h.svc.SequenceStates(r.Context())
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"sequences": states})
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	params := ListParams{
		Query:        query.Get("q"),
		CustomerID:   query.Get("customer_id"),
		ActivityID:   query.Get("activity_id"),
		DocumentType: Type(query.Get("document_type")),
		State:        State(query.Get("state")),
		FromDate:     query.Get("from"),
		ToDate:       query.Get("to"),
	}

	if params.DocumentType != "" && !params.DocumentType.Valid() {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"document_type": "סוג מסמך לא מוכר"}))
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

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) {
	document, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, document)
}

// snapshot returns exactly what the document said when it was issued.
func (h *Handlers) snapshot(w http.ResponseWriter, r *http.Request) {
	raw, err := h.svc.Snapshot(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	if len(raw) == 0 || string(raw) == "null" {
		httpx.WriteError(w, r, httpx.ErrNotFound.WithMessage("למסמך אין תמונת מצב: הוא עדיין טיוטה."))
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(raw); err != nil {
		httpx.Logger(r).Error("write snapshot", "error", err)
	}
}

// downloadPDF streams the stored artifact, verified against its hash.
func (h *Handlers) downloadPDF(w http.ResponseWriter, r *http.Request) {
	data, filename, err := h.svc.PDF(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if _, err := w.Write(data); err != nil {
		httpx.Logger(r).Error("write pdf", "error", err)
	}
}

// preview renders a draft as it will look once issued, without issuing it.
//
// Nothing is allocated or stored: no number, no snapshot, no saved PDF. It is
// the last cheap moment to catch a mistake, because issuance is irreversible.
func (h *Handlers) preview(w http.ResponseWriter, r *http.Request) {
	data, filename, err := h.svc.Preview(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	writePreview(w, r, data, filename)
}

// writePreview streams a preview inline, so the browser shows it rather than
// downloading a file the operator has to find and then delete.
func writePreview(w http.ResponseWriter, r *http.Request, data []byte, filename string) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if _, err := w.Write(data); err != nil {
		httpx.Logger(r).Error("write preview", "error", err)
	}
}

func (h *Handlers) createDraft(w http.ResponseWriter, r *http.Request) {
	var body DraftInput
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	body.Normalize()
	if problems := body.Validate(); len(problems) > 0 {
		httpx.WriteError(w, r, httpx.ValidationError(problems))
		return
	}

	document, err := h.svc.CreateDraft(r.Context(), h.currentUser(r), body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, document)
}

func (h *Handlers) updateDraft(w http.ResponseWriter, r *http.Request) {
	var body DraftInput
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	body.Normalize()
	if problems := body.Validate(); len(problems) > 0 {
		httpx.WriteError(w, r, httpx.ValidationError(problems))
		return
	}

	document, err := h.svc.UpdateDraft(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, document)
}

func (h *Handlers) deleteDraft(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteDraft(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusNoContent, nil)
}

// issue is the critical command. It accepts an idempotency key so a client that
// retries after a timeout gets the original document back rather than a second
// one with a second official number (plan.md 10).
func (h *Handlers) issue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	actor := h.currentUser(r)
	ctx := r.Context()

	key, hasKey := idempotency.Normalize(r.Header.Get(IdempotencyHeader))
	const scope = "documents.issue"
	// The document being issued is the whole request: two issues of the same
	// draft under one key are the same request.
	fingerprint := idempotency.Fingerprint([]byte(scope + ":" + id))

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
			// Replay: return exactly what the original request returned.
			w.Header().Set("Idempotent-Replay", "true")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(record.Status)
			if _, err := w.Write(record.Body); err != nil {
				httpx.Logger(r).Error("write idempotent replay", "error", err)
			}
			return
		}
	}

	document, err := h.svc.Issue(ctx, actor, id, httpx.RequestID(ctx))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}

	if hasKey {
		// Recording the key after the issuance commits leaves a narrow window in
		// which a retry could issue twice — but the draft is no longer a draft
		// by then, so the second attempt is refused with ALREADY_ISSUED rather
		// than allocating a second number.
		if err := h.idempotency.Save(ctx, h.pool, scope, key, fingerprint, actor.ID,
			idempotency.Record{Status: http.StatusOK, Body: encoded, EntityID: id}); err != nil &&
			!errors.Is(err, idempotency.ErrConcurrentReplay) {
			httpx.Logger(r).Error("save idempotency key", "error", err)
		}
	}

	httpx.WriteJSON(w, r, http.StatusOK, document)
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

func (h *Handlers) cancel(w http.ResponseWriter, r *http.Request) {
	var body cancelRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	// A cancellation without a reason is not auditable, so it is not accepted
	// (plan.md 9).
	if len([]rune(body.Reason)) < 3 {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{
			"reason": "יש להזין סיבת ביטול",
		}))
		return
	}

	document, err := h.svc.Cancel(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body.Reason, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, document)
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
	var blocked *ErrComplianceBlocked
	if errors.As(err, &blocked) {
		return httpx.ErrComplianceBlocked.WithMessage(blocked.Reason).WithCause(err)
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.ErrNotFound.WithMessage("המסמך לא נמצא.")
	case errors.Is(err, ErrNotDraft):
		return httpx.ErrImmutableDocument
	case errors.Is(err, ErrAlreadyIssued):
		return httpx.ErrConflict.WithMessage("המסמך כבר הופק.")
	case errors.Is(err, ErrAlreadyVoid):
		return httpx.ErrConflict.WithMessage("המסמך כבר בוטל.")
	case errors.Is(err, ErrNotIssued):
		return httpx.ErrConflict.WithMessage("ניתן לבטל רק מסמך שהופק.")
	case errors.Is(err, ErrNoLines):
		return httpx.ValidationError(map[string]string{"lines": "יש להוסיף לפחות שורה אחת"})
	case errors.Is(err, ErrCustomerHidden):
		return httpx.ValidationError(map[string]string{"customer_id": "הלקוח נמצא בארכיון"})
	case errors.Is(err, pdf.ErrRendererUnavailable):
		// Refusing is the point: a document must never be issued without its PDF.
		return httpx.ErrInternal.
			WithMessage("שירות הפקת ה־PDF אינו זמין. המסמך לא הופק ולא נצרך מספר.").
			WithCause(err)
	case errors.Is(err, storage.ErrFileMissing):
		return httpx.ErrInternal.WithMessage("קובץ ה־PDF של המסמך חסר.").WithCause(err)
	case errors.Is(err, storage.ErrFileCorrupt):
		return httpx.ErrInternal.
			WithMessage("קובץ ה־PDF של המסמך אינו תואם את החתימה השמורה.").WithCause(err)
	case errors.Is(err, money.ErrOverflow):
		return httpx.ValidationError(map[string]string{"lines": "הסכום גדול מדי"})
	default:
		return httpx.ErrInternal.WithCause(err)
	}
}
