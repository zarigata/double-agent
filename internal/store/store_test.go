package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore opens a store in a temporary directory.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCreateAndReadUser(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	created, err := st.CreateUser(ctx, User{
		ID:           "usr_1",
		Username:     "ada",
		PasswordHash: "hash",
		Role:         RoleAdmin,
	})
	if err != nil {
		t.Fatalf("create the user: %v", err)
	}
	if created.CreatedAt.IsZero() {
		t.Error("the creation timestamp was not set")
	}

	got, err := st.UserByUsername(ctx, "ada")
	if err != nil {
		t.Fatalf("read the user: %v", err)
	}
	if got.ID != "usr_1" || got.Role != RoleAdmin {
		t.Errorf("unexpected user: %+v", got)
	}
	if got.PasswordHash != "hash" {
		t.Errorf("the password hash was not preserved: %q", got.PasswordHash)
	}
}

func TestCreateUserRejectsDuplicates(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	_, err := st.CreateUser(ctx, User{ID: "usr_1", Username: "ada", PasswordHash: "h"})
	if err != nil {
		t.Fatalf("create the user: %v", err)
	}
	_, err = st.CreateUser(ctx, User{ID: "usr_2", Username: "ada", PasswordHash: "h"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
}

func TestUserNotFound(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.UserByUsername(context.Background(), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateUser(ctx, User{ID: "usr_1", Username: "ada", PasswordHash: "h"}); err != nil {
		t.Fatalf("create the user: %v", err)
	}
	key, err := st.CreateAPIKey(ctx, APIKey{
		ID:      "key_1",
		UserID:  "usr_1",
		Name:    "cli",
		Prefix:  "dak_abc",
		KeyHash: "hash-1",
		Scopes:  []string{"instances:create", "instances:read"},
	})
	if err != nil {
		t.Fatalf("create the key: %v", err)
	}
	if key.Revoked() {
		t.Error("a new key must not be revoked")
	}

	found, err := st.APIKeyByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("read the key: %v", err)
	}
	if len(found.Scopes) != 2 {
		t.Errorf("unexpected scopes: %v", found.Scopes)
	}
	if !found.Active(time.Now()) {
		t.Error("a new key must be active")
	}

	if err := st.RevokeAPIKey(ctx, "key_1"); err != nil {
		t.Fatalf("revoke the key: %v", err)
	}
	found, err = st.APIKeyByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("read the revoked key: %v", err)
	}
	if !found.Revoked() {
		t.Error("the key must be revoked")
	}
	if found.Active(time.Now()) {
		t.Error("a revoked key must not be active")
	}
}

func TestExpiredAPIKeyIsInactive(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateUser(ctx, User{ID: "usr_1", Username: "ada", PasswordHash: "h"}); err != nil {
		t.Fatalf("create the user: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := st.CreateAPIKey(ctx, APIKey{
		ID: "key_1", UserID: "usr_1", Name: "old", Prefix: "dak_old",
		KeyHash: "hash-1", ExpiresAt: &past,
	}); err != nil {
		t.Fatalf("create the key: %v", err)
	}
	found, err := st.APIKeyByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("read the key: %v", err)
	}
	if found.Active(time.Now()) {
		t.Error("an expired key must not be active")
	}
}

func TestSessionLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateUser(ctx, User{ID: "usr_1", Username: "ada", PasswordHash: "h"}); err != nil {
		t.Fatalf("create the user: %v", err)
	}
	expires := time.Now().Add(time.Hour)
	if _, err := st.CreateSession(ctx, Session{
		ID: "ses_1", UserID: "usr_1", TokenHash: "token-1", CSRFToken: "csrf-1",
		ExpiresAt: expires,
	}); err != nil {
		t.Fatalf("create the session: %v", err)
	}
	sess, err := st.SessionByTokenHash(ctx, "token-1")
	if err != nil {
		t.Fatalf("read the session: %v", err)
	}
	if sess.CSRFToken != "csrf-1" {
		t.Errorf("unexpected csrf token: %q", sess.CSRFToken)
	}
	if sess.Expired(time.Now()) {
		t.Error("the session must not be expired")
	}

	if err := st.DeleteSession(ctx, "ses_1"); err != nil {
		t.Fatalf("delete the session: %v", err)
	}
	if _, err := st.SessionByTokenHash(ctx, "token-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestJobLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	job, err := st.CreateJob(ctx, Job{
		ID: "job_1", UserID: "usr_1", Kind: JobKindInstanceCreate,
		Request: map[string]any{"template": "alpine-shell"},
	})
	if err != nil {
		t.Fatalf("create the job: %v", err)
	}
	if job.State != JobQueued {
		t.Errorf("unexpected initial state: %q", job.State)
	}

	got, err := st.JobByID(ctx, "job_1")
	if err != nil {
		t.Fatalf("read the job: %v", err)
	}
	if got.Request["template"] != "alpine-shell" {
		t.Errorf("the request was not preserved: %v", got.Request)
	}

	if err := st.UpdateJobState(ctx, "job_1", JobRunning, "", ""); err != nil {
		t.Fatalf("update the state: %v", err)
	}
	got, _ = st.JobByID(ctx, "job_1")
	if got.State != JobRunning {
		t.Errorf("unexpected state: %q", got.State)
	}
	if got.StartedAt == nil {
		t.Error("the start timestamp was not set")
	}

	if err := st.UpdateJobState(ctx, "job_1", JobFailed, "runtime_error", "boom"); err != nil {
		t.Fatalf("update the state: %v", err)
	}
	got, _ = st.JobByID(ctx, "job_1")
	if got.State != JobFailed || !got.State.Terminal() {
		t.Errorf("unexpected terminal state: %q", got.State)
	}
	if got.FinishedAt == nil {
		t.Error("the finish timestamp was not set")
	}
	if got.ErrorCode != "runtime_error" {
		t.Errorf("unexpected error code: %q", got.ErrorCode)
	}
}

