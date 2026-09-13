package catalog

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose the catalogue endpoints.
type Handlers struct {
	svc         *Service
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the catalogue HTTP layer.
func NewHandlers(svc *Service, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, currentUser: currentUser}
}

// ReadRoutes mounts endpoints any authenticated role may read.
func (h *Handlers) ReadRoutes(r chi.Router) {
	r.Get("/", h.list)
	r.Get("/categories", h.categories)
	r.Get("/{id}", h.get)
}

// WriteRoutes mounts endpoints that change data.
func (h *Handlers) WriteRoutes(r chi.Router) {
	r.Post("/", h.create)
	r.Put("/{id}", h.update)
	r.Put("/{id}/active", h.setActive)
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	params := ListParams{
		Query:         query.Get("q"),
		Category:      query.Get("category"),
		IncludeHidden: query.Get("include_archived") == "true",
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

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) {
	item, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, item)
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

	item, err := h.svc.Create(r.Context(), h.currentUser(r), body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, item)
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

	item, err := h.svc.Update(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, item)
}

type setActiveRequest struct {
	Active bool `json:"active"`
}

func (h *Handlers) setActive(w http.ResponseWriter, r *http.Request) {
	var body setActiveRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	item, err := h.svc.SetActive(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body.Active, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, item)
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
	if errors.Is(err, ErrNotFound) {
		return httpx.ErrNotFound.WithMessage("השירות לא נמצא.")
	}
	return httpx.ErrInternal.WithCause(err)
}
