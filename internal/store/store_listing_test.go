package store

import (
	"context"
	"testing"
	"time"
)

// newListingStore opens a store in a temporary directory.
func newListingStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/listing.db")
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestListJobsFiltersByUserAndState checks the job listing.
func TestListJobsFiltersByUserAndState(t *testing.T) {
	st := newListingStore(t)
	ctx := context.Background()

	for _, job := range []Job{
		{ID: "job_1", UserID: "usr_1", Kind: JobKindInstanceCreate, State: JobCompleted},
		{ID: "job_2", UserID: "usr_1", Kind: JobKindInstanceCreate, State: JobFailed},
		{ID: "job_3", UserID: "usr_2", Kind: JobKindInstanceExec, State: JobCompleted},
	} {
		if _, err := st.CreateJob(ctx, job); err != nil {
			t.Fatalf("create the job %q: %v", job.ID, err)
		}
	}

	all, err := st.ListJobs(ctx, JobFilter{})
	if err != nil {
		t.Fatalf("list the jobs: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("the listing holds %d jobs, want 3", len(all))
	}

	mine, err := st.ListJobs(ctx, JobFilter{UserID: "usr_1"})
	if err != nil {
		t.Fatalf("list the jobs: %v", err)
	}
	if len(mine) != 2 {
		t.Errorf("the listing holds %d jobs, want 2", len(mine))
	}
	for _, job := range mine {
		if job.UserID != "usr_1" {
			t.Errorf("the listing holds the job of %q", job.UserID)
		}
	}

	completed, err := st.ListJobs(ctx, JobFilter{States: []JobState{JobCompleted}})
	if err != nil {
		t.Fatalf("list the jobs: %v", err)
	}
	if len(completed) != 2 {
		t.Errorf("the listing holds %d completed jobs, want 2", len(completed))
	}

	limited, err := st.ListJobs(ctx, JobFilter{Limit: 1})
	if err != nil {
		t.Fatalf("list the jobs: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("the listing holds %d jobs, want 1", len(limited))
	}
}

// TestCountJobsByState checks the job counters.
func TestCountJobsByState(t *testing.T) {
	st := newListingStore(t)
	ctx := context.Background()

	for _, job := range []Job{
		{ID: "job_1", UserID: "usr_1", Kind: JobKindInstanceCreate, State: JobCompleted},
		{ID: "job_2", UserID: "usr_1", Kind: JobKindInstanceCreate, State: JobCompleted},
		{ID: "job_3", UserID: "usr_1", Kind: JobKindInstanceCreate, State: JobFailed},
	} {
		if _, err := st.CreateJob(ctx, job); err != nil {
			t.Fatalf("create the job %q: %v", job.ID, err)
		}
	}
	counts, err := st.CountJobsByState(ctx)
	if err != nil {
		t.Fatalf("count the jobs: %v", err)
	}
	if counts[JobCompleted] != 2 {
		t.Errorf("the store holds %d completed jobs, want 2", counts[JobCompleted])
	}
	if counts[JobFailed] != 1 {
		t.Errorf("the store holds %d failed jobs, want 1", counts[JobFailed])
	}
}

// TestListInstancesFiltersByUserAndState checks the instance listing.
func TestListInstancesFiltersByUserAndState(t *testing.T) {
	st := newListingStore(t)
	ctx := context.Background()

	for _, instance := range []Instance{
		{ID: "ins_1", UserID: "usr_1", Runtime: "docker", State: InstanceReady, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "ins_2", UserID: "usr_1", Runtime: "docker", State: InstanceDestroyed, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "ins_3", UserID: "usr_2", Runtime: "docker", State: InstanceReady, ExpiresAt: time.Now().Add(time.Hour)},
	} {
		if _, err := st.CreateInstance(ctx, instance); err != nil {
			t.Fatalf("create the instance %q: %v", instance.ID, err)
		}
	}

	all, err := st.ListInstances(ctx, "", nil)
	if err != nil {
		t.Fatalf("list the instances: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("the listing holds %d instances, want 3", len(all))
	}

	mine, err := st.ListInstances(ctx, "usr_1", nil)
	if err != nil {
		t.Fatalf("list the instances: %v", err)
	}
	if len(mine) != 2 {
		t.Errorf("the listing holds %d instances, want 2", len(mine))
	}

	ready, err := st.ListInstances(ctx, "usr_1", []InstanceState{InstanceReady})
	if err != nil {
		t.Fatalf("list the instances: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "ins_1" {
		t.Errorf("the listing holds %+v, want the instance ins_1", ready)
	}
}

// TestListArtifactsFiltersByUser checks the artifact listing.
func TestListArtifactsFiltersByUser(t *testing.T) {
	st := newListingStore(t)
	ctx := context.Background()

	for _, artifact := range []Artifact{
		{ID: "art_1", UserID: "usr_1", Name: "one.txt", Path: "/tmp/one.txt", MIME: "text/plain", Size: 3, Digest: "a"},
		{ID: "art_2", UserID: "usr_1", Name: "two.txt", Path: "/tmp/two.txt", MIME: "text/plain", Size: 3, Digest: "b"},
		{ID: "art_3", UserID: "usr_2", Name: "other.txt", Path: "/tmp/other.txt", MIME: "text/plain", Size: 5, Digest: "c"},
	} {
		if _, err := st.CreateArtifact(ctx, artifact); err != nil {
			t.Fatalf("create the artifact %q: %v", artifact.ID, err)
		}
	}

	all, err := st.ListArtifacts(ctx, "", 0)
	if err != nil {
		t.Fatalf("list the artifacts: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("the listing holds %d artifacts, want 3", len(all))
	}

	mine, err := st.ListArtifacts(ctx, "usr_1", 0)
	if err != nil {
		t.Fatalf("list the artifacts: %v", err)
	}
	if len(mine) != 2 {
		t.Errorf("the listing holds %d artifacts, want 2", len(mine))
	}
	for _, artifact := range mine {
		if artifact.UserID != "usr_1" {
			t.Errorf("the listing holds the artifact of %q", artifact.UserID)
		}
	}

	limited, err := st.ListArtifacts(ctx, "usr_1", 1)
	if err != nil {
		t.Fatalf("list the artifacts: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("the listing holds %d artifacts, want 1", len(limited))
	}
}

// TestListAuditEventsFiltersByActorAndAction checks the audit listing.
func TestListAuditEventsFiltersByActorAndAction(t *testing.T) {
	st := newListingStore(t)
	ctx := context.Background()

	for _, event := range []AuditEvent{
		{ID: "aud_1", ActorType: ActorUser, ActorID: "usr_1", Action: "instance.create", Decision: DecisionAllow},
		{ID: "aud_2", ActorType: ActorUser, ActorID: "usr_1", Action: "instance.destroy", Decision: DecisionDeny},
		{ID: "aud_3", ActorType: ActorUser, ActorID: "usr_2", Action: "instance.create", Decision: DecisionAllow},
	} {
		if _, err := st.AppendAuditEvent(ctx, event); err != nil {
			t.Fatalf("append the event %q: %v", event.ID, err)
		}
	}

	all, err := st.ListAuditEvents(ctx, AuditFilter{})
	if err != nil {
		t.Fatalf("list the audit events: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("the listing holds %d events, want 3", len(all))
	}

	mine, err := st.ListAuditEvents(ctx, AuditFilter{ActorID: "usr_1"})
	if err != nil {
		t.Fatalf("list the audit events: %v", err)
	}
	if len(mine) != 2 {
		t.Errorf("the listing holds %d events, want 2", len(mine))
	}

	creates, err := st.ListAuditEvents(ctx, AuditFilter{Action: "instance.create"})
	if err != nil {
		t.Fatalf("list the audit events: %v", err)
	}
	if len(creates) != 2 {
		t.Errorf("the listing holds %d events, want 2", len(creates))
	}

	count, err := st.CountAuditEvents(ctx)
	if err != nil {
		t.Fatalf("count the audit events: %v", err)
	}
	if count != 3 {
		t.Errorf("the store holds %d events, want 3", count)
	}
}

// TestListUsersAndKeys checks the listings of the accounts and their keys.
func TestListUsersAndKeys(t *testing.T) {
	st := newListingStore(t)
	ctx := context.Background()

	for _, user := range []User{
		{ID: "usr_1", Username: "zoe", PasswordHash: "h"},
		{ID: "usr_2", Username: "ada", PasswordHash: "h"},
	} {
		if _, err := st.CreateUser(ctx, user); err != nil {
			t.Fatalf("create the user %q: %v", user.Username, err)
		}
	}
	users, err := st.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list the users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("the listing holds %d users, want 2", len(users))
	}
	if users[0].Username != "ada" {
		t.Errorf("the listing is not sorted by name: %q", users[0].Username)
	}

	for _, key := range []APIKey{
		{ID: "key_1", UserID: "usr_1", Name: "cli", KeyHash: "h1", Scopes: []string{"instances:read"}},
		{ID: "key_2", UserID: "usr_2", Name: "cli", KeyHash: "h2", Scopes: []string{"instances:read"}},
	} {
		if _, err := st.CreateAPIKey(ctx, key); err != nil {
			t.Fatalf("create the key %q: %v", key.ID, err)
		}
	}
	keys, err := st.ListAPIKeys(ctx, "usr_1")
	if err != nil {
		t.Fatalf("list the keys: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != "key_1" {
		t.Errorf("the listing holds %+v, want the key key_1", keys)
	}
	all, err := st.ListAPIKeys(ctx, "")
	if err != nil {
		t.Fatalf("list the keys: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("the listing holds %d keys, want 2", len(all))
	}
}
