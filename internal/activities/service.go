package activities

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Service performs activity use cases.
type Service struct {
	pool  *db.Pool
	repo  *Repository
	audit *audit.Recorder
}

// NewService wires the activity service.
func NewService(pool *db.Pool, repo *Repository, recorder *audit.Recorder) *Service {
	return &Service{pool: pool, repo: repo, audit: recorder}
}

// List returns a page of activities.
func (s *Service) List(ctx context.Context, p ListParams) (*Page, error) {
	return s.repo.List(ctx, s.pool, p)
}

// Get returns one activity.
func (s *Service) Get(ctx context.Context, id string) (*Activity, error) {
	return s.repo.Get(ctx, s.pool, id)
}

// Create adds an activity.
func (s *Service) Create(ctx context.Context, actor *users.User, in Input, requestID string) (*Activity, error) {
	in.Normalize()

	var created *Activity
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		activity, err := s.repo.Create(ctx, tx, in, actor.ID)
		if err != nil {
			return err
		}
		created = activity

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpActivityCreated,
			EntityType:  audit.EntityActivity,
			EntityID:    activity.ID,
			RequestID:   requestID,
			After:       activity,
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Update replaces an activity's details.
func (s *Service) Update(ctx context.Context, actor *users.User, id string, in Input, requestID string) (*Activity, error) {
	in.Normalize()

	var updated *Activity
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}

		activity, err := s.repo.Update(ctx, tx, id, in, actor.ID)
		if err != nil {
			return err
		}
		updated = activity

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpActivityUpdated,
			EntityType:  audit.EntityActivity,
			EntityID:    id,
			RequestID:   requestID,
			Before:      before,
			After:       activity,
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}
