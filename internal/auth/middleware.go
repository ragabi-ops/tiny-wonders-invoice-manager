package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

type contextKey int

const (
	userKey contextKey = iota
	sessionKey
)

// UserFromContext returns the authenticated user, if any.
func UserFromContext(ctx context.Context) (*users.User, bool) {
	u, ok := ctx.Value(userKey).(*users.User)
	return u, ok
}

// MustUser returns the authenticated user. Handlers behind RequireAuth may call
// it; reaching it unauthenticated is a routing bug, not a runtime condition.
func MustUser(ctx context.Context) *users.User {
	u, ok := UserFromContext(ctx)
	if !ok {
		panic("auth: no user in context; handler is not behind RequireAuth")
	}
	return u
}

// SessionFromContext returns the current session, if any.
func SessionFromContext(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(sessionKey).(*Session)
	return s, ok
}

// Middleware enforces authentication, CSRF and roles.
type Middleware struct {
	sessions *SessionStore
	users    *users.Repository
	db       users.Querier
}

// NewMiddleware wires the enforcement layer.
func NewMiddleware(sessions *SessionStore, repo *users.Repository, db users.Querier) *Middleware {
	return &Middleware{sessions: sessions, users: repo, db: db}
}

// RequireAuth resolves the session cookie into a user, rejects unsafe methods
// without a matching CSRF token, and refuses disabled accounts.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil {
			httpx.WriteError(w, r, httpx.ErrUnauthorized)
			return
		}

		ctx := r.Context()
		auth, err := m.sessions.lookup(ctx, m.db, cookie.Value)
		if err != nil {
			if errors.Is(err, ErrSessionInvalid) {
				// Clear the stale cookie so the browser stops sending it.
				m.sessions.ClearCookies(w)
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
			return
		}

		if isUnsafeMethod(r.Method) {
			if !m.csrfOK(r, auth.csrfHash) {
				httpx.WriteError(w, r, httpx.ErrCSRF)
				return
			}
		}

		user, err := m.users.GetByID(ctx, m.db, auth.userID)
		if err != nil {
			if errors.Is(err, users.ErrNotFound) {
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
			return
		}
		if !user.Active {
			// Deactivation takes effect immediately, not at session expiry.
			_ = m.sessions.DeleteAllForUser(ctx, m.db, user.ID)
			m.sessions.ClearCookies(w)
			httpx.WriteError(w, r, httpx.ErrForbidden.WithMessage("החשבון אינו פעיל."))
			return
		}

		ctx = context.WithValue(ctx, userKey, user)
		ctx = context.WithValue(ctx, sessionKey, &auth.session)
		ctx = httpx.ContextWithLogger(ctx, httpx.Logger(r).With("user_id", user.ID))

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole allows a request only if the user holds one of roles. Deny is the
// default: every non-public route carries an explicit requirement
// (SECURITY.md 5).
func (m *Middleware) RequireRole(roles ...users.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok {
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			// OWNER is a superset of every other role by definition (plan.md 6).
			if user.HasRole(users.RoleOwner) || user.HasAnyRole(roles...) {
				next.ServeHTTP(w, r)
				return
			}
			httpx.WriteError(w, r, httpx.ErrForbidden)
		})
	}
}

// csrfOK compares the X-CSRF-Token header against the session's stored hash in
// constant time. The header, not the cookie, is authoritative: an attacker on
// another origin can cause the cookie to be sent but cannot read it to set the
// header.
func (m *Middleware) csrfOK(r *http.Request, storedHash []byte) bool {
	token := r.Header.Get(CSRFHeaderName)
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare(hashToken(token), storedHash) == 1
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}
