// Package auth owns passwords, sessions, CSRF and role enforcement.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (SECURITY.md 1). They are encoded into every hash, so
// raising them later still verifies existing passwords.
const (
	argonMemory      uint32 = 64 * 1024 // 64 MiB
	argonIterations  uint32 = 3
	argonParallelism uint8  = 2
	argonSaltLength  uint32 = 16
	argonKeyLength   uint32 = 32

	// MinPasswordLength is enforced server-side; the UI mirrors it only as a hint.
	MinPasswordLength = 12
	// maxPasswordLength bounds the work an attacker can force per attempt.
	maxPasswordLength = 256
)

var (
	// ErrPasswordTooShort and ErrPasswordTooLong are validation failures.
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("password must be at most %d characters", maxPasswordLength)

	// ErrInvalidHash means a stored hash is not in the expected encoding.
	ErrInvalidHash = errors.New("password hash is malformed")
	// ErrMismatch means the password does not match the hash.
	ErrMismatch = errors.New("password does not match")
)

// ValidatePasswordStrength checks length only. Composition rules are
// deliberately absent: they push users toward predictable substitutions.
func ValidatePasswordStrength(password string) error {
	switch n := utf8.RuneCountInString(password); {
	case n < MinPasswordLength:
		return ErrPasswordTooShort
	case n > maxPasswordLength:
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword returns an encoded Argon2id hash:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
func HashPassword(password string) (string, error) {
	if err := ValidatePasswordStrength(password); err != nil {
		return "", err
	}

	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword compares a password against an encoded hash in constant time.
func VerifyPassword(password, encodedHash string) error {
	params, salt, want, err := decodeHash(encodedHash)
	if err != nil {
		return err
	}

	got := argon2.IDKey([]byte(password), salt, params.iterations, params.memory, params.parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

type argonParams struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
}

func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	var p argonParams

	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrInvalidHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.iterations, &p.parallelism); err != nil {
		return p, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return p, nil, nil, ErrInvalidHash
	}
	return p, salt, key, nil
}

// dummyHash is verified against when no user matches, so a login attempt for a
// nonexistent account costs the same as one for a real account and cannot be
// used to enumerate users (SECURITY.md 1).
var dummyHash = mustHash("enumeration-resistance-placeholder-password")

func mustHash(password string) string {
	h, err := HashPassword(password)
	if err != nil {
		panic(err)
	}
	return h
}

// VerifyDummy burns comparable CPU time when the account does not exist.
func VerifyDummy() { _ = VerifyPassword("wrong", dummyHash) }
