package app

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/activities"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/auth"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/backup"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/catalog"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/compliance"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/config"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/customers"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/delivery"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/documents"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/expenses"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/exports"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/idempotency"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/jobs"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/payments"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/pdf"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/reports"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/search"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/settings"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// App holds the wired dependencies of the API. Construction happens once, in
// New, so the dependency graph is visible in one place.
type App struct {
	cfg        *config.Config
	pool       *db.Pool
	log        *slog.Logger
	migrations fs.FS

	auth        *auth.Service
	renderer    pdf.Renderer
	idempotency *idempotency.Store
	queue       *jobs.Queue
	worker      *jobs.Worker
	backup      *backup.Service

	authHandlers       *auth.Handlers
	userHandlers       *users.Handlers
	settingsHandlers   *settings.Handlers
	auditHandlers      *audit.Handlers
	complianceHandlers *compliance.Handlers
	customerHandlers   *customers.Handlers
	catalogHandlers    *catalog.Handlers
	activityHandlers   *activities.Handlers
	searchHandlers     *search.Handlers
	documentHandlers   *documents.Handlers
	paymentHandlers    *payments.Handlers
	expenseHandlers    *expenses.Handlers
	deliveryHandlers   *delivery.Handlers
	reportHandlers     *reports.Handlers
	exportHandlers     *exports.Handlers
	backupHandlers     *backup.Handlers
	middleware         *auth.Middleware
}

// Handler builds the HTTP router. Every non-public route sits behind
// RequireAuth, and every one of those carries an explicit role requirement:
// deny is the default (SECURITY.md 5).
func (app *App) Handler() http.Handler {
	r := chi.NewRouter()

	r.NotFound(httpx.NotFoundHandler())
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler())

	r.Use(httpx.RequestContext(app.log))
	r.Use(httpx.Recover)
	r.Use(httpx.SecurityHeaders(app.cfg.IsProduction()))
	r.Use(httpx.CORS(app.cfg.CORSOrigins))
	r.Use(httpx.AccessLog)

	// Probes are unauthenticated so an orchestrator can reach them, and they
	// expose no business data (plan.md 18).
	r.Get("/healthz", app.healthz)
	r.Get("/readyz", app.readyz)

	r.Route("/api/v1", func(r chi.Router) {
		// A global ceiling on top of the per-account and per-IP login limits.
		r.Use(httpx.LimitByIP(httpx.NewRateLimiter(600, 120)))

		r.Route("/auth", func(r chi.Router) {
			app.authHandlers.Routes(r)

			r.Group(func(r chi.Router) {
				r.Use(app.middleware.RequireAuth)
				app.authHandlers.AuthenticatedRoutes(r)
			})
		})

		r.Group(func(r chi.Router) {
			r.Use(app.middleware.RequireAuth)

			// Readable by every role, including READ_ONLY.
			r.Group(func(r chi.Router) {
				r.Use(app.middleware.RequireRole(
					users.RoleOwner, users.RoleOperator, users.RoleAccountant, users.RoleReadOnly))

				r.Route("/compliance", app.complianceHandlers.Routes)
				r.Route("/search", app.searchHandlers.Routes)
				r.Route("/reports", app.reportHandlers.Routes)
				r.Route("/delivery", app.deliveryHandlers.StatusRoutes)
			})

			// Operational data: everyone reads, only OWNER and OPERATOR write
			// (plan.md 6). Each entity mounts once, with the write half behind
			// the stricter requirement.
			r.Route("/customers", func(r chi.Router) {
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(
						users.RoleOwner, users.RoleOperator, users.RoleAccountant, users.RoleReadOnly))
					app.customerHandlers.ReadRoutes(r)
					app.paymentHandlers.CustomerRoutes(r)
				})
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(users.RoleOwner, users.RoleOperator))
					app.customerHandlers.WriteRoutes(r)
				})
			})

			r.Route("/services", func(r chi.Router) {
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(
						users.RoleOwner, users.RoleOperator, users.RoleAccountant, users.RoleReadOnly))
					app.catalogHandlers.ReadRoutes(r)
				})
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(users.RoleOwner, users.RoleOperator))
					app.catalogHandlers.WriteRoutes(r)
				})
			})

			r.Route("/payments", func(r chi.Router) {
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(
						users.RoleOwner, users.RoleOperator, users.RoleAccountant, users.RoleReadOnly))
					app.paymentHandlers.ReadRoutes(r)
				})
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(users.RoleOwner, users.RoleOperator))
					app.paymentHandlers.WriteRoutes(r)
				})
			})

			r.Route("/documents", func(r chi.Router) {
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(
						users.RoleOwner, users.RoleOperator, users.RoleAccountant, users.RoleReadOnly))
					app.documentHandlers.ReadRoutes(r)
					app.deliveryHandlers.ReadRoutes(r)
				})
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(users.RoleOwner, users.RoleOperator))
					app.documentHandlers.WriteRoutes(r)
					app.deliveryHandlers.WriteRoutes(r)
				})
			})

			r.Route("/expenses", func(r chi.Router) {
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(
						users.RoleOwner, users.RoleOperator, users.RoleAccountant, users.RoleReadOnly))
					app.expenseHandlers.ReadRoutes(r)
				})
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(users.RoleOwner, users.RoleOperator))
					app.expenseHandlers.WriteRoutes(r)
				})
			})

			// Exports are the accountant hand-off, so the accountant may build
			// and download them as well as the owner (plan.md 6, 15).
			r.Group(func(r chi.Router) {
				r.Use(app.middleware.RequireRole(users.RoleOwner, users.RoleAccountant))
				r.Route("/exports", app.exportHandlers.Routes)
			})

			r.Route("/activities", func(r chi.Router) {
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(
						users.RoleOwner, users.RoleOperator, users.RoleAccountant, users.RoleReadOnly))
					app.activityHandlers.ReadRoutes(r)
				})
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(users.RoleOwner, users.RoleOperator))
					app.activityHandlers.WriteRoutes(r)
				})
			})

			// Settings mount once; the write half is OWNER-only.
			r.Route("/settings", func(r chi.Router) {
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(
						users.RoleOwner, users.RoleOperator, users.RoleAccountant, users.RoleReadOnly))
					app.settingsHandlers.ReadRoutes(r)
				})
				r.Group(func(r chi.Router) {
					r.Use(app.middleware.RequireRole(users.RoleOwner))
					app.settingsHandlers.OwnerRoutes(r)
				})
			})

			// OWNER only: user administration.
			r.Group(func(r chi.Router) {
				r.Use(app.middleware.RequireRole(users.RoleOwner))
				r.Route("/users", app.userHandlers.Routes)
				r.Route("/backups", app.backupHandlers.Routes)
			})

			// The audit log is also readable by the accountant, who needs it for
			// reconciliation (plan.md 6).
			r.Group(func(r chi.Router) {
				r.Use(app.middleware.RequireRole(users.RoleOwner, users.RoleAccountant))
				r.Route("/audit", app.auditHandlers.Routes)
			})
		})
	})

	return r
}

