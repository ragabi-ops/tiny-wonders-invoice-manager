package auth

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

// Account lockout policy (SECURITY.md 1).
const (
	maxFailedLogins = 8
	lockoutDuration = 15 * time.Minute
)

// Sentinel errors the HTTP layer maps to the error catalogue. They are
// deliberately coarse: the client must not learn why a login failed.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountLocked      = errors.New("account temporarily locked")
	ErrAccountInactive    = errors.New("account is not active")
)

// Service performs authentication use cases.
type Service struct {
	pool     *db.Pool
	users    *users.Repository
	sessions *SessionStore
	audit    *audit.Recorder
}

// NewService wires the authentication service.
func NewService(pool *db.Pool, repo *users.Repository, sessions *SessionStore, recorder *audit.Recorder) *Service {
	return &Service{pool: pool, users: repo, sessions: sessions, audit: recorder}
}

// Sessions exposes the session store to the HTTP layer for cookie handling.
func (s *Service) Sessions() *SessionStore { return s.sessions }

// LoginRequest carries everything one login attempt needs.
type LoginRequest struct {
	Email     string
	Password  string
	IP        string
	UserAgent string
	RequestID string
}

// Login verifies credentials and issues a session. Every outcome, including
// failure, is audited. The whole attempt runs in one transaction so the failure
// counter and its audit event commit together.
func (s *Service) Login(ctx context.Context, req LoginRequest) (*Issued, *users.User, error) {
	var (
		issued *Issued
		user   *users.User
	)

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, err := s.users.GetByEmail(ctx, tx, req.Email)
		if err != nil {
			if !errors.Is(err, users.ErrNotFound) {
				return err
			}
			// Spend comparable time so a missing account is not detectable, and
			// audit the attempt without an actor.
			VerifyDummy()
			return s.auditAttempt(ctx, tx, req, "", audit.OpLoginFailed, "unknown account")
		}

		if found.LockedUntil != nil && found.LockedUntil.After(time.Now()) {
			if auditErr := s.auditAttempt(ctx, tx, req, found.ID, audit.OpLoginBlocked, "account locked"); auditErr != nil {
				return auditErr
			}
			return ErrAccountLocked
		}
		if !found.Active {
			if auditErr := s.auditAttempt(ctx, tx, req, found.ID, audit.OpLoginBlocked, "account inactive"); auditErr != nil {
				return auditErr
			}
			return ErrAccountInactive
		}

		if err := VerifyPassword(req.Password, found.PasswordHash); err != nil {
			if !errors.Is(err, ErrMismatch) && !errors.Is(err, ErrInvalidHash) {
				return err
			}
			if err := s.users.RecordFailedLogin(ctx, tx, found.ID, maxFailedLogins, lockoutDuration); err != nil {
				return err
			}
			if err := s.auditAttempt(ctx, tx, req, found.ID, audit.OpLoginFailed, "wrong password"); err != nil {
				return err
			}
			return ErrInvalidCredentials
		}

		if err := s.users.RecordSuccessfulLogin(ctx, tx, found.ID); err != nil {
			return err
		}
		if issued, err = s.sessions.Create(ctx, tx, found.ID, req.IP, req.UserAgent); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: found.ID,
			ActorEmail:  found.Email,
			Operation:   audit.OpLoginSucceeded,
			EntityType:  audit.EntitySession,
			EntityID:    issued.Session.ID,
			RequestID:   req.RequestID,
			IP:          req.IP,
		}); err != nil {
			return err
		}

		// Re-read so the returned user reflects the cleared failure counter.
		user, err = s.users.GetByID(ctx, tx, found.ID)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if issued == nil {
		// The transaction committed the failure audit; the attempt still failed.
		return nil, nil, ErrInvalidCredentials
	}
	return issued, user, nil
}

// auditAttempt records a failed or blocked attempt. The email is stored so the
// activity log is readable, but never the password.
func (s *Service) auditAttempt(ctx context.Context, q users.Querier, req LoginRequest, userID, operation, reason string) error {
	return s.audit.Record(ctx, q, audit.Entry{
		ActorUserID: userID,
		ActorEmail:  users.NormalizeEmail(req.Email),
		Operation:   operation,
		EntityType:  audit.EntitySession,
		RequestID:   req.RequestID,
		Reason:      reason,
		IP:          req.IP,
	})
}

// Logout deletes the session and audits it.
func (s *Service) Logout(ctx context.Context, token string, user *users.User, requestID, ip string) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.sessions.Delete(ctx, tx, token); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: user.ID,
			ActorEmail:  user.Email,
			Operation:   audit.OpLogout,
			EntityType:  audit.EntitySession,
			RequestID:   requestID,
			IP:          ip,
		})
	})
}

// ChangePassword replaces a user's own password. Every other session of that
// user is destroyed, so a password change evicts an attacker who already had one.
func (s *Service) ChangePassword(ctx context.Context, user *users.User, currentPassword, newPassword, currentToken, requestID, ip string) error {
	if err := VerifyPassword(currentPassword, user.PasswordHash); err != nil {
		return ErrInvalidCredentials
	}
	if err := ValidatePasswordStrength(newPassword); err != nil {
		return err
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.users.UpdatePassword(ctx, tx, user.ID, hash); err != nil {
			return err
		}
		if err := s.sessions.DeleteAllForUser(ctx, tx, user.ID); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: user.ID,
			ActorEmail:  user.Email,
			Operation:   audit.OpPasswordChanged,
			EntityType:  audit.EntityUser,
			EntityID:    user.ID,
			RequestID:   requestID,
			IP:          ip,
		})
	})
}

// EnsureBootstrapOwner creates the first OWNER when the database has no users.
// It is a no-op once any user exists, so leaving the variables set cannot
// resurrect a deleted account or overwrite a password.
func (s *Service) EnsureBootstrapOwner(ctx context.Context, email, password, displayName string) (created bool, err error) {
	if email == "" || password == "" {
		return false, nil
	}
	if err := ValidatePasswordStrength(password); err != nil {
		return false, fmt.Errorf("BOOTSTRAP_OWNER_PASSWORD: %w", err)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	if displayName == "" {
		displayName = "בעלים"
	}

	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		count, err := s.users.Count(ctx, tx)
		if err != nil {
			return err
		}
		if count > 0 {
			return nil
		}

		user, err := s.users.Create(ctx, tx, users.CreateParams{
			Email:        email,
			DisplayName:  displayName,
			PasswordHash: hash,
			Roles:        []users.Role{users.RoleOwner},
			// The bootstrap password travels through the environment, so it is
			// treated as compromised and must be replaced on first login.
			MustChangePassword: true,
		})
		if err != nil {
			return err
		}
		created = true

		return s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: user.ID,
			ActorEmail:  user.Email,
			Operation:   audit.OpUserCreated,
			EntityType:  audit.EntityUser,
			EntityID:    user.ID,
			Reason:      "bootstrap owner created on first start",
			After:       map[string]any{"email": user.Email, "roles": user.Roles},
		})
	})
	return created, err
}

// SweepExpiredSessions deletes expired sessions; the API runs it periodically.
func (s *Service) SweepExpiredSessions(ctx context.Context) (int64, error) {
	return s.sessions.DeleteExpired(ctx, s.pool)
}
