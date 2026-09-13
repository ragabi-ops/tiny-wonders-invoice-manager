package backup

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose backup status and the manual run.
type Handlers struct {
	svc         *Service
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the backup HTTP layer.
func NewHandlers(svc *Service, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, currentUser: currentUser}
}

// Routes mounts the backup endpoints. They are OWNER-only: backups are
// operational plumbing, and the archive location is not something an operator
// or an accountant needs (plan.md 6).
func (h *Handlers) Routes(r chi.Router) {
	r.Get("/status", h.status)
	r.Get("/runs", h.runs)
	r.Post("/run", h.run)
}

func (h *Handlers) status(w http.ResponseWriter, r *http.Request) {
	status, err := h.svc.Status(r.Context())
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, status)
}

func (h *Handlers) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := h.svc.Recent(r.Context(), 20)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"runs": runs})
}

// run performs a backup now, synchronously.
//
// It is not queued: an operator who clicks "back up now" is usually about to do
// something risky and wants to know it worked before they do it.
func (h *Handlers) run(w http.ResponseWriter, r *http.Request) {
	result, err := h.svc.Create(r.Context(), TriggerManual, h.currentUser(r))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, result)
}

func translate(err error) error {
	switch {
	case errors.Is(err, ErrNotConfigured):
		return httpx.ErrInternal.WithMessage(
			"לא הוגדרה תיקיית גיבוי. יש להגדיר BACKUP_DIR ולהפעיל מחדש.").WithCause(err)
	case errors.Is(err, ErrPgDumpMissing):
		return httpx.ErrInternal.WithMessage(
			"לא ניתן להריץ את כלי גיבוי מסד הנתונים (pg_dump). הגיבוי לא בוצע.").WithCause(err)
	default:
		return httpx.ErrInternal.WithMessage("הגיבוי נכשל.").WithCause(err)
	}
}
