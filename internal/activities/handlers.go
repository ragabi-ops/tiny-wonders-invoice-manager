package activities

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose the activity endpoints.
type Handlers struct {
	svc         *Service
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the activity HTTP layer.
func NewHandlers(svc *Service, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, currentUser: currentUser}
}

// ReadRoutes mounts endpoints any authenticated role may read.
func (h *Handlers) ReadRoutes(r chi.Router) {
	r.Get("/", h.list)
	r.Get("/statuses", h.statuses)
	r.Get("/{id}", h.get)
}

// WriteRoutes mounts endpoints that change data.
func (h *Handlers) WriteRoutes(r chi.Router) {
	r.Post("/", h.create)
	r.Put("/{id}", h.update)
}

type statusOption struct {
	Status string `json:"status"`
	Hebrew string `json:"hebrew"`
}

// statuses lists the closed set of statuses, so the UI never hard-codes them.
func (h *Handlers) statuses(w http.ResponseWriter, r *http.Request) {
	options := make([]statusOption, 0, len(AllStatuses))
	for _, status := range AllStatuses {
		options = append(options, statusOption{Status: string(status), Hebrew: status.HebrewName()})
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"statuses": options})
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	params := ListParams{
		Query:  query.Get("q"),
		Status: Status(query.Get("status")),
	}
	if params.Status != "" && !params.Status.Valid() {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"status": "סטטוס לא מוכר"}))
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
	activity, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, activity)
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

	activity, err := h.svc.Create(r.Context(), h.currentUser(r), body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, activity)
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

	activity, err := h.svc.Update(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, activity)
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
		return httpx.ErrNotFound.WithMessage("הפעילות לא נמצאה.")
	}
	return httpx.ErrInternal.WithCause(err)
}
