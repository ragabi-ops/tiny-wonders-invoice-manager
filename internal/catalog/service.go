package catalog

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Service performs catalogue use cases.
type Service struct {
	pool  *db.Pool
	repo  *Repository
	audit *audit.Recorder
}

// NewService wires the catalogue service.
func NewService(pool *db.Pool, repo *Repository, recorder *audit.Recorder) *Service {
	return &Service{pool: pool, repo: repo, audit: recorder}
}

// List returns a page of catalogue items.
func (s *Service) List(ctx context.Context, p ListParams) (*Page, error) {
	return s.repo.List(ctx, s.pool, p)
}

// Get returns one catalogue item.
func (s *Service) Get(ctx context.Context, id string) (*Item, error) {
	return s.repo.Get(ctx, s.pool, id)
}

// Categories returns the categories already in use.
func (s *Service) Categories(ctx context.Context) ([]string, error) {
	return s.repo.Categories(ctx, s.pool)
}

// Create adds a catalogue item.
func (s *Service) Create(ctx context.Context, actor *users.User, in Input, requestID string) (*Item, error) {
	in.Normalize()

	var created *Item
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		item, err := s.repo.Create(ctx, tx, in, actor.ID)
		if err != nil {
			return err
		}
		created = item

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpServiceCreated,
			EntityType:  audit.EntityService,
			EntityID:    item.ID,
			RequestID:   requestID,
			After:       item,
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Update replaces a catalogue item's details. A price change is audited with
// both values, because "what did this cost in March" is a question the business
// will eventually ask.
func (s *Service) Update(ctx context.Context, actor *users.User, id string, in Input, requestID string) (*Item, error) {
	in.Normalize()

	var updated *Item
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}

		item, err := s.repo.Update(ctx, tx, id, in, actor.ID)
		if err != nil {
			return err
		}
		updated = item

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpServiceUpdated,
			EntityType:  audit.EntityService,
			EntityID:    id,
			RequestID:   requestID,
			Before:      before,
			After:       item,
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// SetActive hides or restores a catalogue item.
func (s *Service) SetActive(ctx context.Context, actor *users.User, id string, active bool, requestID string) (*Item, error) {
	var updated *Item

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Active == active {
			updated = before
			return nil
		}

		item, err := s.repo.SetActive(ctx, tx, id, active, actor.ID)
		if err != nil {
			return err
		}
		updated = item

		operation := audit.OpServiceArchived
		if active {
			operation = audit.OpServiceRestored
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   operation,
			EntityType:  audit.EntityService,
			EntityID:    id,
			RequestID:   requestID,
			Before:      map[string]any{"active": before.Active},
			After:       map[string]any{"active": item.Active},
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}
