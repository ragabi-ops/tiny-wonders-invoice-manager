package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// begin records that a backup started, before any work happens. A run that
// crashes therefore leaves a RUNNING row rather than no evidence at all.
func (s *Service) begin(ctx context.Context, trigger string, actor *users.User) (string, error) {
	var (
		id       string
		actorArg any
	)
	if actor != nil {
		actorArg = actor.ID
	}

	if err := s.pool.QueryRow(ctx, `
		INSERT INTO backup_runs (trigger, triggered_by)
		VALUES ($1, $2)
		RETURNING id::text`, trigger, actorArg).Scan(&id); err != nil {
		return "", fmt.Errorf("record backup start: %w", err)
	}
	return id, nil
}

func (s *Service) finishSucceeded(ctx context.Context, runID string, result *buildResult, actor *users.User) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE backup_runs
			SET state = 'SUCCEEDED', finished_at = now(),
			    archive_path = $2, archive_bytes = $3, archive_sha256 = $4, file_count = $5
			WHERE id = $1`,
			runID, result.Path, result.Bytes, result.SHA256, result.FileCount); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actorID(actor),
			ActorEmail:  actorEmail(actor),
			Operation:   audit.OpBackupCompleted,
			EntityType:  audit.EntityBackup,
			EntityID:    runID,
			After: map[string]any{
				"archive_path": result.Path,
				"bytes":        result.Bytes,
				"sha256":       result.SHA256,
				"file_count":   result.FileCount,
			},
		})
	})
}

func (s *Service) finishFailed(ctx context.Context, runID string, cause error, actor *users.User) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE backup_runs
			SET state = 'FAILED', finished_at = now(), error = $2
			WHERE id = $1`, runID, cause.Error()); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actorID(actor),
			ActorEmail:  actorEmail(actor),
			Operation:   audit.OpBackupFailed,
			EntityType:  audit.EntityBackup,
			EntityID:    runID,
			Reason:      cause.Error(),
		})
	})
}

const runColumns = `
	id::text, started_at, finished_at, state, archive_path, archive_bytes,
	archive_sha256, file_count, trigger, error`

func scanRun(row pgx.Row) (*Run, error) {
	var run Run
	if err := row.Scan(&run.ID, &run.StartedAt, &run.FinishedAt, &run.State,
		&run.ArchivePath, &run.ArchiveBytes, &run.ArchiveSHA, &run.FileCount,
		&run.Trigger, &run.Error); err != nil {
		return nil, err
	}
	return &run, nil
}

// Get returns one recorded run.
func (s *Service) Get(ctx context.Context, id string) (*Run, error) {
	return scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM backup_runs WHERE id = $1`, id))
}

// Recent returns the last runs, newest first.
func (s *Service) Recent(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+runColumns+` FROM backup_runs ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

// LastSucceeded returns the most recent successful run, or nil if there has
// never been one. That distinction is the whole point of the readiness check:
// "no backup has ever succeeded" and "the last one was yesterday" are very
// different situations.
func (s *Service) LastSucceeded(ctx context.Context) (*Run, error) {
	run, err := scanRun(s.pool.QueryRow(ctx, `
		SELECT `+runColumns+`
		FROM backup_runs
		WHERE state = 'SUCCEEDED'
		ORDER BY finished_at DESC
		LIMIT 1`))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return run, nil
}

// Status is what the readiness probe and the dashboard show.
type Status struct {
	Configured bool   `json:"configured"`
	Directory  string `json:"directory,omitempty"`
	// RetentionDays bounds the archives kept in the folder. It is not a records
	// retention policy.
	RetentionDays int `json:"retention_days"`
	// ScheduleLocal is when the daily run happens, in the business timezone.
	ScheduleLocal string `json:"schedule_local,omitempty"`

	LastSucceededAt *time.Time `json:"last_succeeded_at"`
	LastArchiveSize *int64     `json:"last_archive_bytes"`
	// HoursSinceSuccess is how stale the newest good backup is. Null when there
	// has never been one.
	HoursSinceSuccess *float64 `json:"hours_since_success"`
	// Stale is true when the last success is older than a day and a half, which
	// means a daily schedule has missed at least one run.
	Stale bool `json:"stale"`

	LastRunState *string `json:"last_run_state"`
	LastRunError *string `json:"last_run_error"`

	// Tools reports whether pg_dump matches the server. A mismatch produces
	// archives that restore badly, so it is surfaced rather than left to be
	// discovered during an actual recovery.
	Tools *ToolVersions `json:"tools,omitempty"`
}

// staleAfter is how old the newest successful backup may be before the system
// says so. A daily schedule plus headroom for a slow run or a late start.
const staleAfter = 36 * time.Hour

// Status summarizes where backups stand.
func (s *Service) Status(ctx context.Context) (*Status, error) {
	status := &Status{
		Configured:    s.Configured(),
		Directory:     s.cfg.Directory,
		RetentionDays: s.cfg.RetentionDays,
	}
	if s.Configured() {
		status.ScheduleLocal = fmt.Sprintf("%02d:%02d %s", s.cfg.Hour, s.cfg.Minute, s.cfg.Location)
	}

	last, err := s.LastSucceeded(ctx)
	if err != nil {
		return nil, err
	}
	if last != nil && last.FinishedAt != nil {
		status.LastSucceededAt = last.FinishedAt
		status.LastArchiveSize = last.ArchiveBytes

		age := time.Since(*last.FinishedAt)
		hours := age.Hours()
		status.HoursSinceSuccess = &hours
		status.Stale = age > staleAfter
	} else if s.Configured() {
		// Never having succeeded is the stalest state there is.
		status.Stale = true
	}

	if s.Configured() {
		// A tool problem is worth reporting, but not worth failing the status
		// call over.
		if versions, err := s.CheckToolVersions(ctx); err == nil {
			status.Tools = versions
		}
	}

	recent, err := s.Recent(ctx, 1)
	if err != nil {
		return nil, err
	}
	if len(recent) > 0 {
		state := recent[0].State
		status.LastRunState = &state
		status.LastRunError = recent[0].Error
	}

	return status, nil
}

func actorID(actor *users.User) string {
	if actor == nil {
		return ""
	}
	return actor.ID
}

func actorEmail(actor *users.User) string {
	if actor == nil {
		// The scheduled run has no human behind it; the audit trail should say
		// so rather than attribute it to whoever last logged in.
		return "system"
	}
	return actor.Email
}
