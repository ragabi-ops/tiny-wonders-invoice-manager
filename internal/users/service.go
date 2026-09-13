package users

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/audit"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
)

// PasswordHasher hashes a plaintext password. It is injected rather than
// imported so this package does not depend on internal/auth, which depends on
// this one.
type PasswordHasher func(password string) (string, error)

// SessionRevoker ends every session of a user. Injected for the same reason.
type SessionRevoker func(ctx context.Context, q Querier, userID string) error

// Service performs user-administration use cases. Every mutation is audited in
// the same transaction as the change itself.
type Service struct {
	pool   *db.Pool
	repo   *Repository
	audit  *audit.Recorder
	hash   PasswordHasher
	revoke SessionRevoker
}

// NewService wires user administration.
func NewService(pool *db.Pool, repo *Repository, recorder *audit.Recorder, hash PasswordHasher, revoke SessionRevoker) *Service {
	return &Service{pool: pool, repo: repo, audit: recorder, hash: hash, revoke: revoke}
}

// List returns every user.
func (s *Service) List(ctx context.Context) ([]*User, error) {
	return s.repo.List(ctx, s.pool)
}

// Get returns one user.
func (s *Service) Get(ctx context.Context, id string) (*User, error) {
	return s.repo.GetByID(ctx, s.pool, id)
}

// NewUserParams describes a user to create.
type NewUserParams struct {
	Email       string
	DisplayName string
	Password    string
	Roles       []Role
	RequestID   string
}

// Create adds a user. The initial password is chosen by an administrator, so
// the account is forced to change it at first login.
func (s *Service) Create(ctx context.Context, actor *User, p NewUserParams) (*User, error) {
	if len(p.Roles) == 0 {
		return nil, fmt.Errorf("%w: at least one role is required", ErrInvalidRole)
	}

	hash, err := s.hash(p.Password)
	if err != nil {
		return nil, err
	}

	var created *User
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		user, err := s.repo.Create(ctx, tx, CreateParams{
			Email:              p.Email,
			DisplayName:        p.DisplayName,
			PasswordHash:       hash,
			Roles:              p.Roles,
			MustChangePassword: true,
		})
		if err != nil {
			return err
		}
		created = user

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpUserCreated,
			EntityType:  audit.EntityUser,
			EntityID:    user.ID,
			RequestID:   p.RequestID,
			After:       map[string]any{"email": user.Email, "display_name": user.DisplayName, "roles": user.Roles},
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// SetRoles replaces a user's roles, refusing to remove the last active OWNER so
// the system cannot be locked out of its own administration.
func (s *Service) SetRoles(ctx context.Context, actor *User, userID string, roles []Role, requestID string) (*User, error) {
	var updated *User

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.repo.GetByID(ctx, tx, userID)
		if err != nil {
			return err
		}

		losingOwner := before.HasRole(RoleOwner) && !containsRole(roles, RoleOwner)
		if losingOwner {
			owners, err := s.repo.CountActiveOwners(ctx, tx)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return ErrLastOwnerGone
			}
		}

		if err := s.repo.SetRoles(ctx, tx, userID, roles); err != nil {
			return err
		}
		if updated, err = s.repo.GetByID(ctx, tx, userID); err != nil {
			return err
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpUserRolesChanged,
			EntityType:  audit.EntityUser,
			EntityID:    userID,
			RequestID:   requestID,
			Before:      map[string]any{"roles": before.Roles},
			After:       map[string]any{"roles": updated.Roles},
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// SetActive enables or disables an account. Deactivating also ends every
// session that account holds, so access stops immediately.
func (s *Service) SetActive(ctx context.Context, actor *User, userID string, active bool, reason, requestID string) (*User, error) {
	if !active && actor.ID == userID {
		return nil, errors.New("a user cannot deactivate their own account")
	}

	var updated *User
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := s.repo.GetByID(ctx, tx, userID)
		if err != nil {
			return err
		}

		if !active && before.HasRole(RoleOwner) {
			owners, err := s.repo.CountActiveOwners(ctx, tx)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return ErrLastOwnerGone
			}
		}

		if err := s.repo.SetActive(ctx, tx, userID, active); err != nil {
			return err
		}
		if !active {
			if err := s.revoke(ctx, tx, userID); err != nil {
				return err
			}
		}
		if updated, err = s.repo.GetByID(ctx, tx, userID); err != nil {
			return err
		}

		operation := audit.OpUserActivated
		if !active {
			operation = audit.OpUserDeactivated
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   operation,
			EntityType:  audit.EntityUser,
			EntityID:    userID,
			RequestID:   requestID,
			Reason:      reason,
			Before:      map[string]any{"active": before.Active},
			After:       map[string]any{"active": updated.Active},
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// ResetPassword sets a new password for another user and ends their sessions.
func (s *Service) ResetPassword(ctx context.Context, actor *User, userID, newPassword, requestID string) error {
	hash, err := s.hash(newPassword)
	if err != nil {
		return err
	}

	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.repo.UpdatePassword(ctx, tx, userID, hash); err != nil {
			return err
		}
		// The new password came from someone else, so it must be changed at
		// first login.
		if _, err := tx.Exec(ctx, `UPDATE users SET must_change_password = true WHERE id = $1`, userID); err != nil {
			return err
		}
		if err := s.revoke(ctx, tx, userID); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: actor.ID,
			ActorEmail:  actor.Email,
			Operation:   audit.OpPasswordChanged,
			EntityType:  audit.EntityUser,
			EntityID:    userID,
			RequestID:   requestID,
			Reason:      "password reset by administrator",
		})
	})
}

func containsRole(roles []Role, want Role) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}
