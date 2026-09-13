package delivery

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/documents"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose the delivery endpoints. They hang off a document, because
// delivery is something done to a document that already exists.
type Handlers struct {
	svc         *Service
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the delivery HTTP layer.
func NewHandlers(svc *Service, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, currentUser: currentUser}
}

// ReadRoutes mounts the delivery history of a document.
func (h *Handlers) ReadRoutes(r chi.Router) {
	r.Get("/{id}/deliveries", h.attempts)
}

// WriteRoutes mounts the send commands.
func (h *Handlers) WriteRoutes(r chi.Router) {
	r.Post("/{id}/send", h.send)
	r.Post("/{id}/whatsapp", h.whatsapp)
}

// StatusRoutes mounts the deployment-wide delivery status.
func (h *Handlers) StatusRoutes(r chi.Router) {
	r.Get("/status", h.status)
	r.Get("/failures", h.failures)
}

func (h *Handlers) status(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"email_available": h.svc.EmailAvailable(),
		// WhatsApp in v1 is always available: it hands the operator a link to
		// send from their own phone and needs no server-side integration.
		"whatsapp_available": true,
	})
}

func (h *Handlers) failures(w http.ResponseWriter, r *http.Request) {
	attempts, err := h.svc.RecentFailures(r.Context(), 20)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"attempts": attempts})
}

func (h *Handlers) attempts(w http.ResponseWriter, r *http.Request) {
	attempts, err := h.svc.Attempts(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"attempts": attempts})
}

type sendRequest struct {
	// Recipient overrides the customer's stored address for this send.
	Recipient string `json:"recipient"`
}

// send queues an email. It returns as soon as the job is queued: the document
// is already issued, and nothing about delivery can undo that (plan.md 14).
func (h *Handlers) send(w http.ResponseWriter, r *http.Request) {
	var body sendRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	if !h.svc.EmailAvailable() {
		httpx.WriteError(w, r, httpx.ErrInternal.
			WithMessage("שליחת דוא״ל אינה מוגדרת במערכת. אפשר לשתף בוואטסאפ או להוריד את הקובץ."))
		return
	}

	attempt, err := h.svc.QueueEmail(r.Context(), h.currentUser(r),
		chi.URLParam(r, "id"), body.Recipient, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusAccepted, attempt)
}

// whatsapp prepares a share link for the operator to send themselves.
func (h *Handlers) whatsapp(w http.ResponseWriter, r *http.Request) {
	share, err := h.svc.PrepareWhatsApp(r.Context(), h.currentUser(r),
		chi.URLParam(r, "id"), httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, share)
}

func translate(err error) error {
	switch {
	case errors.Is(err, documents.ErrNotFound):
		return httpx.ErrNotFound.WithMessage("המסמך לא נמצא.")
	case errors.Is(err, ErrNotIssued):
		return httpx.ErrConflict.WithMessage("ניתן לשלוח רק מסמך שהופק.")
	case errors.Is(err, ErrNoEmailAddress):
		return httpx.ValidationError(map[string]string{
			"recipient": "ללקוח אין כתובת דוא״ל. יש להזין כתובת או להשלים את פרטי הלקוח.",
		})
	case errors.Is(err, ErrNoPhoneNumber):
		return httpx.ValidationError(map[string]string{
			"phone": "ללקוח אין מספר טלפון לשיתוף בוואטסאפ.",
		})
	case errors.Is(err, ErrMailerUnavailable):
		return httpx.ErrInternal.
			WithMessage("שירות הדוא״ל אינו זמין. המסמך נשמר; אפשר לנסות לשלוח שוב מאוחר יותר.").
			WithCause(err)
	default:
		return httpx.ErrInternal.WithCause(err)
	}
}
