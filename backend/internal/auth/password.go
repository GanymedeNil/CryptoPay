// Package auth holds the credential primitives of the gateway: admin password
// hashing and policy, API key / session token / webhook secret generation,
// token hashing for storage, and webhook payload signatures.
//
// It has no HTTP or database dependencies. Plaintext secrets produced here are
// shown to the user once; only HashToken digests (API keys, session tokens) are
// stored, while webhook secrets are stored encrypted by package secret because
// the gateway needs them back to sign payloads.
package auth

import (
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	// DefaultBcryptCost is the bcrypt work factor used for admin passwords.
	DefaultBcryptCost = 12
	// MinPasswordLength is the minimum number of characters (runes) of a password.
	MinPasswordLength = 8
	// MaxPasswordBytes is bcrypt's input limit; longer passwords are rejected
	// instead of being silently truncated.
	MaxPasswordBytes = 72
)

var (
	// ErrWeakPassword is returned (wrapped, with a human readable reason) by
	// ValidatePassword when a password does not satisfy the policy.
	ErrWeakPassword = errors.New("auth: password does not meet the policy")
	// ErrPasswordMismatch is returned by Hasher.Verify / VerifyPassword when the
	// password does not match the hash (or the hash is malformed).
	ErrPasswordMismatch = errors.New("auth: password does not match")
)

// ValidatePassword checks the password policy: at least MinPasswordLength
// characters and at most MaxPasswordBytes bytes (bcrypt limit). The returned
// error wraps ErrWeakPassword and its message is safe to show to the user.
func ValidatePassword(password string) error {
	if n := utf8.RuneCountInString(password); n < MinPasswordLength {
		return fmt.Errorf("%w: must be at least %d characters", ErrWeakPassword, MinPasswordLength)
	}
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("%w: must be at most %d bytes", ErrWeakPassword, MaxPasswordBytes)
	}
	return nil
}

// Hasher hashes and verifies passwords with bcrypt. The zero value uses
// DefaultBcryptCost; tests set Cost to bcrypt.MinCost (4) to stay fast.
type Hasher struct {
	// Cost is the bcrypt work factor; 0 means DefaultBcryptCost.
	Cost int
}

func (h Hasher) cost() int {
	if h.Cost == 0 {
		return DefaultBcryptCost
	}
	return h.Cost
}

// Hash returns the bcrypt hash of password. It does not apply the password
// policy (call ValidatePassword first) but fails on passwords longer than
// MaxPasswordBytes or on an invalid cost.
func (h Hasher) Hash(password string) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", fmt.Errorf("auth: hash password: %w", bcrypt.ErrPasswordTooLong)
	}
	cost := h.cost()
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return "", fmt.Errorf("auth: hash password: invalid bcrypt cost %d", cost)
	}
	b, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(b), nil
}

// Verify returns nil when password matches hash and ErrPasswordMismatch
// otherwise (including a malformed hash). It runs in time proportional to the
// cost stored in the hash, not to h.Cost.
func (h Hasher) Verify(hash, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ErrPasswordMismatch
	}
	return nil
}

// Check reports whether password matches hash.
func (h Hasher) Check(hash, password string) bool { return h.Verify(hash, password) == nil }

// NeedsRehash reports whether hash was produced with a different cost than
// h's (or cannot be parsed), so a login handler can transparently upgrade it.
func (h Hasher) NeedsRehash(hash string) bool {
	c, err := bcrypt.Cost([]byte(hash))
	return err != nil || c != h.cost()
}

var (
	dummyMu     sync.Mutex
	dummyHashes = map[int][]byte{}
)

// VerifyDummy spends the same time as a real Verify at h's cost and always
// fails. Login handlers call it when the e-mail is unknown so response times
// do not reveal which accounts exist.
func (h Hasher) VerifyDummy(password string) {
	cost := h.cost()
	dummyMu.Lock()
	dh, ok := dummyHashes[cost]
	if !ok {
		var err error
		dh, err = bcrypt.GenerateFromPassword([]byte("cryptopay-dummy-password"), cost)
		if err != nil {
			dummyMu.Unlock()
			return
		}
		dummyHashes[cost] = dh
	}
	dummyMu.Unlock()
	_ = bcrypt.CompareHashAndPassword(dh, []byte(password))
}

// HashPassword hashes password with DefaultBcryptCost.
func HashPassword(password string) (string, error) { return Hasher{}.Hash(password) }

// HashPasswordCost hashes password with the given bcrypt cost
// (bcrypt.MinCost..bcrypt.MaxCost).
func HashPasswordCost(password string, cost int) (string, error) {
	return Hasher{Cost: cost}.Hash(password)
}

// VerifyPassword returns nil when password matches hash, ErrPasswordMismatch otherwise.
func VerifyPassword(hash, password string) error { return Hasher{}.Verify(hash, password) }

// CheckPassword reports whether password matches hash.
func CheckPassword(hash, password string) bool { return VerifyPassword(hash, password) == nil }
