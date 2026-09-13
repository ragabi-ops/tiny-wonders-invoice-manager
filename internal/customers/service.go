package customers

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// ErrDuplicatesFound is returned when a create or update matches existing
// customers and the caller has not confirmed that it meant to.
type ErrDuplicatesFound struct {
	Duplicates []Duplicate
}

func (e *ErrDuplicatesFound) Error() string { return "possible duplicate customers found" }

// Service performs customer use cases.
type Service struct {
	pool  *db.Pool
	repo  *Repository
	audit *audit.Recorder
}

// NewService wires the customer service.
func NewService(pool *db.Pool, repo *Repository, recorder *audit.Recorder) *Service {
	return &Service{pool: pool, repo: repo, audit: recorder}
}

// List returns a page of customers.
func (s *Service) List(ctx context.Context, p ListParams) (*Page, error) {
	return s.repo.List(ctx, s.pool, p)
}

// Get returns one customer.
func (s *Service) Get(ctx context.Context, id string) (*Customer, error) {
	return s.repo.Get(ctx, s.pool, id)
}

// CheckDuplicates reports customers resembling the input, without writing
// anything. The UI calls it while the operator types.
func (s *Service) CheckDuplicates(ctx context.Context, in Input, excludeID string) ([]Duplicate, error) {
	in.Normalize()
	return s.repo.FindDuplicates(ctx, s.pool, in, excludeID)
}

// Create adds a customer. Unless confirmDuplicate is set, it refuses when the
// input resembles an existing customer and returns the candidates so the
// operator can choose (plan.md 8).
func (s *Service) Create(ctx context.Context, actor *users.User, in Input, confirmDuplicate bool, requestID string) (*Customer, error) {
	in.Normalize()

	var created *Customer
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if !confirmDuplicate {
			duplicates, err := s.repo.FindDuplicates(ctx, tx, in, "")
			if err != nil {
				return err
			}
			if len(duplicates) > 0 {
				return &ErrDuplicatesFound{Duplicates: duplicates}
			}
		}

		customer, err := s.repo.Create(ctx, tx, in, actor.ID)
		if err != nil {
			return err
		}
		created = customer

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpCustomerCreated,
			EntityType:  audit.EntityCustomer,
			EntityID:    customer.ID,
			RequestID:   requestID,
			After:       customer,
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Update replaces a customer's details. The previous values are audited, so a
// later question about what a document was issued against can be answered.
func (s *Service) Update(ctx context.Context, actor *users.User, id string, in Input, requestID string) (*Customer, error) {
	in.Normalize()

	var updated *Customer
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}

		customer, err := s.repo.Update(ctx, tx, id, in, actor.ID)
		if err != nil {
			return err
		}
		updated = customer

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpCustomerUpdated,
			EntityType:  audit.EntityCustomer,
			EntityID:    id,
			RequestID:   requestID,
			Before:      before,
			After:       customer,
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// SetActive archives or restores a customer. There is no delete: documents
// reference customers and must stay readable for as long as the law requires
// them kept.
func (s *Service) SetActive(ctx context.Context, actor *users.User, id string, active bool, reason, requestID string) (*Customer, error) {
	var updated *Customer

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Active == active {
			updated = before
			return nil
		}

		customer, err := s.repo.SetActive(ctx, tx, id, active, actor.ID)
		if err != nil {
			return err
		}
		updated = customer

		operation := audit.OpCustomerArchived
		if active {
			operation = audit.OpCustomerRestored
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   operation,
			EntityType:  audit.EntityCustomer,
			EntityID:    id,
			RequestID:   requestID,
			Reason:      reason,
			Before:      map[string]any{"active": before.Active},
			After:       map[string]any{"active": customer.Active},
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// AsDuplicatesError extracts an *ErrDuplicatesFound, if that is what err is.
func AsDuplicatesError(err error) (*ErrDuplicatesFound, bool) {
	var duplicates *ErrDuplicatesFound
	ok := errors.As(err, &duplicates)
	return duplicates, ok
}
