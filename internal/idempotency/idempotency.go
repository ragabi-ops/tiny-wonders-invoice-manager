// Package idempotency makes critical POSTs retry-safe: a repeated request with
// the same key returns the original response instead of acting twice
// (plan.md 10, 17).
//
// A client sends `Idempotency-Key`. The first request stores its response; a
// replay reads it back. A key reused with a *different* body is a client bug
// and is refused rather than silently answered with the wrong response.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
)

// MaxKeyLength bounds what a client may send.
const MaxKeyLength = 200

// ErrKeyReused means the key was already used for a different request body.
var ErrKeyReused = errors.New("idempotency key was used with a different request")

// Record is a stored response.
type Record struct {
	Status   int
	Body     json.RawMessage
	EntityID string
}

// Fingerprint hashes a request body so a replay can be told apart from a
// different request that happens to reuse the key.
func Fingerprint(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Normalize trims a client-supplied key and reports whether it is usable.
func Normalize(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > MaxKeyLength {
		return "", false
	}
	return key, true
}

// Store persists responses to keyed requests.
type Store struct{}

// NewStore returns a stateless store.
func NewStore() *Store { return &Store{} }

// Lookup returns the stored response for a key, or nil if this is the first
// time it has been seen. It fails with ErrKeyReused when the key exists but was
// recorded against a different request body.
func (s *Store) Lookup(ctx context.Context, q db.Querier, scope, key, fingerprint string) (*Record, error) {
	var (
		record      Record
		storedPrint string
		entityID    *string
	)
	err := q.QueryRow(ctx, `
		SELECT request_fingerprint, response_status, response_body, entity_id
		FROM idempotency_keys
		WHERE scope = $1 AND key = $2`, scope, key).
		Scan(&storedPrint, &record.Status, &record.Body, &entityID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("look up idempotency key: %w", err)
	}

	if storedPrint != fingerprint {
		return nil, ErrKeyReused
	}
	if entityID != nil {
		record.EntityID = *entityID
	}
	return &record, nil
}

// Save records the response for a key. It must run in the same transaction as
// the action it makes idempotent, so a committed action always has its key
// stored and a rolled-back one leaves none.
//
// A concurrent request holding the same key loses the insert; the caller treats
// that as a replay of the winner.
func (s *Store) Save(ctx context.Context, q db.Querier, scope, key, fingerprint, userID string, record Record) error {
	var userArg any
	if userID != "" {
		userArg = userID
	}
	var entityArg any
	if record.EntityID != "" {
		entityArg = record.EntityID
	}

	_, err := q.Exec(ctx, `
		INSERT INTO idempotency_keys
			(scope, key, request_fingerprint, user_id, response_status, response_body, entity_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		scope, key, fingerprint, userArg, record.Status, record.Body, entityArg)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Someone else got there first with the same key; their response is
			// the authoritative one.
			return ErrConcurrentReplay
		}
		return fmt.Errorf("save idempotency key: %w", err)
	}
	return nil
}

// ErrConcurrentReplay means another request with the same key committed first.
var ErrConcurrentReplay = errors.New("another request with this idempotency key committed first")

// DeleteOlderThan removes stale keys. Retention only has to outlive a client's
// retry window; the actions themselves are kept forever in their own tables.
func (s *Store) DeleteOlderThan(ctx context.Context, q db.Querier, interval string) (int64, error) {
	tag, err := q.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE created_at < now() - $1::interval`, interval)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
