package audit

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
)

// Handlers expose the read-only audit log. There is no write endpoint: events
// are recorded by the actions themselves, never by a client.
type Handlers struct {
	recorder *Recorder
	pool     *db.Pool
}

// NewHandlers builds the audit HTTP layer.
func NewHandlers(recorder *Recorder, pool *db.Pool) *Handlers {
	return &Handlers{recorder: recorder, pool: pool}
}

// Routes mounts the audit log.
func (h *Handlers) Routes(r chi.Router) {
	r.Get("/", h.list)
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	params := ListParams{
		Operation:  query.Get("operation"),
		EntityType: query.Get("entity_type"),
		EntityID:   query.Get("entity_id"),
		ActorID:    query.Get("actor_id"),
	}

	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"limit": "ערך לא תקין"}))
			return
		}
		params.Limit = limit
	}

	// Keyset pagination: the client passes the oldest timestamp it has seen.
	if raw := query.Get("before"); raw != "" {
		before, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"before": "יש להזין תאריך בפורמט תקין"}))
			return
		}
		params.Before = &before
	}

	events, err := h.recorder.List(r.Context(), h.pool, params)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"events": events})
}
