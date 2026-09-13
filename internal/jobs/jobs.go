// Package jobs is a PostgreSQL-backed work queue for everything that must not
// hold up a request: sending email, building exports, retrying failures
// (plan.md 17).
//
// Official document issuance deliberately does NOT run here. It stays
// synchronous, because a document must exist with its number and its PDF by the
// time the operator sees a response.
//
// Workers claim with FOR UPDATE SKIP LOCKED, so two API instances never run the
// same job, and no external broker is needed.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
)

// Kinds of work the queue carries.
const (
	KindSendDocument = "SEND_DOCUMENT"
	KindBuildExport  = "BUILD_EXPORT"
)

// State values, mirroring the database CHECK.
const (
	StatePending   = "PENDING"
	StateRunning   = "RUNNING"
	StateSucceeded = "SUCCEEDED"
	StateFailed    = "FAILED"
	StateCancelled = "CANCELLED"
)

// ErrDuplicate means a live job already exists for the same dedupe key.
var ErrDuplicate = errors.New("a job with this key is already queued or running")

// Job is one unit of queued work.
type Job struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	State       string          `json:"state"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	RunAt       time.Time       `json:"run_at"`
	StartedAt   *time.Time      `json:"started_at"`
	FinishedAt  *time.Time      `json:"finished_at"`
	LastError   *string         `json:"last_error"`
	// Result is what a successful job produced, when it produces anything.
	Result    json.RawMessage `json:"result,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// EnqueueParams describes work to queue.
type EnqueueParams struct {
	Kind        string
	Payload     any
	MaxAttempts int
	// RunAt delays the first attempt. Zero means immediately.
	RunAt time.Time
	// DedupeKey collapses repeated enqueues into one live job, so a double
	// click does not send the same email twice.
	DedupeKey string
	ActorID   string
}

// Queue enqueues and claims work.
type Queue struct{}

// NewQueue returns a stateless queue handle.
func NewQueue() *Queue { return &Queue{} }

// Enqueue adds a job. It takes a Querier so it can run inside the transaction
// that produced the work — a queued send commits with the action that asked for
// it, or not at all.
func (q *Queue) Enqueue(ctx context.Context, qq db.Querier, p EnqueueParams) (string, error) {
	payload, err := json.Marshal(p.Payload)
	if err != nil {
		return "", fmt.Errorf("encode job payload: %w", err)
	}
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 5
	}
	runAt := p.RunAt
	if runAt.IsZero() {
		runAt = time.Now()
	}

	var (
		id        string
		dedupeArg any
		actorArg  any
	)
	if p.DedupeKey != "" {
		dedupeArg = p.DedupeKey
	}
	if p.ActorID != "" {
		actorArg = p.ActorID
	}

	err = qq.QueryRow(ctx, `
		INSERT INTO background_jobs (kind, payload, max_attempts, run_at, dedupe_key, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text`,
		p.Kind, payload, p.MaxAttempts, runAt, dedupeArg, actorArg).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", ErrDuplicate
		}
		return "", fmt.Errorf("enqueue %s: %w", p.Kind, err)
	}
	return id, nil
}

// Handler runs one job. Returning an error schedules a retry until the job runs
// out of attempts. A non-nil result is stored on the job, so whatever the job
// produced can be found afterwards.
type Handler func(ctx context.Context, job *Job) (result any, err error)

// Worker polls the queue and runs jobs.
type Worker struct {
	pool     *db.Pool
	queue    *Queue
	log      *slog.Logger
	handlers map[string]Handler
	interval time.Duration

	mu sync.RWMutex
}

// NewWorker builds a worker polling every interval.
func NewWorker(pool *db.Pool, queue *Queue, log *slog.Logger, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Worker{
		pool:     pool,
		queue:    queue,
		log:      log,
		handlers: map[string]Handler{},
		interval: interval,
	}
}

// Register attaches a handler to a job kind.
func (w *Worker) Register(kind string, handler Handler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handlers[kind] = handler
}

// Run polls until ctx is cancelled. Polling is deliberately simple: this is a
// small business, and a few seconds of latency on an email is not worth a
// notification channel and the operational surface it brings.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Drain what is ready, then wait for the next tick.
			for {
				ran, err := w.runOnce(ctx)
				if err != nil {
					w.log.Error("job worker", "error", err)
					break
				}
				if !ran {
					break
				}
			}
		}
	}
}

// runOnce claims and runs at most one job. It reports whether one ran.
func (w *Worker) runOnce(ctx context.Context) (bool, error) {
	job, err := w.claim(ctx)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}

	w.mu.RLock()
	handler, ok := w.handlers[job.Kind]
	w.mu.RUnlock()

	if !ok {
		// An unknown kind is a deployment mistake, not a transient failure;
		// retrying would never fix it.
		w.fail(ctx, job, fmt.Errorf("no handler registered for %q", job.Kind), false)
		return true, nil
	}

	// A handler must not be able to hang the worker forever.
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	result, err := handler(runCtx, job)
	if err != nil {
		w.log.Warn("job failed", "id", job.ID, "kind", job.Kind,
			"attempt", job.Attempts, "error", err)
		w.fail(ctx, job, err, job.Attempts < job.MaxAttempts)
		return true, nil
	}

	var encoded any
	if result != nil {
		if encoded, err = json.Marshal(result); err != nil {
			return true, fmt.Errorf("encode job result: %w", err)
		}
	}

	if _, err := w.pool.Exec(ctx, `
		UPDATE background_jobs
		SET state = 'SUCCEEDED', finished_at = now(), last_error = NULL, result = $2
		WHERE id = $1`, job.ID, encoded); err != nil {
		return true, fmt.Errorf("mark job succeeded: %w", err)
	}

	w.log.Info("job succeeded", "id", job.ID, "kind", job.Kind, "attempt", job.Attempts)
	return true, nil
}

