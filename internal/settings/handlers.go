package settings

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/storage"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose the settings endpoints.
type Handlers struct {
	svc         *Service
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the settings HTTP layer.
func NewHandlers(svc *Service, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, currentUser: currentUser}
}

// ReadRoutes mounts endpoints any authenticated user may read.
func (h *Handlers) ReadRoutes(r chi.Router) {
	r.Get("/business-profile", h.getBusinessProfile)
	r.Get("/business-profile/logo", h.getLogo)
}

// OwnerRoutes mounts endpoints restricted to OWNER.
func (h *Handlers) OwnerRoutes(r chi.Router) {
	r.Put("/business-profile", h.updateBusinessProfile)
	r.Post("/business-profile/logo", h.uploadLogo)
	r.Delete("/business-profile/logo", h.removeLogo)
	r.Get("/regulatory", h.listRegulatory)
	r.Post("/regulatory", h.setRegulatory)
}

func (h *Handlers) getBusinessProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := h.svc.BusinessProfile(r.Context())
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, profile)
}

func (h *Handlers) updateBusinessProfile(w http.ResponseWriter, r *http.Request) {
	var body BusinessProfile
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	updated, err := h.svc.UpdateBusinessProfile(r.Context(), h.currentUser(r), body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, updated)
}

// getLogo serves the current logo. It is readable by any authenticated role
// because the UI shows it, and it is not sensitive.
func (h *Handlers) getLogo(w http.ResponseWriter, r *http.Request) {
	data, contentType, err := h.svc.Logo(r.Context())
	if err != nil {
		httpx.WriteError(w, r, translateLogo(err))
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	// Private: it is behind a session, so a shared cache must not keep it.
	w.Header().Set("Cache-Control", "private, max-age=300")
	if _, err := w.Write(data); err != nil {
		httpx.Logger(r).Error("write logo", "error", err)
	}
}

// uploadLogo replaces the logo used on future documents.
func (h *Handlers) uploadLogo(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxLogoBytes+(1<<20))

	if err := r.ParseMultipartForm(4 << 20); err != nil {
		httpx.WriteError(w, r, httpx.ErrPayloadTooBig.
			WithMessage("הקובץ גדול מדי או שהבקשה אינה תקינה.").WithCause(err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrBadRequest.WithMessage("לא נמצא קובץ בבקשה.").WithCause(err))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, MaxLogoBytes+1))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}

	profile, err := h.svc.SetLogo(r.Context(), h.currentUser(r), header.Filename, data,
		httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translateLogo(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, profile)
}

// removeLogo stops printing a logo on future documents. Documents already
// issued keep theirs.
func (h *Handlers) removeLogo(w http.ResponseWriter, r *http.Request) {
	profile, err := h.svc.RemoveLogo(r.Context(), h.currentUser(r), httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translateLogo(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, profile)
}

func translateLogo(err error) error {
	switch {
	case errors.Is(err, ErrNoLogo):
		return httpx.ErrNotFound.WithMessage("לא הועלה לוגו.")
	case errors.Is(err, ErrUnsupportedLogoType):
		return httpx.ValidationError(map[string]string{
			"file": "ניתן להעלות קובץ PNG, JPG או WEBP בגודל עד 4MB",
		})
	case errors.Is(err, storage.ErrFileMissing):
		return httpx.ErrInternal.WithMessage("קובץ הלוגו חסר באחסון.").WithCause(err)
	case errors.Is(err, storage.ErrFileCorrupt):
		return httpx.ErrInternal.
			WithMessage("קובץ הלוגו אינו תואם את החתימה השמורה.").WithCause(err)
	default:
		return httpx.ErrInternal.WithCause(err)
	}
}

func (h *Handlers) listRegulatory(w http.ResponseWriter, r *http.Request) {
	entries, err := h.svc.ListRegulatory(r.Context(), r.URL.Query().Get("key"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"entries": entries})
}

type setRegulatoryRequest struct {
	Key           string          `json:"key"`
	EffectiveFrom string          `json:"effective_from"`
	Value         json.RawMessage `json:"value"`
	Note          string          `json:"note"`
}

// setRegulatory adds a new effective-dated value. It never edits an existing
// row: regulatory history is append-only.
func (h *Handlers) setRegulatory(w http.ResponseWriter, r *http.Request) {
	var body setRegulatoryRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	fields := map[string]string{}
	if body.Key == "" {
		fields["key"] = "יש לבחור מפתח הגדרה"
	}
	effectiveFrom, err := time.Parse("2006-01-02", body.EffectiveFrom)
	if err != nil {
		fields["effective_from"] = "יש להזין תאריך תחילת תוקף בפורמט YYYY-MM-DD"
	}
	if len(body.Value) == 0 || !json.Valid(body.Value) {
		fields["value"] = "הערך אינו תקין"
	}
	if len(fields) > 0 {
		httpx.WriteError(w, r, httpx.ValidationError(fields))
		return
	}

	if err := h.svc.SetRegulatoryValue(r.Context(), h.currentUser(r), body.Key,
		effectiveFrom, body.Value, body.Note, httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, httpx.ErrConflict.
			WithMessage("לא ניתן לשמור את ההגדרה. ייתכן שכבר קיימת הגדרה לאותו תאריך תחילת תוקף.").
			WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, map[string]any{"status": "ok"})
}
