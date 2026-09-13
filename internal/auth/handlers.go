package auth

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
	"github.com/ragabix/tiny-wonders-invoice-manager/internal/users"
)

// Handlers expose the authentication endpoints. They contain no business
// logic: they decode, delegate, and encode.
type Handlers struct {
	svc *Service

	// ipLimiter throttles login attempts per source address and accountLimiter
	// per email, so neither a single host nor a distributed attempt against one
	// account can brute-force a password (SECURITY.md 1).
	ipLimiter      *httpx.RateLimiter
	accountLimiter *httpx.RateLimiter
}

// NewHandlers builds the auth HTTP layer.
func NewHandlers(svc *Service) *Handlers {
	return &Handlers{
		svc:            svc,
		ipLimiter:      httpx.NewRateLimiter(20, 10),
		accountLimiter: httpx.NewRateLimiter(10, 5),
	}
}

// Routes mounts the public authentication endpoints.
func (h *Handlers) Routes(r chi.Router) {
	r.Post("/login", h.login)
}

// AuthenticatedRoutes mounts endpoints that require a session.
func (h *Handlers) AuthenticatedRoutes(r chi.Router) {
	r.Get("/me", h.me)
	r.Post("/logout", h.logout)
	r.Post("/change-password", h.changePassword)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type meResponse struct {
	User  *users.User `json:"user"`
	Roles []string    `json:"roles"`
}

func (h *Handlers) login(w http.ResponseWriter, r *http.Request) {
	var body loginRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	email := users.NormalizeEmail(body.Email)
	if email == "" || body.Password == "" {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{
			"email":    "יש להזין דוא״ל וסיסמה",
			"password": "יש להזין דוא״ל וסיסמה",
		}))
		return
	}

	ip := httpx.ClientIP(r)
	if !h.ipLimiter.Allow(ip) || !h.accountLimiter.Allow(email) {
		httpx.WriteError(w, r, httpx.ErrRateLimited)
		return
	}

	issued, user, err := h.svc.Login(r.Context(), LoginRequest{
		Email:     email,
		Password:  body.Password,
		IP:        ip,
		UserAgent: r.UserAgent(),
		RequestID: httpx.RequestID(r.Context()),
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrAccountLocked):
			httpx.WriteError(w, r, httpx.ErrAccountLocked)
		case errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrAccountInactive):
			// One message for every failure mode: no account enumeration.
			httpx.WriteError(w, r, httpx.ErrInvalidLogin.WithCause(err))
		default:
			httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		}
		return
	}

	// A legitimate user should not stay throttled by their own earlier typos.
	h.ipLimiter.Reset(ip)
	h.accountLimiter.Reset(email)

	h.svc.Sessions().SetCookies(w, issued)
	httpx.WriteJSON(w, r, http.StatusOK, meResponse{User: user, Roles: roleStrings(user.Roles)})
}

func (h *Handlers) me(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	httpx.WriteJSON(w, r, http.StatusOK, meResponse{User: user, Roles: roleStrings(user.Roles)})
}

func (h *Handlers) logout(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	token := ""
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		token = cookie.Value
	}

	if err := h.svc.Logout(r.Context(), token, user, httpx.RequestID(r.Context()), httpx.ClientIP(r)); err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}

	h.svc.Sessions().ClearCookies(w)
	httpx.WriteJSON(w, r, http.StatusNoContent, nil)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handlers) changePassword(w http.ResponseWriter, r *http.Request) {
	var body changePasswordRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	user := MustUser(r.Context())

	token := ""
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		token = cookie.Value
	}

	err := h.svc.ChangePassword(r.Context(), user,
		body.CurrentPassword, body.NewPassword, token,
		httpx.RequestID(r.Context()), httpx.ClientIP(r))
	switch {
	case err == nil:
	case errors.Is(err, ErrInvalidCredentials):
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{
			"current_password": "הסיסמה הנוכחית שגויה",
		}))
		return
	case errors.Is(err, ErrPasswordTooShort), errors.Is(err, ErrPasswordTooLong):
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{
			"new_password": passwordRuleHebrew(),
		}))
		return
	default:
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}

	// Every session was destroyed, this one included: the user logs in again.
	h.svc.Sessions().ClearCookies(w)
	httpx.WriteJSON(w, r, http.StatusNoContent, nil)
}

// passwordRuleHebrew is the single Hebrew wording of the password rule, used
// wherever a password is rejected.
func passwordRuleHebrew() string {
	return "הסיסמה חייבת להכיל לפחות " + strconv.Itoa(MinPasswordLength) + " תווים"
}

func roleStrings(roles []users.Role) []string {
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		out = append(out, string(role))
	}
	return out
}
