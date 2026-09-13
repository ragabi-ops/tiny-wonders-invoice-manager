package users

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ragabix/tiny-wonders-invoice-manager/internal/httpx"
)

// Handlers expose user administration. Every route is OWNER-only; the router
// applies that requirement (SECURITY.md 5).
type Handlers struct {
	svc *Service
	// currentUser resolves the acting user from a request. It is injected so
	// this package does not import internal/auth, which imports this one.
	currentUser func(*http.Request) *User
}

// NewHandlers builds the user-administration HTTP layer.
func NewHandlers(svc *Service, currentUser func(*http.Request) *User) *Handlers {
	return &Handlers{svc: svc, currentUser: currentUser}
}

// Routes mounts user administration under /users.
func (h *Handlers) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/roles", h.roles)
	r.Get("/{id}", h.get)
	r.Put("/{id}/roles", h.setRoles)
	r.Put("/{id}/active", h.setActive)
	r.Post("/{id}/reset-password", h.resetPassword)
}

type roleOption struct {
	Role   string `json:"role"`
	Hebrew string `json:"hebrew"`
}

// roles lists the closed set of roles, so the UI never hard-codes them.
func (h *Handlers) roles(w http.ResponseWriter, r *http.Request) {
	options := make([]roleOption, 0, len(AllRoles))
	for _, role := range AllRoles {
		options = append(options, roleOption{Role: string(role), Hebrew: role.HebrewName()})
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"roles": options})
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrInternal.WithCause(err))
		return
	}
	if list == nil {
		list = []*User{}
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"users": list})
}

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) {
	user, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, user)
}

type createUserRequest struct {
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Password    string   `json:"password"`
	Roles       []string `json:"roles"`
}

func (h *Handlers) create(w http.ResponseWriter, r *http.Request) {
	var body createUserRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	fields := map[string]string{}
	if strings.TrimSpace(body.Email) == "" || !strings.Contains(body.Email, "@") {
		fields["email"] = "יש להזין כתובת דוא״ל תקינה"
	}
	if strings.TrimSpace(body.DisplayName) == "" {
		fields["display_name"] = "יש להזין שם"
	}
	if len(body.Roles) == 0 {
		fields["roles"] = "יש לבחור לפחות תפקיד אחד"
	}
	if len(fields) > 0 {
		httpx.WriteError(w, r, httpx.ValidationError(fields))
		return
	}

	roles, err := parseRoles(body.Roles)
	if err != nil {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"roles": "תפקיד לא מוכר"}))
		return
	}

	actor := h.currentUser(r)
	created, err := h.svc.Create(r.Context(), actor, NewUserParams{
		Email:       body.Email,
		DisplayName: body.DisplayName,
		Password:    body.Password,
		Roles:       roles,
		RequestID:   httpx.RequestID(r.Context()),
	})
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, created)
}

type setRolesRequest struct {
	Roles []string `json:"roles"`
}

func (h *Handlers) setRoles(w http.ResponseWriter, r *http.Request) {
	var body setRolesRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	roles, err := parseRoles(body.Roles)
	if err != nil || len(roles) == 0 {
		httpx.WriteError(w, r, httpx.ValidationError(map[string]string{"roles": "יש לבחור לפחות תפקיד אחד תקין"}))
		return
	}

	updated, err := h.svc.SetRoles(r.Context(), h.currentUser(r), chi.URLParam(r, "id"), roles, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, updated)
}

type setActiveRequest struct {
	Active bool   `json:"active"`
	Reason string `json:"reason"`
}

func (h *Handlers) setActive(w http.ResponseWriter, r *http.Request) {
	var body setActiveRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	updated, err := h.svc.SetActive(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body.Active, body.Reason, httpx.RequestID(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, updated)
}

type resetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

func (h *Handlers) resetPassword(w http.ResponseWriter, r *http.Request) {
	var body resetPasswordRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	if err := h.svc.ResetPassword(r.Context(), h.currentUser(r), chi.URLParam(r, "id"),
		body.NewPassword, httpx.RequestID(r.Context())); err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusNoContent, nil)
}

func parseRoles(raw []string) ([]Role, error) {
	roles := make([]Role, 0, len(raw))
	for _, value := range raw {
		role := Role(strings.ToUpper(strings.TrimSpace(value)))
		if !role.Valid() {
			return nil, ErrInvalidRole
		}
		if !containsRole(roles, role) {
			roles = append(roles, role)
		}
	}
	return roles, nil
}

// translate maps domain errors onto the API error catalogue.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.ErrNotFound.WithMessage("המשתמש לא נמצא.")
	case errors.Is(err, ErrEmailTaken):
		return httpx.ErrConflict.WithMessage("כתובת הדוא״ל כבר רשומה במערכת.")
	case errors.Is(err, ErrInvalidRole):
		return httpx.ValidationError(map[string]string{"roles": "תפקיד לא מוכר"})
	case errors.Is(err, ErrLastOwnerGone):
		return httpx.ErrConflict.WithMessage("לא ניתן להסיר את בעל ההרשאות האחרון במערכת.")
	case errors.Is(err, ErrWeakPassword):
		// The hasher's wrapper carries the Hebrew rule after the sentinel.
		message := err.Error()
		if _, detail, ok := strings.Cut(message, ": "); ok {
			message = detail
		}
		return httpx.ValidationError(map[string]string{"password": message})
	default:
		return httpx.ErrInternal.WithCause(err)
	}
}
