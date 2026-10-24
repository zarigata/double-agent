package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/zarigata/double-agent/internal/auth"
)

// TestHashPasswordAndVerifyPasswordRoundTrip checks that a password can be
// verified against its own hash.
func TestHashPasswordAndVerifyPasswordRoundTrip(t *testing.T) {
	const password = "a-correct-horse-1"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash the password: %v", err)
	}
	if hash == "" {
		t.Fatal("the hash is empty")
	}
	if strings.Contains(hash, password) {
		t.Error("the hash carries the clear-text password")
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("the hash %q does not announce the argon2id algorithm", hash)
	}
	if err := auth.VerifyPassword(password, hash); err != nil {
		t.Errorf("the password did not verify: %v", err)
	}
}

// TestVerifyPasswordRejectsAWrongPassword checks that a wrong password is
// refused.
func TestVerifyPasswordRejectsAWrongPassword(t *testing.T) {
	hash, err := auth.HashPassword("a-correct-horse-1")
	if err != nil {
		t.Fatalf("hash the password: %v", err)
	}
	err = auth.VerifyPassword("a-wrong-horse-1", hash)
	if !errors.Is(err, auth.ErrMismatch) {
		t.Fatalf("a wrong password returned %v, want a mismatch", err)
	}
}

// TestVerifyPasswordRejectsAMalformedHash checks that a stored value that is
// not an argon2id hash is reported as such.
func TestVerifyPasswordRejectsAMalformedHash(t *testing.T) {
	cases := []struct {
		name string
		hash string
	}{
		{name: "an empty hash", hash: ""},
		{name: "a plain text password", hash: "a-correct-horse-1"},
		{name: "an incomplete hash", hash: "$argon2id$v=19$m=65536,t=3,p=2"},
		{name: "another algorithm", hash: "$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
		{name: "an unsupported version", hash: "$argon2id$v=18$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
		{name: "a broken version", hash: "$argon2id$v=x$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
		{name: "broken parameters", hash: "$argon2id$v=19$m=many,t=3,p=2$c2FsdA$aGFzaA"},
		{name: "a salt that is not base64", hash: "$argon2id$v=19$m=65536,t=3,p=2$not!base64$aGFzaA"},
		{name: "a hash that is not base64", hash: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$not!base64"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := auth.VerifyPassword("a-correct-horse-1", test.hash)
			if !errors.Is(err, auth.ErrInvalidHash) {
				t.Errorf("the hash %q returned %v, want an invalid hash", test.hash, err)
			}
		})
	}
}

// TestValidatePasswordEnforcesThePolicy checks the password policy.
func TestValidatePasswordEnforcesThePolicy(t *testing.T) {
	cases := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{name: "a long password with letters and digits", password: "a-correct-horse-1"},
		{name: "a long password with letters and symbols", password: "a-correct-horse!!"},
		{name: "an empty password", password: "", wantErr: true},
		{name: "a short password", password: "short1", wantErr: true},
		{name: "only letters", password: "alllettersonly", wantErr: true},
		{name: "only digits", password: "12345678901234", wantErr: true},
		{name: "only symbols", password: "!@#$%^&*()_+{}", wantErr: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := auth.ValidatePassword(test.password)
			if test.wantErr {
				if !errors.Is(err, auth.ErrWeakPassword) {
					t.Errorf("the password %q returned %v, want a weak password", test.password, err)
				}
				return
			}
			if err != nil {
				t.Errorf("the password %q was refused: %v", test.password, err)
			}
		})
	}
}

// TestHashPasswordRefusesAWeakPassword checks that the policy is applied
// before the password is hashed.
func TestHashPasswordRefusesAWeakPassword(t *testing.T) {
	if _, err := auth.HashPassword("short"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("a weak password returned %v, want a weak password", err)
	}
}

// TestHashPasswordIsSalted checks that two hashes of the same password differ
// and that both of them still verify.
func TestHashPasswordIsSalted(t *testing.T) {
	const password = "a-correct-horse-1"
	first, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash the password: %v", err)
	}
	second, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash the password: %v", err)
	}
	if first == second {
		t.Error("two hashes of the same password are identical: the salt is missing")
	}
	if err := auth.VerifyPassword(password, first); err != nil {
		t.Errorf("the first hash did not verify: %v", err)
	}
	if err := auth.VerifyPassword(password, second); err != nil {
		t.Errorf("the second hash did not verify: %v", err)
	}
}

// TestHashSecretIsDeterministic checks that the hash of a secret is stable
// and that it does not carry the secret.
func TestHashSecretIsDeterministic(t *testing.T) {
	const secret = "dak_" + "2Xk9" + "mQpL" + "7vRt" + "Y3wZ"
	first := auth.HashSecret(secret)
	second := auth.HashSecret(secret)
	if first != second {
		t.Errorf("the hash changed between two calls: %q and %q", first, second)
	}
	if len(first) != 64 {
		t.Errorf("the hash %q holds %d characters, want 64", first, len(first))
	}
	if strings.Contains(first, secret) {
		t.Error("the hash carries the clear-text secret")
	}
	if auth.HashSecret(secret+"x") == first {
		t.Error("two different secrets share a hash")
	}
}

// TestSecretPrefixDoesNotRevealTheSecret checks that the displayable prefix
// of a secret is too short to be used.
func TestSecretPrefixDoesNotRevealTheSecret(t *testing.T) {
	const secret = "dak_" + "2Xk9" + "mQpL" + "7vRt" + "Y3wZ"
	prefix := auth.SecretPrefix(secret)
	if prefix == secret {
		t.Fatal("the prefix is the whole secret")
	}
	if !strings.HasPrefix(secret, prefix) {
		t.Errorf("the prefix %q is not the start of the secret", prefix)
	}
	if len(prefix) >= len(secret)/2 {
		t.Errorf("the prefix %q reveals too much of the secret", prefix)
	}
	// A secret that is shorter than the shown window is returned as it is.
	if got := auth.SecretPrefix("dak"); got != "dak" {
		t.Errorf("the prefix of a short secret is %q, want dak", got)
	}
}

// TestIssueKeyIssuesADistinctSecret checks the shape of a client key.
func TestIssueKeyIssuesADistinctSecret(t *testing.T) {
	secret, _, prefix, hash, err := auth.IssueKey()
	if err != nil {
		t.Fatalf("issue a key: %v", err)
	}
	if !strings.HasPrefix(secret, auth.KeyPrefix+"_") {
		t.Errorf("the secret %q does not carry the key prefix", secret)
	}
	if prefix != auth.SecretPrefix(secret) {
		t.Errorf("the prefix %q does not match the secret", prefix)
	}
	if hash != auth.HashSecret(secret) {
		t.Error("the hash does not match the secret")
	}
	other, _, _, _, err := auth.IssueKey()
	if err != nil {
		t.Fatalf("issue a key: %v", err)
	}
	if other == secret {
		t.Error("two keys share a secret")
	}
}

// TestConstantTimeEqual checks the comparison helper.
func TestConstantTimeEqual(t *testing.T) {
	if !auth.ConstantTimeEqual("same", "same") {
		t.Error("two equal values were reported as different")
	}
	if auth.ConstantTimeEqual("same", "other") {
		t.Error("two different values were reported as equal")
	}
}
