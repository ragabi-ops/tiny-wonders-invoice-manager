// Package users owns user records and their roles. It knows nothing about HTTP
// or about how passwords are hashed; it stores the hash it is given.
package users

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/db"
)

// Role is an authorization role (plan.md 6).
type Role string

const (
	RoleOwner      Role = "OWNER"
	RoleOperator   Role = "OPERATOR"
	RoleAccountant Role = "ACCOUNTANT"
	RoleReadOnly   Role = "READ_ONLY"
)

// AllRoles is the closed set of valid roles, mirroring the database CHECK.
var AllRoles = []Role{RoleOwner, RoleOperator, RoleAccountant, RoleReadOnly}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return slices.Contains(AllRoles, r) }

// HebrewName is the role's display name in the UI.
func (r Role) HebrewName() string {
	switch r {
	case RoleOwner:
		return "בעלים"
	case RoleOperator:
		return "מפעיל"
	case RoleAccountant:
		return "רואה חשבון"
	case RoleReadOnly:
		return "צפייה בלבד"
	default:
		return string(r)
	}
}

// Sentinel errors callers translate into API errors.
var (
	ErrNotFound      = errors.New("user not found")
	ErrEmailTaken    = errors.New("email already registered")
	ErrInvalidRole   = errors.New("invalid role")
	ErrLastOwnerGone = errors.New("the last OWNER cannot be removed or deactivated")

	// ErrWeakPassword carries a Hebrew message because it is shown to the user.
	// The password rule itself lives in internal/auth; the hasher injected into
	// Service translates its failures into this error.
	ErrWeakPassword = errors.New("הסיסמה קצרה מדי")
)

// User is an account. PasswordHash never leaves the server.
type User struct {
	ID                 string     `json:"id"`
	Email              string     `json:"email"`
	DisplayName        string     `json:"display_name"`
	Active             bool       `json:"active"`
	MustChangePassword bool       `json:"must_change_password"`
	Roles              []Role     `json:"roles"`
	LastLoginAt        *time.Time `json:"last_login_at"`
	CreatedAt          time.Time  `json:"created_at"`

	PasswordHash     string     `json:"-"`
	FailedLoginCount int        `json:"-"`
	LockedUntil      *time.Time `json:"-"`
}

// HasRole reports whether the user holds role.
func (u *User) HasRole(role Role) bool { return slices.Contains(u.Roles, role) }

// HasAnyRole reports whether the user holds at least one of roles.
func (u *User) HasAnyRole(roles ...Role) bool {
	for _, role := range roles {
		if u.HasRole(role) {
			return true
		}
	}
	return false
}

// Querier is the shared read/write interface satisfied by both the pool and a
// transaction.
type Querier = db.Querier

// NormalizeEmail lower-cases and trims an email for comparison and storage.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Repository reads and writes user records.
type Repository struct{}

// NewRepository returns a stateless repository.
func NewRepository() *Repository { return &Repository{} }

const userColumns = `
	u.id, u.email, u.display_name, u.password_hash, u.active,
	u.must_change_password, u.failed_login_count, u.locked_until,
	u.last_login_at, u.created_at,
	COALESCE(ARRAY(SELECT r.role FROM user_roles r WHERE r.user_id = u.id ORDER BY r.role), '{}')`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	var roles []string
	if err := row.Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.Active,
		&u.MustChangePassword, &u.FailedLoginCount, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &roles,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Roles = make([]Role, 0, len(roles))
	for _, r := range roles {
		u.Roles = append(u.Roles, Role(r))
	}
	return &u, nil
}

// GetByEmail looks a user up case-insensitively.
func (repo *Repository) GetByEmail(ctx context.Context, q Querier, email string) (*User, error) {
	return scanUser(q.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users u WHERE lower(u.email) = lower($1)`,
		NormalizeEmail(email)))
}

// GetByID looks a user up by identifier.
func (repo *Repository) GetByID(ctx context.Context, q Querier, id string) (*User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id = $1`, id))
}