// claim takes the next due job. SKIP LOCKED lets several workers run side by
// side without ever handing the same row to two of them.
func (w *Worker) claim(ctx context.Context) (*Job, error) {
	var job Job

	err := db.InTx(ctx, w.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `
			SELECT id::text
			FROM background_jobs
			WHERE state = 'PENDING' AND run_at <= now()
			ORDER BY run_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1`).Scan(&id)
		if err != nil {
			return err
		}

		return tx.QueryRow(ctx, `
			UPDATE background_jobs
			SET state = 'RUNNING', attempts = attempts + 1, started_at = now()
			WHERE id = $1
			RETURNING id::text, kind, payload, state, attempts, max_attempts,
			          run_at, started_at, finished_at, last_error, result, created_at`, id).
			Scan(&job.ID, &job.Kind, &job.Payload, &job.State, &job.Attempts,
				&job.MaxAttempts, &job.RunAt, &job.StartedAt, &job.FinishedAt,
				&job.LastError, &job.Result, &job.CreatedAt)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

// fail records the error and either schedules a retry with exponential backoff
// or gives up.
func (w *Worker) fail(ctx context.Context, job *Job, cause error, retry bool) {
	if !retry {
		if _, err := w.pool.Exec(ctx, `
			UPDATE background_jobs
			SET state = 'FAILED', finished_at = now(), last_error = $2
			WHERE id = $1`, job.ID, cause.Error()); err != nil {
			w.log.Error("mark job failed", "id", job.ID, "error", err)
		}
		return
	}

	backoff := Backoff(job.Attempts)
	if _, err := w.pool.Exec(ctx, `
		UPDATE background_jobs
		SET state = 'PENDING', run_at = now() + $2::interval, last_error = $3
		WHERE id = $1`, job.ID, backoff.String(), cause.Error()); err != nil {
		w.log.Error("reschedule job", "id", job.ID, "error", err)
	}
}

// Backoff is the delay before retry number `attempt`: 30s, 1m, 2m, 4m, capped
// at 15 minutes. Long enough to ride out a mail server hiccup, short enough
// that a document is not stuck unsent for an afternoon.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 30 * time.Second * time.Duration(math.Pow(2, float64(attempt-1)))
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}

// Stats summarizes the queue for the dashboard and the readiness probe
// (plan.md 18).
type Stats struct {
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Failed    int `json:"failed"`
	Succeeded int `json:"succeeded"`
}

// Counts returns how many jobs sit in each state.
func (q *Queue) Counts(ctx context.Context, qq db.Querier) (*Stats, error) {
	rows, err := qq.Query(ctx, `SELECT state, count(*) FROM background_jobs GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := &Stats{}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		switch state {
		case StatePending:
			stats.Pending = count
		case StateRunning:
			stats.Running = count
		case StateFailed:
			stats.Failed = count
		case StateSucceeded:
			stats.Succeeded = count
		}
	}
	return stats, rows.Err()
}

// Get returns one job.
func (q *Queue) Get(ctx context.Context, qq db.Querier, id string) (*Job, error) {
	var job Job
	err := qq.QueryRow(ctx, `
		SELECT id::text, kind, payload, state, attempts, max_attempts,
		       run_at, started_at, finished_at, last_error, result, created_at
		FROM background_jobs WHERE id = $1`, id).
		Scan(&job.ID, &job.Kind, &job.Payload, &job.State, &job.Attempts,
			&job.MaxAttempts, &job.RunAt, &job.StartedAt, &job.FinishedAt,
			&job.LastError, &job.Result, &job.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("job %s not found", id)
		}
		return nil, err
	}
	return &job, nil
}

// RecentFailures lists jobs that gave up, newest first, for the dashboard.
func (q *Queue) RecentFailures(ctx context.Context, qq db.Querier, limit int) ([]Job, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	rows, err := qq.Query(ctx, `
		SELECT id::text, kind, payload, state, attempts, max_attempts,
		       run_at, started_at, finished_at, last_error, result, created_at
		FROM background_jobs
		WHERE state = 'FAILED'
		ORDER BY finished_at DESC NULLS LAST
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	failures := []Job{}
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.Kind, &job.Payload, &job.State, &job.Attempts,
			&job.MaxAttempts, &job.RunAt, &job.StartedAt, &job.FinishedAt,
			&job.LastError, &job.Result, &job.CreatedAt); err != nil {
			return nil, err
		}
		failures = append(failures, job)
	}
	return failures, rows.Err()
}

// Retry puts a failed job back in the queue, for an operator who has fixed
// whatever was wrong.
func (q *Queue) Retry(ctx context.Context, qq db.Querier, id string) error {
	tag, err := qq.Exec(ctx, `
		UPDATE background_jobs
		SET state = 'PENDING', run_at = now(), attempts = 0,
		    started_at = NULL, finished_at = NULL
		WHERE id = $1 AND state = 'FAILED'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %s is not in a failed state", id)
	}
	return nil
}

// DeleteFinishedOlderThan prunes succeeded jobs. Failures are kept: they are
// the record of something that needed attention.
func (q *Queue) DeleteFinishedOlderThan(ctx context.Context, qq db.Querier, interval string) (int64, error) {
	tag, err := qq.Exec(ctx,
		`DELETE FROM background_jobs WHERE state = 'SUCCEEDED' AND finished_at < now() - $1::interval`,
		interval)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
