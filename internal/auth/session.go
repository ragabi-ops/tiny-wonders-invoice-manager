package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Cookie names. The session cookie is HttpOnly; the CSRF cookie deliberately is
// not, because the browser must read it to echo the token back (SECURITY.md 3).
const (
	SessionCookieName = "session"
	CSRFCookieName    = "csrf_token"
	CSRFHeaderName    = "X-CSRF-Token"

	tokenBytes = 32
)

// ErrSessionInvalid covers a missing, expired or unknown session.
var ErrSessionInvalid = errors.New("session is invalid or expired")

// Session is a live login.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// Issued carries the plaintext tokens, which exist only in the response that
// creates them; the database keeps hashes only.
type Issued struct {
	Session      Session
	Token        string
	CSRFToken    string
	AbsoluteTTL  time.Duration
	IdleTimeout  time.Duration
	SecureCookie bool
}

// SessionStore persists sessions.
type SessionStore struct {
	ttl          time.Duration
	idleTimeout  time.Duration
	secureCookie bool
}

// NewSessionStore configures lifetimes and cookie security.
func NewSessionStore(ttl, idleTimeout time.Duration, secureCookie bool) *SessionStore {
	return &SessionStore{ttl: ttl, idleTimeout: idleTimeout, secureCookie: secureCookie}
}

func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Create issues a fresh session. A new token on every login prevents session
// fixation.
func (s *SessionStore) Create(ctx context.Context, q users.Querier, userID, ip, userAgent string) (*Issued, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	csrfToken, err := newToken()
	if err != nil {
		return nil, err
	}

	var ipValue any
	if ip != "" {
		ipValue = ip
	}

	issued := &Issued{
		Token:        token,
		CSRFToken:    csrfToken,
		AbsoluteTTL:  s.ttl,
		IdleTimeout:  s.idleTimeout,
		SecureCookie: s.secureCookie,
	}

	err = q.QueryRow(ctx, `
		INSERT INTO sessions (user_id, token_hash, csrf_token_hash, expires_at, ip, user_agent)
		VALUES ($1, $2, $3, now() + $4::interval, $5, $6)
		RETURNING id, user_id, created_at, last_seen_at, expires_at`,
		userID, hashToken(token), hashToken(csrfToken), s.ttl.String(), ipValue, truncate(userAgent, 512),
	).Scan(&issued.Session.ID, &issued.Session.UserID, &issued.Session.CreatedAt,
		&issued.Session.LastSeenAt, &issued.Session.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return issued, nil
}

// authenticated is the result of a successful session lookup.
type authenticated struct {
	session   Session
	userID    string
	csrfHash  []byte
	userFound bool
}

// lookup resolves a session token, enforcing both the absolute expiry and the
// idle timeout, and refreshes last_seen_at. Both limits are server-side; the
// cookie's own expiry is a convenience only (SECURITY.md 2).
func (s *SessionStore) lookup(ctx context.Context, q users.Querier, token string) (*authenticated, error) {
	if token == "" {
		return nil, ErrSessionInvalid
	}

	var a authenticated
	err := q.QueryRow(ctx, `
		UPDATE sessions
		SET last_seen_at = now()
		WHERE token_hash = $1
		  AND expires_at > now()
		  AND last_seen_at > now() - $2::interval
		RETURNING id, user_id, created_at, last_seen_at, expires_at, csrf_token_hash`,
		hashToken(token), s.idleTimeout.String(),
	).Scan(&a.session.ID, &a.session.UserID, &a.session.CreatedAt,
		&a.session.LastSeenAt, &a.session.ExpiresAt, &a.csrfHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionInvalid
		}
		return nil, fmt.Errorf("lookup session: %w", err)
	}
	a.userID = a.session.UserID
	a.userFound = true
	return &a, nil
}

// Delete removes one session, so logout immediately kills a stolen cookie.
func (s *SessionStore) Delete(ctx context.Context, q users.Querier, token string) error {
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token))
	return err
}

// DeleteAllForUser ends every session of a user, used when a password changes
// or an account is disabled.
func (s *SessionStore) DeleteAllForUser(ctx context.Context, q users.Querier, userID string) error {
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

// DeleteExpired removes sessions past their absolute expiry or idle timeout.
func (s *SessionStore) DeleteExpired(ctx context.Context, q users.Querier) (int64, error) {
	tag, err := q.Exec(ctx,
		`DELETE FROM sessions WHERE expires_at <= now() OR last_seen_at <= now() - $1::interval`,
		s.idleTimeout.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SetCookies writes the session and CSRF cookies for a new login.
func (s *SessionStore) SetCookies(w http.ResponseWriter, issued *Issued) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    issued.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
		Expires:  issued.Session.ExpiresAt,
	})
	// Readable by JavaScript on purpose: the SPA echoes it in X-CSRF-Token.
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    issued.CSRFToken,
		Path:     "/",
		HttpOnly: false,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
		Expires:  issued.Session.ExpiresAt,
	})
}

// ClearCookies expires both cookies on logout.
func (s *SessionStore) ClearCookies(w http.ResponseWriter) {
	for _, name := range []string{SessionCookieName, CSRFCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: name == SessionCookieName,
			Secure:   s.secureCookie,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
