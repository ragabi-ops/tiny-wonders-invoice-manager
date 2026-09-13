package reports

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
)

// Handlers expose the read-only reports.
type Handlers struct {
	svc *Service
}

// NewHandlers builds the reports HTTP layer.
func NewHandlers(svc *Service) *Handlers { return &Handlers{svc: svc} }

// Routes mounts the reports. Every role may read them: an accountant needs the
// numbers and a read-only viewer is harmless (plan.md 6).
func (h *Handlers) Routes(r chi.Router) {
	r.Get("/dashboard", h.dashboard)
	r.Get("/revenue", h.revenue)
	r.Get("/unpaid", h.unpaid)
	r.Get("/turnover", h.turnover)
}

func (h *Handlers) dashboard(w http.ResponseWriter, r *http.Request) {
	dashboard, err := h.svc.Dashboard(r.Context())
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, dashboard)
}

// revenue serves every revenue breakdown from one endpoint, chosen by `by`.
func (h *Handlers) revenue(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	period := Range{From: query.Get("from"), To: query.Get("to")}

	var (
		rows []RevenueRow
		err  error
	)
	switch query.Get("by") {
	case "", "period":
		rows, err = h.svc.RevenueByPeriod(r.Context(), period, query.Get("granularity"))
	case "customer":
		rows, err = h.svc.RevenueByCustomer(r.Context(), period)
	case "service":
		rows, err = h.svc.RevenueByService(r.Context(), period)
	case "activity":
		rows, err = h.svc.RevenueByActivity(r.Context(), period)
	default:
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"by": "פילוח לא מוכר"}))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"rows": rows, "range": period})
}

func (h *Handlers) unpaid(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Unpaid(r.Context(), h.svc.Today())
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"documents": rows})
}

func (h *Handlers) turnover(w http.ResponseWriter, r *http.Request) {
	year := h.svc.Today().Year()
	if raw := r.URL.Query().Get("year"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 2000 || parsed > 2200 {
			httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"year": "שנה לא תקינה"}))
			return
		}
		year = parsed
	}

	turnover, err := h.svc.AnnualTurnover(r.Context(), year)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, turnover)
}
