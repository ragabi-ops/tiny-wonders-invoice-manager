// Package httpx holds transport-level concerns shared by every domain package:
// request IDs, JSON encoding, the error envelope, middleware and rate limiting.
// It contains no business logic and imports no domain package.
package httpx

import (
	"errors"
	"fmt"
	"net/http"
)

// Error is an API error. Code is a stable English machine identifier; Message
// is Hebrew and is shown to the user as-is (ARCHITECTURE.md, Errors).
type Error struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`

	// cause is logged but never serialized to the client.
	cause error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.cause)
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.cause }

// WithCause attaches an internal error for logging without exposing it.
func (e *Error) WithCause(err error) *Error {
	clone := *e
	clone.cause = err
	return &clone
}

// WithDetails attaches structured details, such as per-field validation errors.
func (e *Error) WithDetails(details map[string]any) *Error {
	clone := *e
	clone.Details = details
	return &clone
}

// WithMessage overrides the Hebrew user-facing message.
func (e *Error) WithMessage(msg string) *Error {
	clone := *e
	clone.Message = msg
	return &clone
}

// newError builds a reusable error template.
func newError(status int, code, hebrewMessage string) *Error {
	return &Error{Status: status, Code: code, Message: hebrewMessage}
}

// Error catalogue. Every message is Hebrew because it reaches the user.
var (
	ErrBadRequest    = newError(http.StatusBadRequest, "BAD_REQUEST", "הבקשה אינה תקינה.")
	ErrValidation    = newError(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "חלק מהשדות אינם תקינים.")
	ErrUnauthorized  = newError(http.StatusUnauthorized, "UNAUTHORIZED", "יש להתחבר כדי להמשיך.")
	ErrInvalidLogin  = newError(http.StatusUnauthorized, "INVALID_CREDENTIALS", "פרטי ההתחברות שגויים.")
	ErrAccountLocked = newError(http.StatusTooManyRequests, "ACCOUNT_LOCKED", "החשבון ננעל זמנית בעקבות ניסיונות התחברות כושלים. נסו שוב מאוחר יותר.")
	ErrForbidden     = newError(http.StatusForbidden, "FORBIDDEN", "אין לך הרשאה לבצע פעולה זו.")
	ErrCSRF          = newError(http.StatusForbidden, "CSRF_TOKEN_INVALID", "פג תוקף הבקשה. רעננו את הדף ונסו שוב.")
	ErrNotFound      = newError(http.StatusNotFound, "NOT_FOUND", "הפריט המבוקש לא נמצא.")
	ErrConflict      = newError(http.StatusConflict, "CONFLICT", "הפעולה מתנגשת עם נתונים קיימים.")
	ErrPayloadTooBig = newError(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "הקובץ או הבקשה גדולים מדי.")
	ErrRateLimited   = newError(http.StatusTooManyRequests, "RATE_LIMITED", "יותר מדי בקשות. נסו שוב בעוד רגע.")
	ErrInternal      = newError(http.StatusInternalServerError, "INTERNAL_ERROR", "אירעה שגיאה בשרת. נסו שוב, ואם הבעיה נמשכת פנו לתמיכה עם מזהה הבקשה.")

	// Domain guarantees that must never be weakened for UX convenience
	// (plan.md 22.5).
	ErrImmutableDocument = newError(http.StatusConflict, "DOCUMENT_IMMUTABLE", "מסמך שהופק אינו ניתן לעריכה או למחיקה.")
	ErrComplianceBlocked = newError(http.StatusForbidden, "COMPLIANCE_BLOCKED", "הפעולה חסומה עד להשלמת בדיקת ההתאמה הרגולטורית.")
)

// AsError extracts an *Error, wrapping anything unrecognized as ErrInternal so
// an unexpected failure never leaks its text to the client.
func AsError(err error) *Error {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return ErrInternal.WithCause(err)
}
