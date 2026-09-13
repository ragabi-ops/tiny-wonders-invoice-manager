package customers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose the customer endpoints.
type Handlers struct {
	svc         *Service
	currentUser func(*http.Request) *users.User
}

// NewHandlers builds the customer HTTP layer.
func NewHandlers(svc *Service, currentUser func(*http.Request) *users.User) *Handlers {
	return &Handlers{svc: svc, currentUser: currentUser}
}

// ReadRoutes mounts endpoints any authenticated role may read.
func (h *Handlers) ReadRoutes(r chi.Router) {
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
}

// WriteRoutes mounts endpoints that change data.
func (h *Handlers) WriteRoutes(r chi.Router) {
	r.Post("/", h.create)
	r.Post("/check-duplicates", h.checkDuplicates)
	r.Put("/{id}", h.update)
	r.Put("/{id}/active", h.setActive)
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	params := ListParams{
		Query:         query.Get("q"),
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

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) {
	customer, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, customer)
}

// createRequest is Input plus the operator's answer to a duplicate warning.
type createRequest struct {
	Input
	ConfirmDuplicate bool `json:"confirm_duplicate"`
}

func (h *Handlers) create(w http.ResponseWriter, r *http.Request) {
	var body createRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	body.Input.Normalize()
	if problems := body.Input.Validate(); len(problems) > 0 {
		httpx.WriteError(w, r, httpx.ValidationError(problems))
		return
	}

	customer, err := h.svc.Create(r.Context(), h.currentUser(r), body.Input,
		body.ConfirmDuplicate, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, customer)
}

type checkDuplicatesRequest struct {
	Input
	ExcludeID string `json:"exclude_id"`
}

// checkDuplicates lets the UI warn while the operator is still typing, before
// anything is submitted.
func (h *Handlers) checkDuplicates(w http.ResponseWriter, r *http.Request) {
	var body checkDuplicatesRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	duplicates, err := h.svc.CheckDuplicates(r.Context(), body.Input, body.ExcludeID)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"duplicates": duplicates})
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

	customer, err := h.svc.Update(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, customer)
}

type setActiveRequest struct {
	Active bool   `json:"active"`
	Reason string `json:"reason"`
}

func (h *Handlers) setActive(w http.ResponseWriter, r *http.Request) {
	var body setActiveRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	customer, err := h.svc.SetActive(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body.Active, body.Reason, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, customer)
}

// intParam parses an optional non-negative integer query parameter.
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
	if duplicates, ok := AsDuplicatesError(err); ok {
		// 409 with the candidates attached: the operator decides, not the server.
		return httpx.ErrConflict.
			WithMessage("נמצאו לקוחות דומים. בדקו אם מדובר באותו לקוח.").
			WithDetails(map[string]any{
				"code":       "DUPLICATE_CUSTOMER",
				"duplicates": duplicates.Duplicates,
			}).
			WithCause(err)
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.ErrNotFound.WithMessage("הלקוח לא נמצא.")
	case errors.Is(err, ErrInvalidType):
		return httpx.ValidationError(map[string]string{"customer_type": "סוג לקוח לא מוכר"})
	case errors.Is(err, ErrInvalidDeliv):
		return httpx.ValidationError(map[string]string{"preferred_delivery": "אמצעי משלוח לא מוכר"})
	default:
		return httpx.ErrInternal.WithCause(err)
	}
}
