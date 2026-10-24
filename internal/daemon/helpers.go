package daemon

import (
	"fmt"

	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/ids"
)

// hashPassword derives the stored hash of a password.
func hashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("daemon: the password is required")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", fmt.Errorf("daemon: hash the password: %w", err)
	}
	return hash, nil
}

// newUserID returns a fresh user identifier.
func newUserID() string { return ids.New(ids.PrefixUser) }
