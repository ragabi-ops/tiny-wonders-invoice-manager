package expenses

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/payments"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/storage"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose the expense endpoints.
type Handlers struct {
	svc         *Service
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the expense HTTP layer.
func NewHandlers(svc *Service, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, currentUser: currentUser}
}

// ReadRoutes mounts endpoints any authenticated role may read. The accountant
// needs the attachments as much as the owner does (plan.md 6).
func (h *Handlers) ReadRoutes(r chi.Router) {
	r.Get("/", h.list)
	r.Get("/categories", h.categories)
	r.Get("/by-category", h.byCategory)
	r.Get("/{id}", h.get)
	r.Get("/attachments/{attachmentID}", h.downloadAttachment)
}

// WriteRoutes mounts endpoints that change data.
func (h *Handlers) WriteRoutes(r chi.Router) {
	r.Post("/", h.create)
	r.Put("/{id}", h.update)
	r.Delete("/{id}", h.remove)
	r.Post("/{id}/attachments", h.uploadAttachment)
	r.Delete("/{id}/attachments/{attachmentID}", h.removeAttachment)
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	params := ListParams{
		Query:      query.Get("q"),
		Category:   query.Get("category"),
		ActivityID: query.Get("activity_id"),
		Method:     payments.Method(query.Get("payment_method")),
		FromDate:   query.Get("from"),
		ToDate:     query.Get("to"),
	}
	if params.Method != "" && !params.Method.Valid() {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"payment_method": "אמצעי תשלום לא מוכר"}))
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

func (h *Handlers) categories(w http.ResponseWriter, r *http.Request) {
	categories, err := h.svc.Categories(r.Context())
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"categories": categories})
}

func (h *Handlers) byCategory(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	totals, err := h.svc.TotalsByCategory(r.Context(),
		query.Get("from"), query.Get("to"), query.Get("activity_id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"totals": totals})
}

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) {
	expense, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, expense)
}

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

	expense, err := h.svc.Create(r.Context(), h.currentUser(r), body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, expense)
}

func (h *Handlers) update(w http.ResponseWriter, r *http.Request) {
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

	expense, err := h.svc.Update(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, expense)
}

func (h *Handlers) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusNoContent, nil)
}

// uploadAttachment accepts one file as multipart form data.
func (h *Handlers) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	// The cap is applied before parsing, so an oversized body is rejected
	// rather than buffered.
	r.Body = http.MaxBytesReader(w, r.Body, MaxAttachmentBytes+(1<<20))

	if err := r.ParseMultipartForm(8 << 20); err != nil {
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

	data, err := io.ReadAll(io.LimitReader(file, MaxAttachmentBytes+1))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}

	attachment, err := h.svc.AddAttachment(r.Context(), h.currentUser(r),
		chi.URLParam(r, "id"), header.Filename, data, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, attachment)
}

// downloadAttachment streams a stored file, verified against its hash.
func (h *Handlers) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	data, ref, err := h.svc.Attachment(r.Context(), chi.URLParam(r, "attachmentID"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}

	// Always an attachment, never inline: a stored file is not rendered in the
	// browser, so an uploaded HTML or SVG cannot execute in this origin
	// (SECURITY.md 6).
	w.Header().Set("Content-Type", ref.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+ref.OriginalFilename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if _, err := w.Write(data); err != nil {
		httpx.Logger(r).Error("write attachment", "error", err)
	}
}

func (h *Handlers) removeAttachment(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveAttachment(r.Context(), h.currentUser(r),
		chi.URLParam(r, "id"), chi.URLParam(r, "attachmentID"),
		httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusNoContent, nil)
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

func translate(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.ErrNotFound.WithMessage("ההוצאה או הקובץ לא נמצאו.")
	case errors.Is(err, ErrUnsupportedFileType):
		return httpx.ValidationError(map[string]string{
			"file": "ניתן לצרף תמונה (JPG, PNG, HEIC, WEBP) או קובץ PDF בלבד",
		})
	case errors.Is(err, storage.ErrFileMissing):
		return httpx.ErrInternal.WithMessage("הקובץ המצורף חסר באחסון.").WithCause(err)
	case errors.Is(err, storage.ErrFileCorrupt):
		return httpx.ErrInternal.
			WithMessage("הקובץ המצורף אינו תואם את החתימה השמורה.").WithCause(err)
	default:
		return httpx.ErrInternal.WithCause(err)
	}
}
