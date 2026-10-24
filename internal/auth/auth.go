// Package auth implements the identity and credential handling of the Double
// Agent control plane.
//
// Passwords are hashed with argon2id, client keys are random secrets that are
// stored only as a SHA-256 hash, and browser sessions use random cookies
// protected by CSRF tokens.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Errors returned by the package.
var (
	// ErrInvalidHash is returned when a stored password hash cannot be parsed.
	ErrInvalidHash = errors.New("auth: invalid password hash")
	// ErrMismatch is returned when a credential does not match.
	ErrMismatch = errors.New("auth: credential mismatch")
	// ErrWeakPassword is returned when a password fails the policy check.
	ErrWeakPassword = errors.New("auth: password does not meet the policy")
)

// Password policy defaults.
const (
	// MinPasswordLength is the shortest accepted password.
	MinPasswordLength = 12
	// DefaultArgonTime is the number of argon2 passes.
	DefaultArgonTime = 3
	// DefaultArgonMemory is the amount of memory in KiB.
	DefaultArgonMemory = 64 * 1024
	// DefaultArgonThreads is the number of worker threads.
	DefaultArgonThreads = 2
	// DefaultArgonKeyLength is the length of the derived key in bytes.
	DefaultArgonKeyLength = 32
	// DefaultSaltLength is the length of the random salt in bytes.
	DefaultSaltLength = 16
)

// HashPassword derives an argon2id hash from a password.
//
// The returned string encodes the algorithm, its parameters and the salt so
// that the parameters can be raised later without invalidating old hashes.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, DefaultSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, DefaultArgonTime, DefaultArgonMemory,
		DefaultArgonThreads, DefaultArgonKeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, DefaultArgonMemory, DefaultArgonTime, DefaultArgonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against an encoded argon2id hash.
func VerifyPassword(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	// An argon2id hash has the shape: "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash.
	if len(parts) != 6 || parts[1] != "argon2id" {
		return fmt.Errorf("%w: unsupported format", ErrInvalidHash)
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return fmt.Errorf("%w: bad version", ErrInvalidHash)
	}
	if version != argon2.Version {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidHash, version)
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return fmt.Errorf("%w: bad parameters", ErrInvalidHash)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("%w: bad salt", ErrInvalidHash)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("%w: bad hash", ErrInvalidHash)
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// ValidatePassword enforces the password policy.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("%w: at least %d characters are required", ErrWeakPassword, MinPasswordLength)
	}
	if len(password) > 1024 {
		return fmt.Errorf("%w: password is too long", ErrWeakPassword)
	}
	var hasLetter, hasOther bool
	for _, r := range password {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		default:
			hasOther = true
		}
	}
	if !hasLetter || !hasOther {
		return fmt.Errorf("%w: mix letters with digits or symbols", ErrWeakPassword)
	}
	return nil
}

// KeyPrefix is the human readable marker of a Double Agent client key.
const KeyPrefix = "dak"

// IssueKey creates a new client key.
//
// The returned secret is shown to the operator exactly once; only its hash is
// persisted.
func IssueKey() (secret, id, prefix, hash string, err error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", "", fmt.Errorf("auth: read key material: %w", err)
	}
	secret = KeyPrefix + "_" + base64.RawURLEncoding.EncodeToString(raw)
	return secret, "", SecretPrefix(secret), HashSecret(secret), nil
}

// SecretPrefix returns the displayable prefix of a secret, for example
// "dak_AbCdEf". It never reveals enough of the secret to be usable.
func SecretPrefix(secret string) string {
	const shown = 6
	if len(secret) <= shown {
		return secret
	}
	return secret[:shown]
}

// HashSecret computes the stored hash of a client key secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual compares two strings without leaking their contents
// through timing.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
