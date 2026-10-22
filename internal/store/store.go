// Package store implements the persistent metadata store of the Double Agent
// control plane.
//
// SQLite is used so that the daemon stays a single self-contained binary. The
// schema is migrated forward transactionally; see docs/INSTALLATION_AND_OPERATIONS.md.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver, no cgo required
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("store: record not found")

// ErrConflict is returned when a uniqueness constraint is violated.
var ErrConflict = errors.New("store: record already exists")

// Store is the metadata database.
type Store struct {
	db *sql.DB
}

// Open opens or creates the metadata database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create state directory: %w", err)
		}
	}
	dsn := "file:" + path + "?" + strings.Join([]string{
		"_pragma=busy_timeout(5000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
	}, "&")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite tolerates a single writer; more connections only add lock
	// contention, so the pool is deliberately small.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the underlying handle for tests and advanced queries.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// migrations are applied in order exactly once.
var migrations = []struct {
	version int
	name    string
	apply   func(context.Context, *sql.Tx) error
}{
	{version: 1, name: "initial schema", apply: migrateInitial},
}

// migrate brings the schema up to date.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	applied := map[int]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scan migration: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read migrations: %w", err)
	}
	rows.Close()

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if err := m.apply(ctx, tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, formatTime(time.Now().UTC())); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
	}
	return nil
}

// migrateInitial creates the full initial schema.
func migrateInitial(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE users (
			id            TEXT PRIMARY KEY,
			username      TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role          TEXT NOT NULL,
			disabled      INTEGER NOT NULL DEFAULT 0,
			created_at    TEXT NOT NULL,
			updated_at    TEXT NOT NULL
		)`,
		`CREATE TABLE api_keys (
			id          TEXT PRIMARY KEY,
			user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name        TEXT NOT NULL,
			prefix      TEXT NOT NULL,
			key_hash    TEXT NOT NULL UNIQUE,
			scopes      TEXT NOT NULL,
			expires_at  TEXT,
			last_used_at TEXT,
			revoked_at  TEXT,
			created_at  TEXT NOT NULL
		)`,
		`CREATE INDEX api_keys_user_idx ON api_keys(user_id)`,
		`CREATE TABLE sessions (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL UNIQUE,
			csrf_token TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL
		)`,
		`CREATE TABLE templates (
			id         TEXT PRIMARY KEY,
			version    TEXT NOT NULL,
			spec       TEXT NOT NULL,
			digest     TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE jobs (
			id              TEXT PRIMARY KEY,
			user_id         TEXT NOT NULL,
			session_id      TEXT,
			kind            TEXT NOT NULL,
			state           TEXT NOT NULL,
			template_id     TEXT,
			runtime         TEXT,
			instance_id     TEXT,
			request         TEXT NOT NULL,
			recipe          TEXT,
			error_code      TEXT,
			error_message   TEXT,
			idempotency_key TEXT,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			started_at      TEXT,
			finished_at     TEXT
		)`,
		`CREATE INDEX jobs_user_idx ON jobs(user_id, created_at DESC)`,
		`CREATE INDEX jobs_state_idx ON jobs(state)`,
		`CREATE UNIQUE INDEX jobs_idempotency_idx ON jobs(user_id, idempotency_key)
			WHERE idempotency_key IS NOT NULL AND idempotency_key != ''`,
		`CREATE TABLE instances (
			id          TEXT PRIMARY KEY,
			job_id      TEXT,
			user_id     TEXT NOT NULL,
			template_id TEXT,
			runtime     TEXT NOT NULL,
			runtime_id  TEXT,
			name        TEXT,
			state       TEXT NOT NULL,
			resources   TEXT NOT NULL,
			workspace   TEXT,
			expires_at  TEXT NOT NULL,
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL
		)`,
		`CREATE INDEX instances_user_idx ON instances(user_id, state)`,
		`CREATE INDEX instances_expiry_idx ON instances(expires_at)`,
		`CREATE TABLE artifacts (
			id          TEXT PRIMARY KEY,
			user_id     TEXT NOT NULL,
			job_id      TEXT,
			instance_id TEXT,
			name        TEXT NOT NULL,
			path        TEXT NOT NULL,
			mime        TEXT NOT NULL,
			size        INTEGER NOT NULL,
			digest      TEXT NOT NULL,
			state       TEXT NOT NULL,
			created_at  TEXT NOT NULL,
			expires_at  TEXT
		)`,
		`CREATE INDEX artifacts_user_idx ON artifacts(user_id, created_at DESC)`,
		`CREATE INDEX artifacts_expiry_idx ON artifacts(state, expires_at)`,
		`CREATE TABLE audit_events (
			id            TEXT PRIMARY KEY,
			ts            TEXT NOT NULL,
			actor_type    TEXT NOT NULL,
			actor_id      TEXT NOT NULL,
			action        TEXT NOT NULL,
			target_type   TEXT,
			target_id     TEXT,
			decision      TEXT NOT NULL,
			correlation_id TEXT,
			policy_version TEXT,
			result        TEXT,
			metadata      TEXT
		)`,
		`CREATE INDEX audit_ts_idx ON audit_events(ts DESC)`,
		`CREATE INDEX audit_actor_idx ON audit_events(actor_id, ts DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// firstLine returns the leading line of a statement, used in error messages.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// formatTime renders a timestamp in the canonical storage format.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// parseTime reads a timestamp from the canonical storage format.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// parseOptionalTime reads a nullable timestamp.
func parseOptionalTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

// formatOptionalTime renders a nullable timestamp.
func formatOptionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// nullString converts an optional string for storage.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
