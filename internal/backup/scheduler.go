package backup

import (
	"context"
	"time"
)

// checkInterval is how often the scheduler asks "is a backup due?". Frequent
// enough that a missed window is picked up quickly, cheap enough to ignore.
const checkInterval = 5 * time.Minute

// RunScheduler runs the daily backup until ctx is cancelled.
//
// It is deliberately not a cron expression. The rule is simply: if no backup
// has succeeded since today's scheduled time, run one. That survives the two
// things a small deployment actually does — the machine being asleep at 02:00,
// and the process being restarted — without needing to catch up on a queue of
// missed firings.
func (s *Service) RunScheduler(ctx context.Context, log logger) {
	if !s.Configured() {
		log.Warn("backups are not configured; no daily backup will run",
			"hint", "set BACKUP_DIR")
		return
	}

	log.Info("daily backup scheduled",
		"directory", s.cfg.Directory,
		"at", s.cfg.Location.String(),
		"hour", s.cfg.Hour, "minute", s.cfg.Minute,
		"retention_days", s.cfg.RetentionDays)

	// Say this at startup rather than during a recovery.
	if versions, err := s.CheckToolVersions(ctx); err != nil {
		log.Error("cannot run pg_dump; backups will fail", "error", err)
	} else if versions.Mismatch {
		log.Warn("pg_dump version does not match the database server",
			"pg_dump", versions.PgDumpMajor,
			"server", versions.ServerMajor,
			"consequence", "archives may not restore cleanly",
			"fix", "install a matching client or set PG_DUMP_PATH")
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	// Check once at startup: a machine that was off overnight should back up as
	// soon as it comes back, not wait for tomorrow.
	s.runIfDue(ctx, log)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runIfDue(ctx, log)
		}
	}
}

// runIfDue starts a backup when today's scheduled time has passed and no
// backup has succeeded since it.
func (s *Service) runIfDue(ctx context.Context, log logger) {
	due, err := s.due(ctx)
	if err != nil {
		log.Error("check whether a backup is due", "error", err)
		return
	}
	if !due {
		return
	}

	log.Info("starting the daily backup")
	run, err := s.Create(ctx, TriggerScheduled, nil)
	if err != nil {
		// Already recorded as a failed run; this makes it visible in the log too.
		log.Error("daily backup failed", "error", err)
		return
	}

	log.Info("daily backup finished",
		"archive", derefString(run.ArchivePath),
		"bytes", derefInt64(run.ArchiveBytes),
		"files", derefInt(run.FileCount))
}

// due reports whether the scheduled time has passed today without a successful
// backup after it.
func (s *Service) due(ctx context.Context) (bool, error) {
	now := time.Now().In(s.cfg.Location)
	scheduled := time.Date(now.Year(), now.Month(), now.Day(),
		s.cfg.Hour, s.cfg.Minute, 0, 0, s.cfg.Location)

	if now.Before(scheduled) {
		return false, nil
	}

	last, err := s.LastSucceeded(ctx)
	if err != nil {
		return false, err
	}
	if last == nil || last.FinishedAt == nil {
		// Never backed up: do it now rather than wait for tomorrow.
		return true, nil
	}
	return last.FinishedAt.Before(scheduled), nil
}

// logger is the slice of slog this package uses, so tests can pass a quiet one.
type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
