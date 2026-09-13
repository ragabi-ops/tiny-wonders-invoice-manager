package compliance

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/settings"
)

// Handlers expose the read-only compliance status. There is no write endpoint:
// the gate is changed through regulatory configuration, which is audited.
type Handlers struct {
	svc *Service
}

// NewHandlers builds the compliance HTTP layer.
func NewHandlers(svc *Service) *Handlers { return &Handlers{svc: svc} }

// Routes mounts the compliance status.
func (h *Handlers) Routes(r chi.Router) {
	r.Get("/", h.status)
}

// status reports the current mode and gate state, so the UI can always show
// whether real issuance is enabled.
func (h *Handlers) status(w http.ResponseWriter, r *http.Request) {
	rules, err := h.svc.CurrentRules(r.Context())
	if err != nil {
		if errors.Is(err, settings.ErrNotConfigured) {
			// Missing regulatory data is a deployment fault, not a user error,
			// and must never be papered over with a default value.
			httpx.WriteError(w, r, httpx.ErrInternal.
				WithMessage("הגדרות רגולטוריות חסרות. יש להשלים אותן לפני המשך העבודה.").
				WithCause(err))
			return
		}
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, rules.Status())
}
