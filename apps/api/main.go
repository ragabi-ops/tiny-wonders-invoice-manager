// Command api is the Invoice & Receipt System backend: one Go binary serving
// the JSON API described in docs/openapi.yaml.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/app"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/config"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/migrations"
)

// migrationsFS is the embedded set of SQL migrations.
var migrationsFS fs.FS = migrations.FS

// logger is the process logger. Structured JSON, no secrets (SECURITY.md 8).
var logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

func main() {
	if err := run(); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		migrateOnly = flag.Bool("migrate", false, "apply pending migrations and exit")
		statusOnly  = flag.Bool("migrate-status", false, "print migration status and exit")
		backupNow   = flag.Bool("backup-now", false, "run one backup and exit")
		envFile     = flag.String("env-file", ".env", "path to an env file; missing files are ignored")
	)
	flag.Parse()

	if err := config.LoadDotEnv(*envFile); err != nil {
		return fmt.Errorf("load env file: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if *statusOnly {
		return printMigrationStatus(ctx, pool)
	}

	// Migrations always run at startup: an API instance must never serve a
	// schema it was not built for.
	if err := db.Migrate(ctx, pool, migrationsFS, logger); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if *migrateOnly {
		logger.Info("migrations applied")
		return nil
	}

	application, err := app.New(cfg, pool, migrationsFS, logger)
	if err != nil {
		return err
	}

	// Running a backup from the command line needs no login. An operator about
	// to do something risky should be able to take one without a browser, and
	// it is what the runbook tells them to do.
	if *backupNow {
		return application.BackupNow(ctx)
	}

	created, err := application.EnsureBootstrapOwner(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap owner: %w", err)
	}
	if created {
		logger.Warn("bootstrap owner created; clear BOOTSTRAP_OWNER_* from the environment now",
			"email", cfg.BootstrapOwnerEmail)
	}

	go application.SweepSessions(ctx)
	// Email and export building run here, off the request path, so a delivery
	// failure can never roll back an issued document (plan.md 14).
	go application.RunWorker(ctx)
	go application.RunBackupScheduler(ctx)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           application.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("api listening",
			"addr", cfg.HTTPAddr, "env", cfg.AppEnv, "timezone", cfg.TimezoneName)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	// Finish in-flight requests before closing the pool: a request that is
	// issuing a document must not be cut in half.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}

func printMigrationStatus(ctx context.Context, pool *db.Pool) error {
	known, applied, err := db.MigrationStatus(ctx, pool, migrationsFS)
	if err != nil {
		return err
	}
	for _, m := range known {
		state := "pending"
		if _, ok := applied[m.Version]; ok {
			state = "applied"
		}
		fmt.Printf("%04d  %-24s %s\n", m.Version, m.Name, state)
	}
	fmt.Printf("\n%d of %d applied\n", len(applied), len(known))
	return nil
}
