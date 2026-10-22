package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CreateJob inserts a new job.
func (s *Store) CreateJob(ctx context.Context, j Job) (Job, error) {
	now := time.Now().UTC()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	j.UpdatedAt = now
	if j.State == "" {
		j.State = JobQueued
	}
	request, err := json.Marshal(nonNilMap(j.Request))
	if err != nil {
		return j, fmt.Errorf("encode job request: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO jobs (id, user_id, session_id, kind, state, template_id, runtime, instance_id,
		                  request, recipe, error_code, error_message, idempotency_key,
		                  created_at, updated_at, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.UserID, nullString(j.SessionID), string(j.Kind), string(j.State),
		nullString(j.TemplateID), nullString(j.Runtime), nullString(j.InstanceID),
		string(request), jsonOrNull(j.Recipe), nullString(j.ErrorCode), nullString(j.ErrorMessage),
		nullString(j.IdempotencyKey), formatTime(j.CreatedAt), formatTime(j.UpdatedAt),
		formatOptionalTime(j.StartedAt), formatOptionalTime(j.FinishedAt))
	if err != nil {
		if isUnique(err) {
			return j, fmt.Errorf("%w: idempotency key for job", ErrConflict)
		}
		return j, fmt.Errorf("insert job: %w", err)
	}
	return j, nil
}

// JobByID loads a job by identifier.
func (s *Store) JobByID(ctx context.Context, id string) (Job, error) {
	row := s.db.QueryRowContext(ctx, jobColumns+` WHERE id = ?`, id)
	return s.scanJob(row)
}

// JobByIdempotencyKey returns the job previously created for a key.
func (s *Store) JobByIdempotencyKey(ctx context.Context, userID, key string) (Job, error) {
	if key == "" {
		return Job{}, fmt.Errorf("%w: job", ErrNotFound)
	}
	row := s.db.QueryRowContext(ctx, jobColumns+` WHERE user_id = ? AND idempotency_key = ?`, userID, key)
	return s.scanJob(row)
}

// UpdateJobState moves a job to a new state and records optional failure
// information. Terminal states stamp the finish time.
func (s *Store) UpdateJobState(ctx context.Context, id string, state JobState, errCode, errMsg string) error {
	now := time.Now().UTC()
	if !state.Valid() {
		return fmt.Errorf("store: invalid job state %q", state)
	}
	var finished any
	var started any
	if state == JobRunning || state == JobPreparing {
		started = formatTime(now)
	}
	if state.Terminal() {
		finished = formatTime(now)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET state = ?, error_code = ?, error_message = ?,
		        started_at = COALESCE(started_at, ?), finished_at = ?, updated_at = ?
		 WHERE id = ?`,
		string(state), nullString(errCode), nullString(errMsg), started, finished, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("update job state: %w", err)
	}
	return requireAffected(res, "job")
}

// SetJobInstance records the instance a job produced.
func (s *Store) SetJobInstance(ctx context.Context, jobID, instanceID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET instance_id = ?, updated_at = ? WHERE id = ?`,
		instanceID, formatTime(time.Now().UTC()), jobID)
	if err != nil {
		return fmt.Errorf("set job instance: %w", err)
	}
	return nil
}

// SetJobRecipe stores the resolved recipe of a job.
func (s *Store) SetJobRecipe(ctx context.Context, jobID string, recipe map[string]any) error {
	encoded, err := json.Marshal(nonNilMap(recipe))
	if err != nil {
		return fmt.Errorf("encode recipe: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE jobs SET recipe = ?, updated_at = ? WHERE id = ?`,
		string(encoded), formatTime(time.Now().UTC()), jobID)
	if err != nil {
		return fmt.Errorf("set job recipe: %w", err)
	}
	return nil
}

// JobFilter narrows a job listing.
type JobFilter struct {
	UserID string
	States []JobState
	Limit  int
}

// ListJobs returns jobs matching the filter, newest first.
func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]Job, error) {
	query := jobColumns + ` WHERE 1 = 1`
	var args []any
	if f.UserID != "" {
		query += ` AND user_id = ?`
		args = append(args, f.UserID)
	}
	if len(f.States) > 0 {
		query += ` AND state IN (` + placeholders(len(f.States)) + `)`
		for _, st := range f.States {
			args = append(args, string(st))
		}
	}
	query += ` ORDER BY created_at DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJobRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// CountJobsByState returns the number of jobs in each state.
func (s *Store) CountJobsByState(ctx context.Context) (map[JobState]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM jobs GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("count jobs: %w", err)
	}
	defer rows.Close()
	out := map[JobState]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("scan job count: %w", err)
		}
		out[JobState(state)] = n
	}
	return out, rows.Err()
}

// jobColumns is the shared column list of the jobs table.
const jobColumns = `SELECT id, user_id, session_id, kind, state, template_id, runtime, instance_id,
       request, recipe, error_code, error_message, idempotency_key,
       created_at, updated_at, started_at, finished_at FROM jobs`

// scanJob reads a single job row.
func (s *Store) scanJob(row *sql.Row) (Job, error) {
	var j Job
	var kind, state string
	var request, recipe sql.NullString
	var created, updated string
	var started, finished sql.NullString
	var sessionID, templateID, runtime, instanceID sql.NullString
	var errorCode, errorMessage, idempotencyKey sql.NullString
	err := row.Scan(&j.ID, &j.UserID, &sessionID, &kind, &state, &templateID, &runtime,
		&instanceID, &request, &recipe, &errorCode, &errorMessage, &idempotencyKey,
		&created, &updated, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return j, fmt.Errorf("%w: job", ErrNotFound)
	}
	if err != nil {
		return j, fmt.Errorf("scan job: %w", err)
	}
	j.Kind = JobKind(kind)
	j.State = JobState(state)
	j.SessionID = sessionID.String
	j.TemplateID = templateID.String
	j.Runtime = runtime.String
	j.InstanceID = instanceID.String
	j.ErrorCode = errorCode.String
	j.ErrorMessage = errorMessage.String
	j.IdempotencyKey = idempotencyKey.String
	j.Request = decodeMap(request)
	j.Recipe = decodeMap(recipe)
	j.CreatedAt = parseTime(created)
	j.UpdatedAt = parseTime(updated)
	j.StartedAt = parseOptionalTime(started)
	j.FinishedAt = parseOptionalTime(finished)
	return j, nil
}

// scanJobRows reads a job from an open row set.
func scanJobRows(rows *sql.Rows) (Job, error) {
	var j Job
	var kind, state string
	var request, recipe sql.NullString
	var created, updated string
	var started, finished sql.NullString
	var sessionID, templateID, runtime, instanceID sql.NullString
	var errorCode, errorMessage, idempotencyKey sql.NullString
	if err := rows.Scan(&j.ID, &j.UserID, &sessionID, &kind, &state, &templateID, &runtime,
		&instanceID, &request, &recipe, &errorCode, &errorMessage, &idempotencyKey,
		&created, &updated, &started, &finished); err != nil {
		return j, fmt.Errorf("scan job: %w", err)
	}
	j.Kind = JobKind(kind)
	j.State = JobState(state)
	j.SessionID = sessionID.String
	j.TemplateID = templateID.String
	j.Runtime = runtime.String
	j.InstanceID = instanceID.String
	j.ErrorCode = errorCode.String
	j.ErrorMessage = errorMessage.String
	j.IdempotencyKey = idempotencyKey.String
	j.Request = decodeMap(request)
	j.Recipe = decodeMap(recipe)
	j.CreatedAt = parseTime(created)
	j.UpdatedAt = parseTime(updated)
	j.StartedAt = parseOptionalTime(started)
	j.FinishedAt = parseOptionalTime(finished)
	return j, nil
}

// CreateInstance inserts a new instance record.
func (s *Store) CreateInstance(ctx context.Context, i Instance) (Instance, error) {
	now := time.Now().UTC()
	if i.CreatedAt.IsZero() {
		i.CreatedAt = now
	}
	i.UpdatedAt = now
	if i.State == "" {
		i.State = InstanceRequested
	}
	resources, err := json.Marshal(nonNilMap(i.Resources))
	if err != nil {
		return i, fmt.Errorf("encode instance resources: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO instances (id, job_id, user_id, template_id, runtime, runtime_id, name, state,
		                        resources, workspace, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.ID, nullString(i.JobID), i.UserID, nullString(i.TemplateID), i.Runtime,
		nullString(i.RuntimeID), nullString(i.Name), string(i.State), string(resources),
		nullString(i.Workspace), formatTime(i.ExpiresAt), formatTime(i.CreatedAt), formatTime(i.UpdatedAt))
	if err != nil {
		return i, fmt.Errorf("insert instance: %w", err)
	}
	return i, nil
}

// InstanceByID loads an instance by identifier.
func (s *Store) InstanceByID(ctx context.Context, id string) (Instance, error) {
	row := s.db.QueryRowContext(ctx, instanceColumns+` WHERE id = ?`, id)
	return s.scanInstance(row)
}

// UpdateInstanceState moves an instance to a new state.
func (s *Store) UpdateInstanceState(ctx context.Context, id string, state InstanceState) error {
	if !state.Valid() {
		return fmt.Errorf("store: invalid instance state %q", state)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE instances SET state = ?, updated_at = ? WHERE id = ?`,
		string(state), formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("update instance state: %w", err)
	}
	return requireAffected(res, "instance")
}

