package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CreateUser inserts a new local account.
func (s *Store) CreateUser(ctx context.Context, u User) (User, error) {
	now := time.Now().UTC()
	u.CreatedAt = now
	u.UpdatedAt = now
	if u.ID == "" {
		return u, fmt.Errorf("store: user requires an id")
	}
	if u.Role == "" {
		u.Role = RoleUser
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, disabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, string(u.Role), boolToInt(u.Disabled),
		formatTime(u.CreatedAt), formatTime(u.UpdatedAt))
	if err != nil {
		if isUnique(err) {
			return u, fmt.Errorf("%w: user %q", ErrConflict, u.Username)
		}
		return u, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// UserByUsername looks a user up by its login name.
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, updated_at
		 FROM users WHERE username = ?`, username))
}

// UserByID looks a user up by its identifier.
func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, updated_at
		 FROM users WHERE id = ?`, id))
}

// ListUsers returns every account ordered by username.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password_hash, role, disabled, created_at, updated_at
		 FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUserRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of accounts in the database.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// UpdatePassword replaces the stored password hash of a user.
func (s *Store) UpdatePassword(ctx context.Context, userID, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, formatTime(time.Now().UTC()), userID)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return requireAffected(res, "user")
}

// SetUserDisabled enables or disables an account.
func (s *Store) SetUserDisabled(ctx context.Context, userID string, disabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(disabled), formatTime(time.Now().UTC()), userID)
	if err != nil {
		return fmt.Errorf("set user disabled: %w", err)
	}
	return requireAffected(res, "user")
}

// DeleteUser removes an account.
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return requireAffected(res, "user")
}

// scanUser reads a single user row.
func (s *Store) scanUser(row *sql.Row) (User, error) {
	var u User
	var role string
	var disabled int
	var created, updated string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &disabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return u, fmt.Errorf("%w: user", ErrNotFound)
	}
	if err != nil {
		return u, fmt.Errorf("scan user: %w", err)
	}
	u.Role = Role(role)
	u.Disabled = disabled != 0
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return u, nil
}

// scanUserRows reads a user from an open row set.
func scanUserRows(rows *sql.Rows) (User, error) {
	var u User
	var role string
	var disabled int
	var created, updated string
	if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &disabled, &created, &updated); err != nil {
		return u, fmt.Errorf("scan user: %w", err)
	}
	u.Role = Role(role)
	u.Disabled = disabled != 0
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return u, nil
}

// CreateAPIKey stores a new client credential. The secret is never persisted;
// keyHash must already be the hash of the issued secret.
func (s *Store) CreateAPIKey(ctx context.Context, k APIKey) (APIKey, error) {
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, user_id, name, prefix, key_hash, scopes, expires_at, last_used_at, revoked_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.UserID, k.Name, k.Prefix, k.KeyHash, strings.Join(k.Scopes, ","),
		formatOptionalTime(k.ExpiresAt), formatOptionalTime(k.LastUsedAt),
		formatOptionalTime(k.RevokedAt), formatTime(k.CreatedAt))
	if err != nil {
		if isUnique(err) {
			return k, fmt.Errorf("%w: api key", ErrConflict)
		}
		return k, fmt.Errorf("insert api key: %w", err)
	}
	return k, nil
}

// APIKeyByHash looks a key up by the hash of its secret.
func (s *Store) APIKeyByHash(ctx context.Context, hash string) (APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, prefix, key_hash, scopes, expires_at, last_used_at, revoked_at, created_at
		 FROM api_keys WHERE key_hash = ?`, hash)
	return s.scanAPIKey(row)
}

// APIKeyByID looks a key up by its identifier.
func (s *Store) APIKeyByID(ctx context.Context, id string) (APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, prefix, key_hash, scopes, expires_at, last_used_at, revoked_at, created_at
		 FROM api_keys WHERE id = ?`, id)
	return s.scanAPIKey(row)
}

