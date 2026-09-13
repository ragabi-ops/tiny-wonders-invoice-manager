package search

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
)

// Handlers expose the global search endpoint.
type Handlers struct {
	svc *Service
}

// NewHandlers builds the search HTTP layer.
func NewHandlers(svc *Service) *Handlers { return &Handlers{svc: svc} }

// Routes mounts global search.
func (h *Handlers) Routes(r chi.Router) {
	r.Get("/", h.search)
}

func (h *Handlers) search(w http.ResponseWriter, r *http.Request) {
	results, err := h.svc.Search(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, results)
}