func TestJobIdempotencyKeyIsUnique(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateJob(ctx, Job{ID: "job_1", UserID: "usr_1", IdempotencyKey: "k1"}); err != nil {
		t.Fatalf("create the job: %v", err)
	}
	if _, err := st.CreateJob(ctx, Job{ID: "job_2", UserID: "usr_1", IdempotencyKey: "k1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	// A different user may reuse the same key.
	if _, err := st.CreateJob(ctx, Job{ID: "job_3", UserID: "usr_2", IdempotencyKey: "k1"}); err != nil {
		t.Fatalf("create the job for another user: %v", err)
	}
	found, err := st.JobByIdempotencyKey(ctx, "usr_1", "k1")
	if err != nil {
		t.Fatalf("look the job up: %v", err)
	}
	if found.ID != "job_1" {
		t.Errorf("unexpected job: %q", found.ID)
	}
}

func TestInstanceLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	instance, err := st.CreateInstance(ctx, Instance{
		ID: "ins_1", UserID: "usr_1", Runtime: "docker", State: InstanceRequested,
		Resources: map[string]any{"cpu": 2.0},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create the instance: %v", err)
	}
	if instance.State != InstanceRequested {
		t.Errorf("unexpected initial state: %q", instance.State)
	}

	got, err := st.InstanceByID(ctx, "ins_1")
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if got.Resources["cpu"] != 2.0 {
		t.Errorf("the resources were not preserved: %v", got.Resources)
	}

	if err := st.UpdateInstanceState(ctx, "ins_1", InstanceReady); err != nil {
		t.Fatalf("update the state: %v", err)
	}
	got, _ = st.InstanceByID(ctx, "ins_1")
	if got.State != InstanceReady {
		t.Errorf("unexpected state: %q", got.State)
	}

	if n, err := st.CountLiveInstances(ctx); err != nil {
		t.Fatalf("count the instances: %v", err)
	} else if n != 1 {
		t.Errorf("unexpected live instance count: %d", n)
	}
}

func TestExpiredInstances(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateInstance(ctx, Instance{
		ID: "ins_old", UserID: "usr_1", Runtime: "docker",
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create the instance: %v", err)
	}
	if _, err := st.CreateInstance(ctx, Instance{
		ID: "ins_new", UserID: "usr_1", Runtime: "docker",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create the instance: %v", err)
	}

	expired, err := st.ExpiredInstances(ctx, time.Now())
	if err != nil {
		t.Fatalf("list the expired instances: %v", err)
	}
	if len(expired) != 1 || expired[0].ID != "ins_old" {
		t.Fatalf("unexpected expired instances: %+v", expired)
	}
}

func TestArtifactAndAudit(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	expires := time.Now().Add(time.Hour)
	if _, err := st.CreateArtifact(ctx, Artifact{
		ID: "art_1", UserID: "usr_1", Name: "out.txt", Path: "/tmp/out.txt",
		MIME: "text/plain", Size: 10, Digest: "abc", ExpiresAt: &expires,
	}); err != nil {
		t.Fatalf("create the artifact: %v", err)
	}
	got, err := st.ArtifactByID(ctx, "art_1")
	if err != nil {
		t.Fatalf("read the artifact: %v", err)
	}
	if got.State != ArtifactAvailable {
		t.Errorf("unexpected state: %q", got.State)
	}

	if _, err := st.AppendAuditEvent(ctx, AuditEvent{
		ID: "aud_1", ActorType: ActorUser, ActorID: "usr_1",
		Action: "instance.create", Decision: DecisionAllow,
	}); err != nil {
		t.Fatalf("append the audit event: %v", err)
	}
	events, err := st.ListAuditEvents(ctx, AuditFilter{ActorID: "usr_1"})
	if err != nil {
		t.Fatalf("list the audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("unexpected audit event count: %d", len(events))
	}
	if events[0].Decision != DecisionAllow {
		t.Errorf("unexpected decision: %q", events[0].Decision)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	st.Close()

	// Reopening must not reapply the migrations.
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen the store: %v", err)
	}
	defer st.Close()

	var count int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count the migrations: %v", err)
	}
	if count != 1 {
		t.Errorf("unexpected migration count: %d", count)
	}
}
