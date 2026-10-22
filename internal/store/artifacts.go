package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CreateArtifact records an exported file.
func (s *Store) CreateArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.State == "" {
		a.State = ArtifactAvailable
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO artifacts (id, user_id, job_id, instance_id, name, path, mime, size, digest, state, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.UserID, nullString(a.JobID), nullString(a.InstanceID), a.Name, a.Path, a.MIME,
		a.Size, a.Digest, string(a.State), formatTime(a.CreatedAt), formatOptionalTime(a.ExpiresAt))
	if err != nil {
		return a, fmt.Errorf("insert artifact: %w", err)
	}
	return a, nil
}

// ArtifactByID loads an artifact by identifier.
func (s *Store) ArtifactByID(ctx context.Context, id string) (Artifact, error) {
	row := s.db.QueryRowContext(ctx, artifactColumns+` WHERE id = ?`, id)
	return s.scanArtifact(row)
}

// ListArtifacts returns the artifacts of a user, newest first.
func (s *Store) ListArtifacts(ctx context.Context, userID string, limit int) ([]Artifact, error) {
	query := artifactColumns
	var args []any
	if userID != "" {
		query += ` WHERE user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY created_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		a, err := scanArtifactRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetArtifactState updates the lifecycle state of an artifact.
func (s *Store) SetArtifactState(ctx context.Context, id string, state ArtifactState) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE artifacts SET state = ? WHERE id = ?`, string(state), id)
	if err != nil {
		return fmt.Errorf("set artifact state: %w", err)
	}
	return nil
}

// ExpiredArtifacts returns available artifacts whose retention deadline passed.
func (s *Store) ExpiredArtifacts(ctx context.Context, now time.Time) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx,
		artifactColumns+` WHERE state = 'available' AND expires_at IS NOT NULL AND expires_at <= ?`,
		formatTime(now.UTC()))
	if err != nil {
		return nil, fmt.Errorf("list expired artifacts: %w", err)
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		a, err := scanArtifactRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteArtifact removes an artifact record.
func (s *Store) DeleteArtifact(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete artifact: %w", err)
	}
	return nil
}

// AppendAuditEvent writes a security and operational record.
func (s *Store) AppendAuditEvent(ctx context.Context, e AuditEvent) (AuditEvent, error) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.Decision == "" {
		e.Decision = DecisionAllow
	}
	meta, err := json.Marshal(nonNilMap(e.Metadata))
	if err != nil {
		return e, fmt.Errorf("encode audit metadata: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO audit_events (id, ts, actor_type, actor_id, action, target_type, target_id,
		                          decision, correlation_id, policy_version, result, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, formatTime(e.Timestamp), e.ActorType, e.ActorID, e.Action,
		nullString(e.TargetType), nullString(e.TargetID), e.Decision,
		nullString(e.CorrelationID), nullString(e.PolicyVersion), nullString(e.Result), string(meta))
	if err != nil {
		return e, fmt.Errorf("insert audit event: %w", err)
	}
	return e, nil
}

// AuditFilter narrows an audit listing.
type AuditFilter struct {
	ActorID string
	Action  string
	Since   *time.Time
	Limit   int
}

// ListAuditEvents returns audit records, newest first.
func (s *Store) ListAuditEvents(ctx context.Context, f AuditFilter) ([]AuditEvent, error) {
	query := `SELECT id, ts, actor_type, actor_id, action, target_type, target_id, decision,
	                 correlation_id, policy_version, result, metadata FROM audit_events WHERE 1 = 1`
	var args []any
	if f.ActorID != "" {
		query += ` AND actor_id = ?`
		args = append(args, f.ActorID)
	}
	if f.Action != "" {
		query += ` AND action = ?`
		args = append(args, f.Action)
	}
	if f.Since != nil {
		query += ` AND ts >= ?`
		args = append(args, formatTime(*f.Since))
	}
	query += ` ORDER BY ts DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		e, err := scanAuditRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountAuditEvents returns the total number of audit records.
func (s *Store) CountAuditEvents(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count audit events: %w", err)
	}
	return n, nil
}

// artifactColumns is the shared column list of the artifacts table.
const artifactColumns = `SELECT id, user_id, job_id, instance_id, name, path, mime, size, digest, state, created_at, expires_at FROM artifacts`

// scanArtifact reads a single artifact row.
func (s *Store) scanArtifact(row *sql.Row) (Artifact, error) {
	var a Artifact
	var state string
	var created string
	var expires sql.NullString
	var jobID, instanceID sql.NullString
	err := row.Scan(&a.ID, &a.UserID, &jobID, &instanceID, &a.Name, &a.Path, &a.MIME,
		&a.Size, &a.Digest, &state, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return a, fmt.Errorf("%w: artifact", ErrNotFound)
	}
	if err != nil {
		return a, fmt.Errorf("scan artifact: %w", err)
	}
	a.JobID = jobID.String
	a.InstanceID = instanceID.String
	a.State = ArtifactState(state)
	a.CreatedAt = parseTime(created)
	a.ExpiresAt = parseOptionalTime(expires)
	return a, nil
}

// scanArtifactRows reads an artifact from an open row set.
func scanArtifactRows(rows *sql.Rows) (Artifact, error) {
	var a Artifact
	var state string
	var created string
	var expires sql.NullString
	var jobID, instanceID sql.NullString
	if err := rows.Scan(&a.ID, &a.UserID, &jobID, &instanceID, &a.Name, &a.Path, &a.MIME,
		&a.Size, &a.Digest, &state, &created, &expires); err != nil {
		return a, fmt.Errorf("scan artifact: %w", err)
	}
	a.JobID = jobID.String
	a.InstanceID = instanceID.String
	a.State = ArtifactState(state)
	a.CreatedAt = parseTime(created)
	a.ExpiresAt = parseOptionalTime(expires)
	return a, nil
}

// scanAuditRows reads an audit event from an open row set.
func scanAuditRows(rows *sql.Rows) (AuditEvent, error) {
	var e AuditEvent
	var ts string
	var metadata string
	var targetType, targetID, correlationID, policyVersion, result sql.NullString
	if err := rows.Scan(&e.ID, &ts, &e.ActorType, &e.ActorID, &e.Action, &targetType,
		&targetID, &e.Decision, &correlationID, &policyVersion, &result, &metadata); err != nil {
		return e, fmt.Errorf("scan audit event: %w", err)
	}
	e.TargetType = targetType.String
	e.TargetID = targetID.String
	e.CorrelationID = correlationID.String
	e.PolicyVersion = policyVersion.String
	e.Result = result.String
	e.Timestamp = parseTime(ts)
	e.Metadata = decodeMapAny(metadata)
	return e, nil
}

// nonNilMap returns an empty map when the input is nil so JSON encodes "{}".
func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// jsonOrNull encodes a map, returning nil for an empty map.
func jsonOrNull(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return string(b)
}

// decodeMap decodes a nullable JSON object column.
func decodeMap(v sql.NullString) map[string]any {
	if !v.Valid || v.String == "" {
		return nil
	}
	return decodeMapAny(v.String)
}

// decodeMapAny decodes a JSON object column, returning nil when empty.
func decodeMapAny(s string) map[string]any {
	if s == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// placeholders renders "?, ?, ?" for an IN clause.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
