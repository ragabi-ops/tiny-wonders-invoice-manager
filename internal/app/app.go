// Package app wires the API together: services, handlers, router and the
// background work the process runs. Keeping the wiring here — rather than in
// apps/api — lets tests build the real router against a real database.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

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
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/idempotency"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/jobs"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/payments"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/pdf"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/reports"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/search"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/settings"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/storage"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// New wires every dependency of the API. A failure here is a deployment fault —
// an unusable storage directory, say — and must stop the process rather than
// surface later as a failed issuance.
func New(cfg *config.Config, pool *db.Pool, migrationsFS fs.FS, log *slog.Logger) (*App, error) {
	recorder := audit.NewRecorder()
	userRepo := users.NewRepository()
	sessions := auth.NewSessionStore(cfg.SessionTTL, cfg.SessionIdleTimeout, cfg.SecureCookies)

	authService := auth.NewService(pool, userRepo, sessions, recorder)
	middleware := auth.NewMiddleware(sessions, userRepo, pool)

	// currentUser is safe on any route behind RequireAuth.
	currentUser := func(r *http.Request) *users.User { return auth.MustUser(r.Context()) }

	// The password rule lives in internal/auth; this adapter translates its
	// failures so internal/users need not import internal/auth.
	hashPassword := func(password string) (string, error) {
		hash, err := auth.HashPassword(password)
		if err != nil {
			if errors.Is(err, auth.ErrPasswordTooShort) || errors.Is(err, auth.ErrPasswordTooLong) {
				return "", fmt.Errorf("%w: הסיסמה חייבת להכיל לפחות %d תווים",
					users.ErrWeakPassword, auth.MinPasswordLength)
			}
			return "", err
		}
		return hash, nil
	}

	fileStore, err := storage.NewStore(cfg.StorageDir)
	if err != nil {
		return nil, fmt.Errorf("file storage: %w", err)
	}

	settingsService := settings.NewService(pool, recorder, fileStore)
	complianceService := compliance.NewService(settingsService, cfg.Location())
	userService := users.NewService(pool, userRepo, recorder, hashPassword, sessions.DeleteAllForUser)

	customerService := customers.NewService(pool, customers.NewRepository(), recorder)
	catalogService := catalog.NewService(pool, catalog.NewRepository(), recorder)
	activityService := activities.NewService(pool, activities.NewRepository(), recorder)
	searchService := search.NewService(pool)

	// A missing renderer is not a soft failure: without one, no document can be
	// issued, and the Unavailable renderer says so rather than letting a
	// document be issued with no artifact (plan.md 3.4).
	var renderer pdf.Renderer = pdf.Unavailable{}
	if cfg.GotenbergURL != "" {
		renderer = pdf.NewGotenberg(cfg.GotenbergURL)
	} else {
		log.Warn("no PDF renderer configured; document issuance will be refused",
			"hint", "set GOTENBERG_URL")
	}

	idempotencyStore := idempotency.NewStore()
	documentService := documents.NewService(
		pool, documents.NewRepository(), documents.NewSequenceAllocator(), recorder,
		settingsService, complianceService, renderer, fileStore, cfg.Location())

	paymentService := payments.NewService(pool, payments.NewRepository(), documentService, recorder)
	expenseService := expenses.NewService(pool, expenses.NewRepository(), recorder, fileStore)
	reportService := reports.NewService(pool, settingsService, complianceService, cfg.Location())

	// Email is optional. Without it the UI offers WhatsApp share and download
	// instead of a send that cannot work.
	var mailer delivery.Mailer = delivery.Unavailable{}
	if cfg.SMTPHost != "" {
		mailer = delivery.NewSMTPMailer(delivery.SMTPConfig{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			From:     cfg.SMTPFrom,
			FromName: cfg.SMTPFromName,
			StartTLS: cfg.SMTPStartTLS,
		})
	} else {
		log.Warn("email is not configured; documents can be shared by WhatsApp or downloaded",
			"hint", "set SMTP_HOST and SMTP_FROM")
	}

	backupService := backup.NewService(backup.Config{
		Directory:     cfg.BackupDir,
		DatabaseURL:   cfg.DatabaseURL,
		StorageDir:    cfg.StorageDir,
		PgDumpPath:    cfg.PgDumpPath,
		RetentionDays: cfg.BackupRetentionDays,
		Location:      cfg.Location(),
		Hour:          cfg.BackupHour,
		Minute:        cfg.BackupMinute,
	}, pool, recorder)

	queue := jobs.NewQueue()
	deliveryService := delivery.NewService(
		pool, documentService, settingsService, mailer, queue, recorder, cfg.Location())
	exportService := exports.NewService(pool, fileStore, queue, recorder)

	// Sending and export building run off the request path, so neither can hold
	// up — or undo — an issuance (plan.md 14, 17).
	worker := jobs.NewWorker(pool, queue, log, cfg.JobPollInterval)
	worker.Register(jobs.KindSendDocument, deliveryService.HandleSendJob)
	worker.Register(jobs.KindBuildExport, exportService.HandleBuildJob)

	return &App{
		cfg:                cfg,
		renderer:           renderer,
		idempotency:        idempotencyStore,
		queue:              queue,
		worker:             worker,
		backup:             backupService,
		pool:               pool,
		log:                log,
		migrations:         migrationsFS,
		auth:               authService,
		authHandlers:       auth.NewHandlers(authService),
		userHandlers:       users.NewHandlers(userService, currentUser),
		settingsHandlers:   settings.NewHandlers(settingsService, currentUser),
		auditHandlers:      audit.NewHandlers(recorder, pool),
		complianceHandlers: compliance.NewHandlers(complianceService),
		customerHandlers:   customers.NewHandlers(customerService, currentUser),
		catalogHandlers:    catalog.NewHandlers(catalogService, currentUser),
		activityHandlers:   activities.NewHandlers(activityService, currentUser),
		searchHandlers:     search.NewHandlers(searchService),
		documentHandlers:   documents.NewHandlers(documentService, idempotencyStore, pool, currentUser),
		paymentHandlers:    payments.NewHandlers(paymentService, idempotencyStore, pool, currentUser),
		expenseHandlers:    expenses.NewHandlers(expenseService, currentUser),
		deliveryHandlers:   delivery.NewHandlers(deliveryService, currentUser),
		reportHandlers:     reports.NewHandlers(reportService),
		exportHandlers:     exports.NewHandlers(exportService, queue, pool, recorder, currentUser),
		backupHandlers:     backup.NewHandlers(backupService, currentUser),
		middleware:         middleware,
	}, nil
}