// healthz reports that the process is alive. It touches nothing external, so a
// database outage does not make the process look dead and get restarted.
func (app *App) healthz(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// readyz reports whether the API can serve requests: the database must answer
// and every migration must be applied.
func (app *App) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{}
	ready := true

	if err := app.pool.Ping(ctx); err != nil {
		checks["database"] = "unavailable"
		ready = false
	} else {
		checks["database"] = "ok"
	}

	if ready {
		known, applied, err := db.MigrationStatus(ctx, app.pool, app.migrations)
		switch {
		case err != nil:
			checks["migrations"] = "unknown"
			ready = false
		case len(applied) < len(known):
			checks["migrations"] = "pending"
			ready = false
		default:
			checks["migrations"] = "ok"
		}
	}

	// The renderer is reported but does not gate readiness: the API can still
	// serve everything except issuance without it, and issuance fails loudly on
	// its own.
	switch renderer := app.renderer.(type) {
	case *pdf.Gotenberg:
		if err := renderer.Health(ctx); err != nil {
			checks["pdf_renderer"] = "unavailable"
		} else {
			checks["pdf_renderer"] = "ok"
		}
	default:
		checks["pdf_renderer"] = "not configured"
	}

	// Failed jobs are surfaced but do not gate readiness: the API serves fine
	// with a stuck email, and the dashboard is where an operator acts on it.
	if ready {
		if stats, err := app.queue.Counts(ctx, app.pool); err == nil {
			checks["jobs_pending"] = strconv.Itoa(stats.Pending)
			checks["jobs_failed"] = strconv.Itoa(stats.Failed)
		}
	}

	// Backup staleness is reported, not fatal: an API that refuses traffic
	// because last night's backup failed helps nobody.
	if backupStatus, err := app.backup.Status(ctx); err == nil {
		switch {
		case !backupStatus.Configured:
			checks["backups"] = "not configured"
		case backupStatus.Stale:
			checks["backups"] = "stale"
		default:
			checks["backups"] = "ok"
		}
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	httpx.WriteJSON(w, r, status, map[string]any{"ready": ready, "checks": checks})
}
