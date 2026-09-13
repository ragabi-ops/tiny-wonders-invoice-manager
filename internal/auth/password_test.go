package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	password := "a-perfectly-fine-passphrase"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("hash has unexpected encoding: %q", hash)
	}
	if strings.Contains(hash, password) {
		t.Fatal("the hash must not contain the password")
	}

	if err := VerifyPassword(password, hash); err != nil {
		t.Fatalf("VerifyPassword rejected the correct password: %v", err)
	}
	if err := VerifyPassword(password+"x", hash); !errors.Is(err, ErrMismatch) {
		t.Fatalf("VerifyPassword accepted a wrong password, err = %v", err)
	}
}

func TestHashesAreSaltedPerPassword(t *testing.T) {
	password := "the-same-password-twice"

	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	// Equal hashes would mean a shared salt, which makes a stolen table
	// answerable with one precomputed dictionary.
	if first == second {
		t.Fatal("two hashes of the same password are identical: the salt is not random")
	}
	for _, hash := range []string{first, second} {
		if err := VerifyPassword(password, hash); err != nil {
			t.Fatalf("VerifyPassword failed on a valid hash: %v", err)
		}
	}
}

func TestPasswordLengthIsEnforced(t *testing.T) {
	if _, err := HashPassword("short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("a short password should be refused, err = %v", err)
	}
	if _, err := HashPassword(strings.Repeat("x", 300)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("an overlong password should be refused, err = %v", err)
	}

	// Exactly at the minimum is valid.
	if _, err := HashPassword(strings.Repeat("x", MinPasswordLength)); err != nil {
		t.Fatalf("a password of exactly the minimum length should be accepted: %v", err)
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	malformed := []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=65536,t=3,p=2$onlyonepart",
		"$bcrypt$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=13$m=65536,t=3,p=2$c2FsdA$aGFzaA", // wrong version
	}
	for _, hash := range malformed {
		if err := VerifyPassword("whatever", hash); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("VerifyPassword(%q) should report a malformed hash, got %v", hash, err)
		}
	}
}

func TestVerifyDummyDoesNotPanic(t *testing.T) {
	// Called on the unknown-account path of every login, so it must be safe.
	VerifyDummy()
}
