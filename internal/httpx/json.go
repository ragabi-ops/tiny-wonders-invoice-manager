package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// MaxBodyBytes caps a JSON request body. Uploads use their own larger limit.
const MaxBodyBytes int64 = 1 << 20 // 1 MiB

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil || status == http.StatusNoContent {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent; all that is left is a log line.
		Logger(r).Error("encode response", "error", err)
	}
}

type errorEnvelope struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Details   map[string]any `json:"details,omitempty"`
		RequestID string         `json:"request_id,omitempty"`
	} `json:"error"`
}

// WriteError renders err in the single error envelope and logs the internal
// cause. Client-visible text is Hebrew; the cause never crosses the wire.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := AsError(err)

	log := Logger(r)
	attrs := []any{"code", apiErr.Code, "status", apiErr.Status}
	if cause := apiErr.Unwrap(); cause != nil {
		attrs = append(attrs, "error", cause.Error())
	}
	if apiErr.Status >= http.StatusInternalServerError {
		log.Error("request failed", attrs...)
	} else {
		log.Warn("request rejected", attrs...)
	}

	var env errorEnvelope
	env.Error.Code = apiErr.Code
	env.Error.Message = apiErr.Message
	env.Error.Details = apiErr.Details
	env.Error.RequestID = RequestID(r.Context())

	WriteJSON(w, r, apiErr.Status, env)
}

// DecodeJSON reads exactly one JSON object into dst. Unknown fields are
// rejected so a client cannot silently send data the server ignores, and the
// body is size-limited before parsing.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mediaType, _, _ := strings.Cut(ct, ";"); strings.TrimSpace(mediaType) != "application/json" {
			return ErrBadRequest.WithMessage("סוג התוכן של הבקשה אינו נתמך.")
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError

		switch {
		case errors.As(err, &maxBytesErr):
			return ErrPayloadTooBig.WithCause(err)
		case errors.Is(err, io.EOF):
			return ErrBadRequest.WithMessage("גוף הבקשה ריק.").WithCause(err)
		case errors.As(err, &syntaxErr):
			return ErrBadRequest.WithMessage("מבנה הבקשה אינו תקין.").WithCause(err)
		case errors.As(err, &typeErr):
			return ErrValidation.WithDetails(map[string]any{typeErr.Field: "ערך מסוג שגוי"}).WithCause(err)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
			return ErrBadRequest.
				WithMessage("הבקשה כוללת שדה שאינו מוכר.").
				WithDetails(map[string]any{"field": field}).
				WithCause(err)
		default:
			return ErrBadRequest.WithCause(err)
		}
	}

	// Refuse trailing content: two concatenated objects must not be accepted as
	// one request.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrBadRequest.WithMessage("הבקשה חייבת להכיל אובייקט JSON אחד בלבד.")
	}
	return nil
}

// ValidationError builds a field-level validation failure.
// Keys are English field names; values are Hebrew messages for the user.
func ValidationError(fields map[string]string) *Error {
	details := make(map[string]any, len(fields))
	for k, v := range fields {
		details[k] = v
	}
	return ErrValidation.WithDetails(details)
}

// noopLogger is used when a request carries no logger, so helpers never panic.
var noopLogger = slog.New(slog.DiscardHandler)