// List returns all users ordered by display name.
func (repo *Repository) List(ctx context.Context, q Querier) ([]*User, error) {
	rows, err := q.Query(ctx, `SELECT `+userColumns+` FROM users u ORDER BY u.display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, rows.Err()
}

// CreateParams describes a new user.
type CreateParams struct {
	Email              string
	DisplayName        string
	PasswordHash       string
	Roles              []Role
	MustChangePassword bool
}

// Create inserts a user with their roles. The caller supplies an already
// hashed password.
func (repo *Repository) Create(ctx context.Context, q Querier, p CreateParams) (*User, error) {
	for _, role := range p.Roles {
		if !role.Valid() {
			return nil, fmt.Errorf("%w: %q", ErrInvalidRole, role)
		}
	}

	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash, must_change_password)
		VALUES ($1, $2, $3, $4)
		RETURNING id`,
		NormalizeEmail(p.Email), strings.TrimSpace(p.DisplayName), p.PasswordHash, p.MustChangePassword,
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err, "users_email_lower_key") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	if err := repo.SetRoles(ctx, q, id, p.Roles); err != nil {
		return nil, err
	}
	return repo.GetByID(ctx, q, id)
}

// SetRoles replaces a user's roles.
func (repo *Repository) SetRoles(ctx context.Context, q Querier, userID string, roles []Role) error {
	for _, role := range roles {
		if !role.Valid() {
			return fmt.Errorf("%w: %q", ErrInvalidRole, role)
		}
	}
	if _, err := q.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, role := range roles {
		if _, err := q.Exec(ctx,
			`INSERT INTO user_roles (user_id, role) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			userID, string(role)); err != nil {
			return err
		}
	}
	return nil
}

// SetActive enables or disables an account.
func (repo *Repository) SetActive(ctx context.Context, q Querier, userID string, active bool) error {
	tag, err := q.Exec(ctx, `UPDATE users SET active = $2 WHERE id = $1`, userID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePassword stores a new hash and clears the forced-change flag.
func (repo *Repository) UpdatePassword(ctx context.Context, q Querier, userID, passwordHash string) error {
	tag, err := q.Exec(ctx, `
		UPDATE users
		SET password_hash = $2, must_change_password = false,
		    failed_login_count = 0, locked_until = NULL
		WHERE id = $1`, userID, passwordHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordSuccessfulLogin stamps the login time and clears the failure counter.
func (repo *Repository) RecordSuccessfulLogin(ctx context.Context, q Querier, userID string) error {
	_, err := q.Exec(ctx, `
		UPDATE users
		SET last_login_at = now(), failed_login_count = 0, locked_until = NULL
		WHERE id = $1`, userID)
	return err
}

// RecordFailedLogin increments the failure counter and locks the account once
// maxAttempts is reached. Locking is per account; the IP is limited separately.
func (repo *Repository) RecordFailedLogin(ctx context.Context, q Querier, userID string, maxAttempts int, lockFor time.Duration) error {
	_, err := q.Exec(ctx, `
		UPDATE users
		SET failed_login_count = failed_login_count + 1,
		    locked_until = CASE
		        WHEN failed_login_count + 1 >= $2 THEN now() + $3::interval
		        ELSE locked_until
		    END
		WHERE id = $1`, userID, maxAttempts, lockFor.String())
	return err
}

// CountActiveOwners counts enabled accounts holding OWNER, so the system can
// refuse to lock itself out.
func (repo *Repository) CountActiveOwners(ctx context.Context, q Querier) (int, error) {
	var count int
	err := q.QueryRow(ctx, `
		SELECT count(*)
		FROM users u
		JOIN user_roles r ON r.user_id = u.id
		WHERE r.role = 'OWNER' AND u.active`).Scan(&count)
	return count, err
}

// Count returns the total number of users, used to decide bootstrap.
func (repo *Repository) Count(ctx context.Context, q Querier) (int, error) {
	var count int
	err := q.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count)
	return count, err
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}