// ListAPIKeys returns the keys owned by a user, or every key when userID is empty.
func (s *Store) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	query := `SELECT id, user_id, name, prefix, key_hash, scopes, expires_at, last_used_at, revoked_at, created_at
	          FROM api_keys`
	var args []any
	if userID != "" {
		query += ` WHERE user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		k, err := scanAPIKeyRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// TouchAPIKey records that a key was just used.
func (s *Store) TouchAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("touch api key: %w", err)
	}
	return nil
}

// RevokeAPIKey marks a key as revoked. Revocation is immediate.
func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return requireAffected(res, "api key")
}

// DeleteAPIKey removes a key permanently.
func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	return requireAffected(res, "api key")
}

// scanAPIKey reads a single API key row.
func (s *Store) scanAPIKey(row *sql.Row) (APIKey, error) {
	var k APIKey
	var scopes string
	var expires, lastUsed, revoked sql.NullString
	var created string
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.KeyHash, &scopes,
		&expires, &lastUsed, &revoked, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return k, fmt.Errorf("%w: api key", ErrNotFound)
	}
	if err != nil {
		return k, fmt.Errorf("scan api key: %w", err)
	}
	k.Scopes = splitScopes(scopes)
	k.ExpiresAt = parseOptionalTime(expires)
	k.LastUsedAt = parseOptionalTime(lastUsed)
	k.RevokedAt = parseOptionalTime(revoked)
	k.CreatedAt = parseTime(created)
	return k, nil
}

// scanAPIKeyRows reads an API key from an open row set.
func scanAPIKeyRows(rows *sql.Rows) (APIKey, error) {
	var k APIKey
	var scopes string
	var expires, lastUsed, revoked sql.NullString
	var created string
	if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.KeyHash, &scopes,
		&expires, &lastUsed, &revoked, &created); err != nil {
		return k, fmt.Errorf("scan api key: %w", err)
	}
	k.Scopes = splitScopes(scopes)
	k.ExpiresAt = parseOptionalTime(expires)
	k.LastUsedAt = parseOptionalTime(lastUsed)
	k.RevokedAt = parseOptionalTime(revoked)
	k.CreatedAt = parseTime(created)
	return k, nil
}

// splitScopes parses the comma separated scope list.
func splitScopes(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CreateSession stores a browser session.
func (s *Store) CreateSession(ctx context.Context, sess Session) (Session, error) {
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, csrf_token, created_at, expires_at, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.TokenHash, sess.CSRFToken,
		formatTime(sess.CreatedAt), formatTime(sess.ExpiresAt), formatTime(sess.LastSeen))
	if err != nil {
		if isUnique(err) {
			return sess, fmt.Errorf("%w: session", ErrConflict)
		}
		return sess, fmt.Errorf("insert session: %w", err)
	}
	return sess, nil
}

// SessionByTokenHash looks a session up by the hash of its cookie value.
func (s *Store) SessionByTokenHash(ctx context.Context, hash string) (Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, token_hash, csrf_token, created_at, expires_at, last_seen_at
		 FROM sessions WHERE token_hash = ?`, hash)
	var sess Session
	var created, expires, lastSeen string
	err := row.Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &sess.CSRFToken, &created, &expires, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return sess, fmt.Errorf("%w: session", ErrNotFound)
	}
	if err != nil {
		return sess, fmt.Errorf("scan session: %w", err)
	}
	sess.CreatedAt = parseTime(created)
	sess.ExpiresAt = parseTime(expires)
	sess.LastSeen = parseTime(lastSeen)
	return sess, nil
}

// TouchSession updates the last seen timestamp of a session.
func (s *Store) TouchSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions that are past their expiry.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, formatTime(now.UTC()))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// boolToInt converts a boolean to the integer storage form.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUnique reports whether the error is a uniqueness violation.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// requireAffected turns an empty update into a not-found error.
func requireAffected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return nil
}