// EnsureBootstrapOwner creates the first OWNER when the database has no users.
func (app *App) EnsureBootstrapOwner(ctx context.Context) (bool, error) {
	return app.auth.EnsureBootstrapOwner(ctx,
		app.cfg.BootstrapOwnerEmail, app.cfg.BootstrapOwnerPassword, app.cfg.BootstrapOwnerName)
}

// RunWorker processes background jobs until ctx is cancelled.
func (app *App) RunWorker(ctx context.Context) { app.worker.Run(ctx) }

// BackupNow performs one backup immediately and reports where it landed. It is
// the command-line entry point, so it takes no actor.
func (app *App) BackupNow(ctx context.Context) error {
	run, err := app.backup.Create(ctx, backup.TriggerManual, nil)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}

	path := ""
	if run.ArchivePath != nil {
		path = *run.ArchivePath
	}
	var size int64
	if run.ArchiveBytes != nil {
		size = *run.ArchiveBytes
	}
	files := 0
	if run.FileCount != nil {
		files = *run.FileCount
	}

	app.log.Info("backup complete", "archive", path, "bytes", size, "files", files)
	return nil
}

// RunBackupScheduler runs the daily backup until ctx is cancelled.
func (app *App) RunBackupScheduler(ctx context.Context) {
	app.backup.RunScheduler(ctx, app.log)
}

// SweepSessions deletes expired sessions, stale idempotency keys and old
// succeeded jobs until ctx is cancelled, so none of those tables grows without
// bound (SECURITY.md 2).
func (app *App) SweepSessions(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := app.auth.SweepExpiredSessions(ctx)
			if err != nil {
				app.log.Error("sweep expired sessions", "error", err)
			} else if deleted > 0 {
				app.log.Info("expired sessions removed", "count", deleted)
			}

			// Keys only have to outlive a client's retry window; the actions
			// they made idempotent are kept forever in their own tables.
			keys, err := app.idempotency.DeleteOlderThan(ctx, app.pool, "7 days")
			if err != nil {
				app.log.Error("sweep idempotency keys", "error", err)
			} else if keys > 0 {
				app.log.Info("stale idempotency keys removed", "count", keys)
			}

			// Succeeded jobs are pruned; failures are kept, because they are the
			// record of something that needed attention.
			finished, err := app.queue.DeleteFinishedOlderThan(ctx, app.pool, "30 days")
			if err != nil {
				app.log.Error("sweep finished jobs", "error", err)
			} else if finished > 0 {
				app.log.Info("old succeeded jobs removed", "count", finished)
			}
		}
	}
}