// SetInstanceRuntimeID records the backend identifier of an instance.
func (s *Store) SetInstanceRuntimeID(ctx context.Context, id, runtimeID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE instances SET runtime_id = ?, updated_at = ? WHERE id = ?`,
		runtimeID, formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("set instance runtime id: %w", err)
	}
	return nil
}

// ExtendInstanceTTL pushes the expiry of an instance further into the future.
func (s *Store) ExtendInstanceTTL(ctx context.Context, id string, expiresAt time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE instances SET expires_at = ?, updated_at = ? WHERE id = ?`,
		formatTime(expiresAt), formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("extend instance ttl: %w", err)
	}
	return requireAffected(res, "instance")
}

// ListInstances returns instances, optionally filtered by user and state.
func (s *Store) ListInstances(ctx context.Context, userID string, states []InstanceState) ([]Instance, error) {
	query := instanceColumns + ` WHERE 1 = 1`
	var args []any
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	if len(states) > 0 {
		query += ` AND state IN (` + placeholders(len(states)) + `)`
		for _, st := range states {
			args = append(args, string(st))
		}
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		i, err := scanInstanceRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ExpiredInstances returns live instances whose TTL has elapsed.
func (s *Store) ExpiredInstances(ctx context.Context, now time.Time) ([]Instance, error) {
	rows, err := s.db.QueryContext(ctx,
		instanceColumns+` WHERE state NOT IN ('destroyed', 'failed') AND expires_at <= ? ORDER BY expires_at`,
		formatTime(now.UTC()))
	if err != nil {
		return nil, fmt.Errorf("list expired instances: %w", err)
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		i, err := scanInstanceRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CountLiveInstances returns the number of instances that are not destroyed.
func (s *Store) CountLiveInstances(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM instances WHERE state NOT IN ('destroyed', 'failed')`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count live instances: %w", err)
	}
	return n, nil
}

// CountLiveInstancesByUser returns the number of live instances owned by a user.
func (s *Store) CountLiveInstancesByUser(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM instances WHERE user_id = ? AND state NOT IN ('destroyed', 'failed')`,
		userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count user instances: %w", err)
	}
	return n, nil
}

// DeleteInstance removes an instance record.
func (s *Store) DeleteInstance(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete instance: %w", err)
	}
	return nil
}

// instanceColumns is the shared column list of the instances table.
const instanceColumns = `SELECT id, job_id, user_id, template_id, runtime, runtime_id, name, state,
       resources, workspace, expires_at, created_at, updated_at FROM instances`

// scanInstance reads a single instance row.
func (s *Store) scanInstance(row *sql.Row) (Instance, error) {
	var i Instance
	var state string
	var resources string
	var expires, created, updated string
	var jobID, templateID, runtimeID, name, workspace sql.NullString
	err := row.Scan(&i.ID, &jobID, &i.UserID, &templateID, &i.Runtime, &runtimeID,
		&name, &state, &resources, &workspace, &expires, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return i, fmt.Errorf("%w: instance", ErrNotFound)
	}
	if err != nil {
		return i, fmt.Errorf("scan instance: %w", err)
	}
	i.JobID = jobID.String
	i.TemplateID = templateID.String
	i.RuntimeID = runtimeID.String
	i.Name = name.String
	i.Workspace = workspace.String
	i.State = InstanceState(state)
	i.Resources = decodeMapAny(resources)
	i.ExpiresAt = parseTime(expires)
	i.CreatedAt = parseTime(created)
	i.UpdatedAt = parseTime(updated)
	return i, nil
}

// scanInstanceRows reads an instance from an open row set.
func scanInstanceRows(rows *sql.Rows) (Instance, error) {
	var i Instance
	var state string
	var resources string
	var expires, created, updated string
	var jobID, templateID, runtimeID, name, workspace sql.NullString
	if err := rows.Scan(&i.ID, &jobID, &i.UserID, &templateID, &i.Runtime, &runtimeID,
		&name, &state, &resources, &workspace, &expires, &created, &updated); err != nil {
		return i, fmt.Errorf("scan instance: %w", err)
	}
	i.JobID = jobID.String
	i.TemplateID = templateID.String
	i.RuntimeID = runtimeID.String
	i.Name = name.String
	i.Workspace = workspace.String
	i.State = InstanceState(state)
	i.Resources = decodeMapAny(resources)
	i.ExpiresAt = parseTime(expires)
	i.CreatedAt = parseTime(created)
	i.UpdatedAt = parseTime(updated)
	return i, nil
}
