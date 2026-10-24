package orchestrator_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
	"github.com/zarigata/double-agent/internal/templates"
)

// fakeBackend is a runtime backend that records the calls instead of running
// a workload. It keeps the tests of the control plane free of a container
// engine.
type fakeBackend struct {
	mu        sync.Mutex
	instances map[runtime.ID]runtime.State
	// prepareErr is returned by Prepare when it is set.
	prepareErr error
}

// newFakeBackend returns a backend that accepts every request.
func newFakeBackend() *fakeBackend {
	return &fakeBackend{instances: map[runtime.ID]runtime.State{}}
}

func (b *fakeBackend) Kind() runtime.Kind { return runtime.KindContainer }

func (b *fakeBackend) Name() string { return "fake" }

func (b *fakeBackend) Prepare(ctx context.Context, recipe runtime.Recipe) error {
	return b.prepareErr
}

func (b *fakeBackend) Create(ctx context.Context, spec runtime.InstanceSpec) (runtime.Instance, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.instances[spec.ID] = runtime.StateRequested
	return runtime.Instance{
		ID:      spec.ID,
		Backend: b.Name(),
		Runtime: runtime.ID("fake-" + string(spec.ID)),
		State:   runtime.StateRequested,
	}, nil
}

func (b *fakeBackend) Start(ctx context.Context, id runtime.ID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.instances[id] = runtime.StateReady
	return nil
}

func (b *fakeBackend) Exec(ctx context.Context, id runtime.ID, req runtime.ExecRequest) (runtime.ExecHandle, error) {
	done := make(chan runtime.ExecResult, 1)
	done <- runtime.ExecResult{ExitCode: 0, Stdout: []byte("ok"), Duration: time.Millisecond}
	return runtime.ExecHandle{ID: "exec-" + string(id), Done: done}, nil
}

func (b *fakeBackend) Console(ctx context.Context, id runtime.ID) (runtime.ConsoleEndpoint, error) {
	return runtime.ConsoleEndpoint{URL: "https://console.invalid/" + string(id), Kind: "vnc"}, nil
}

func (b *fakeBackend) CopyIn(ctx context.Context, id runtime.ID, src runtime.ArtifactRef, dst runtime.GuestPath) error {
	return nil
}

func (b *fakeBackend) CopyOut(ctx context.Context, id runtime.ID, src runtime.GuestPath) (runtime.ArtifactRef, error) {
	return runtime.ArtifactRef{}, runtime.ErrNotFound
}

func (b *fakeBackend) Stop(ctx context.Context, id runtime.ID, grace time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.instances[id] = runtime.StateDestroyed
	return nil
}

func (b *fakeBackend) Destroy(ctx context.Context, id runtime.ID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.instances[id] = runtime.StateDestroyed
	return nil
}

func (b *fakeBackend) Inspect(ctx context.Context, id runtime.ID) (runtime.ObservedState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.instances[id]
	if !ok {
		return runtime.ObservedState{}, runtime.ErrNotFound
	}
	return runtime.ObservedState{State: state, Started: time.Now()}, nil
}

func (b *fakeBackend) Health(ctx context.Context) error { return nil }

// owner is the actor that owns the resources of the tests.
func owner() policy.Actor {
	return policy.Actor{
		Type:   "user",
		ID:     "usr_owner",
		Scopes: policy.NewScopeSet(policy.DefaultUserScopes),
	}
}

// stranger is another actor with the same scopes.
func stranger() policy.Actor {
	return policy.Actor{
		Type:   "user",
		ID:     "usr_stranger",
		Scopes: policy.NewScopeSet(policy.DefaultUserScopes),
	}
}

// newOrchestrator returns an orchestrator backed by a temporary database and
// a fake backend.
func newOrchestrator(t *testing.T, limits orchestrator.Limits) (*orchestrator.Orchestrator, *fakeBackend) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "double-agent.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	// The creation of an instance runs in the background. The test waits for
	// the jobs to finish before the database is closed, otherwise the
	// background workflow writes into a closed database.
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			jobs, err := st.ListJobs(context.Background(), store.JobFilter{})
			if err != nil {
				return
			}
			pending := false
			for _, job := range jobs {
				if !job.State.Terminal() {
					pending = true
				}
			}
			if !pending {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})

	catalogue := templates.NewCatalogue()
	tmpl, err := templates.New(templates.Spec{
		ID:        "test-template",
		Version:   "1.0.0",
		Title:     "Test Template",
		Runtime:   runtime.KindContainer,
		Image:     "docker.io/library/alpine:3.20",
		Resources: runtime.Resources{CPU: 1, MemoryMB: 256},
		Network:   runtime.NetworkPolicy{Egress: "none"},
	})
	if err != nil {
		t.Fatalf("build the template: %v", err)
	}
	if err := catalogue.Publish(tmpl); err != nil {
		t.Fatalf("publish the template: %v", err)
	}

	backend := newFakeBackend()
	registry := runtime.NewRegistry()
	registry.Register(backend)
	registry.Default = backend.Name()

	orch, err := orchestrator.New(orchestrator.Options{
		Store:     st,
		Catalogue: catalogue,
		Runtimes:  registry,
		DataDir:   t.TempDir(),
		Limits:    limits,
	})
	if err != nil {
		t.Fatalf("create the orchestrator: %v", err)
	}
	return orch, backend
}

// waitForJobState waits until a job reaches a state.
func waitForJobState(t *testing.T, orch *orchestrator.Orchestrator, jobID string, want store.JobState) store.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last store.Job
	for time.Now().Before(deadline) {
		job, err := orch.Store().JobByID(context.Background(), jobID)
		if err != nil {
			t.Fatalf("read the job: %v", err)
		}
		last = job
		if job.State == want {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the job %q is in the state %q, want %q", jobID, last.State, want)
	return last
}

// TestCreateReturnsAQueuedJob checks the answer of a create request.
func TestCreateReturnsAQueuedJob(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()

	result, err := orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      owner(),
		TemplateID: "test-template",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if result.Job.State != store.JobQueued {
		t.Errorf("the job is in the state %q, want %q", result.Job.State, store.JobQueued)
	}
	if result.Job.ID == "" {
		t.Fatal("the job has no identifier")
	}
	if result.Job.UserID != "usr_owner" {
		t.Errorf("the job belongs to %q, want usr_owner", result.Job.UserID)
	}
	if result.Job.TemplateID != "test-template" {
		t.Errorf("the job uses the template %q", result.Job.TemplateID)
	}
	if result.Job.InstanceID == "" {
		t.Error("the job has no instance")
	}
	if result.Replayed {
		t.Error("a fresh request was reported as a replay")
	}
	// The job is stored before the answer is sent.
	if _, err := orch.Store().JobByID(ctx, result.Job.ID); err != nil {
		t.Errorf("the job was not stored: %v", err)
	}
}

// TestTheJobReachesATerminalState checks the lifecycle of a job.
func TestTheJobReachesATerminalState(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()

	result, err := orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      owner(),
		TemplateID: "test-template",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	job := waitForJobState(t, orch, result.Job.ID, store.JobCompleted)
	if !job.State.Terminal() {
		t.Errorf("the state %q is not terminal", job.State)
	}
	if job.FinishedAt == nil {
		t.Error("the finished job carries no finish time")
	}
	// The instance of the job is ready.
	instance, err := orch.Store().InstanceByID(ctx, result.Job.InstanceID)
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if instance.State != store.InstanceReady {
		t.Errorf("the instance is in the state %q, want %q", instance.State, store.InstanceReady)
	}
	if instance.UserID != "usr_owner" {
		t.Errorf("the instance belongs to %q, want usr_owner", instance.UserID)
	}
}

// TestTheIdempotencyKeyReturnsTheSameJob checks that a replayed request does
// not start a second job.
func TestTheIdempotencyKeyReturnsTheSameJob(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	request := orchestrator.CreateRequest{
		Actor:          owner(),
		TemplateID:     "test-template",
		IdempotencyKey: "the-key",
	}

	first, err := orch.Create(ctx, request)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := orch.Create(ctx, request)
	if err != nil {
		t.Fatalf("create again: %v", err)
	}
	if second.Job.ID != first.Job.ID {
		t.Errorf("the replay returned the job %q, want %q", second.Job.ID, first.Job.ID)
	}
	if !second.Replayed {
		t.Error("the replay was not reported as such")
	}
	if first.Replayed {
		t.Error("the first request was reported as a replay")
	}
	// Only one job was created.
	jobs, err := orch.Store().ListJobs(ctx, store.JobFilter{UserID: "usr_owner"})
	if err != nil {
		t.Fatalf("list the jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Errorf("the user owns %d jobs, want 1", len(jobs))
	}
}

// TestTheInstanceQuotaIsEnforced checks that a node does not host more
// instances than it is allowed to.
func TestTheInstanceQuotaIsEnforced(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 1, MaxInstancesPerUser: 4})
	ctx := context.Background()

	first, err := orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      owner(),
		TemplateID: "test-template",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The instance has to exist before the quota is checked.
	waitForJobState(t, orch, first.Job.ID, store.JobCompleted)

	_, err = orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      owner(),
		TemplateID: "test-template",
	})
	if !errors.Is(err, orchestrator.ErrQuotaExceeded) {
		t.Fatalf("the second instance returned %v, want a quota that is exceeded", err)
	}
}

// TestTheUserQuotaIsEnforced checks the quota of a single account.
func TestTheUserQuotaIsEnforced(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 8, MaxInstancesPerUser: 1})
	ctx := context.Background()

	first, err := orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      owner(),
		TemplateID: "test-template",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitForJobState(t, orch, first.Job.ID, store.JobCompleted)

	_, err = orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      owner(),
		TemplateID: "test-template",
	})
	if !errors.Is(err, orchestrator.ErrQuotaExceeded) {
		t.Fatalf("the second instance returned %v, want a quota that is exceeded", err)
	}
}

// TestANonOwnerCannotOperateOnAnInstance checks the ownership rule of the
// instance operations.
func TestANonOwnerCannotOperateOnAnInstance(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()

	result, err := orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      owner(),
		TemplateID: "test-template",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitForJobState(t, orch, result.Job.ID, store.JobCompleted)

	// Another account cannot run a command in the instance.
	_, err = orch.Exec(ctx, orchestrator.ExecRequest{
		Actor:      stranger(),
		InstanceID: result.Job.InstanceID,
		Argv:       []string{"echo", "hello"},
	})
	if !errors.Is(err, orchestrator.ErrPolicyDenied) {
		t.Fatalf("the exec of a stranger returned %v, want a denied request", err)
	}
	// It cannot stop it either.
	if err := orch.Stop(ctx, stranger(), result.Job.InstanceID, time.Second, ""); !errors.Is(err, orchestrator.ErrPolicyDenied) {
		t.Fatalf("the stop of a stranger returned %v, want a denied request", err)
	}
	// The owner can.
	response, err := orch.Exec(ctx, orchestrator.ExecRequest{
		Actor:      owner(),
		InstanceID: result.Job.InstanceID,
		Argv:       []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("the exec of the owner returned %v", err)
	}
	if response.ExitCode != 0 {
		t.Errorf("the command exited with %d, want 0", response.ExitCode)
	}
}

// TestAnActorWithoutTheScopeIsRefused checks that the scope of the actor is
// enforced.
func TestAnActorWithoutTheScopeIsRefused(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()

	actor := owner()
	actor.Scopes = policy.NewScopeSet([]policy.Scope{policy.ScopeCapabilitiesRead})
	_, err := orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      actor,
		TemplateID: "test-template",
	})
	if !errors.Is(err, orchestrator.ErrPolicyDenied) {
		t.Fatalf("the request returned %v, want a denied request", err)
	}
}

// TestAnUnknownTemplateIsRefused checks the lookup of a template.
func TestAnUnknownTemplateIsRefused(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	_, err := orch.Create(context.Background(), orchestrator.CreateRequest{
		Actor:      owner(),
		TemplateID: "missing-template",
	})
	if !errors.Is(err, orchestrator.ErrTemplateNotFound) {
		t.Fatalf("the request returned %v, want a missing template", err)
	}
}

// TestTheRequestIsAudited checks that a decision leaves a record.
func TestTheRequestIsAudited(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()

	result, err := orch.Create(ctx, orchestrator.CreateRequest{
		Actor:         owner(),
		TemplateID:    "test-template",
		CorrelationID: "corr-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	events, err := orch.Store().ListAuditEvents(ctx, store.AuditFilter{ActorID: "usr_owner"})
	if err != nil {
		t.Fatalf("list the audit events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("the request was not audited")
	}
	found := false
	for _, event := range events {
		if event.Action == "instance.create" && event.Decision == store.DecisionAllow {
			found = true
			if event.CorrelationID != "corr-1" {
				t.Errorf("the audit event carries the correlation id %q", event.CorrelationID)
			}
		}
	}
	if !found {
		t.Errorf("the audit log holds no allowed create: %+v", events)
	}
	_ = result
}

// readyInstance creates an instance and waits until it is ready.
func readyInstance(t *testing.T, orch *orchestrator.Orchestrator, actor policy.Actor) store.Instance {
	t.Helper()
	ctx := context.Background()
	result, err := orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      actor,
		TemplateID: "test-template",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitForJobState(t, orch, result.Job.ID, store.JobCompleted)
	instance, err := orch.Store().InstanceByID(ctx, result.Job.InstanceID)
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	return instance
}

// TestAnInstanceCanBeStoppedAndDestroyed checks the end of the life of an
// instance.
func TestAnInstanceCanBeStoppedAndDestroyed(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	instance := readyInstance(t, orch, owner())

	if err := orch.Stop(ctx, owner(), instance.ID, time.Second, ""); err != nil {
		t.Fatalf("stop the instance: %v", err)
	}
	stopped, err := orch.Store().InstanceByID(ctx, instance.ID)
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if stopped.State != store.InstanceDestroyed {
		t.Errorf("the instance is in the state %q, want %q", stopped.State, store.InstanceDestroyed)
	}
	if !stopped.State.Terminal() {
		t.Error("the state of a stopped instance is not terminal")
	}
}

// TestTheLifetimeOfAnInstanceCanBeExtended checks the extension of the
// lifetime of an instance.
func TestTheLifetimeOfAnInstanceCanBeExtended(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4, MaxTTL: 4 * time.Hour})
	ctx := context.Background()
	instance := readyInstance(t, orch, owner())

	expiry, err := orch.ExtendTTL(ctx, owner(), instance.ID, 2*time.Hour, "")
	if err != nil {
		t.Fatalf("extend the lifetime: %v", err)
	}
	if !expiry.After(instance.ExpiresAt) {
		t.Errorf("the new deadline %s is not after the old one %s", expiry, instance.ExpiresAt)
	}
	extended, err := orch.Store().InstanceByID(ctx, instance.ID)
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if !extended.ExpiresAt.Equal(expiry) {
		t.Errorf("the instance expires at %s, want %s", extended.ExpiresAt, expiry)
	}
}

// TestAnAdministratorMayOperateOnAnotherInstance checks that an
// administrator is not bound by the ownership of a resource.
func TestAnAdministratorMayOperateOnAnotherInstance(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	instance := readyInstance(t, orch, owner())

	admin := policy.Actor{
		Type:    "user",
		ID:      "usr_admin",
		IsAdmin: true,
		Scopes:  policy.NewScopeSet(policy.AdminScopes),
	}
	if _, err := orch.Exec(ctx, orchestrator.ExecRequest{
		Actor:      admin,
		InstanceID: instance.ID,
		Argv:       []string{"echo", "hello"},
	}); err != nil {
		t.Fatalf("the exec of an administrator returned %v", err)
	}
}

// TestAJobCanBeCanceled checks the cancellation of a job that did not finish
// yet.
func TestAJobCanBeCanceled(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()

	job, err := orch.Store().CreateJob(ctx, store.Job{
		ID:     "job_pending",
		UserID: "usr_owner",
		Kind:   store.JobKindInstanceCreate,
		State:  store.JobQueued,
	})
	if err != nil {
		t.Fatalf("create the job: %v", err)
	}
	if err := orch.CancelJob(ctx, owner(), job.ID, ""); err != nil {
		t.Fatalf("cancel the job: %v", err)
	}
	canceled, err := orch.Store().JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("read the job: %v", err)
	}
	if canceled.State != store.JobCanceled {
		t.Errorf("the job is in the state %q, want %q", canceled.State, store.JobCanceled)
	}
	// A job that already finished cannot be canceled.
	if err := orch.CancelJob(ctx, owner(), job.ID, ""); !errors.Is(err, orchestrator.ErrConflict) {
		t.Errorf("the second cancellation returned %v, want a conflict", err)
	}
}

// TestAnotherAccountCannotCancelAJob checks the ownership of a job.
func TestAnotherAccountCannotCancelAJob(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()

	job, err := orch.Store().CreateJob(ctx, store.Job{
		ID:     "job_pending",
		UserID: "usr_owner",
		Kind:   store.JobKindInstanceCreate,
		State:  store.JobQueued,
	})
	if err != nil {
		t.Fatalf("create the job: %v", err)
	}
	if err := orch.CancelJob(ctx, stranger(), job.ID, ""); !errors.Is(err, orchestrator.ErrPolicyDenied) {
		t.Errorf("the cancellation returned %v, want a denied request", err)
	}
	// The job of the owner is closed so that the node has nothing pending
	// when the test ends.
	if err := orch.Store().UpdateJobState(ctx, job.ID, store.JobCanceled, "", ""); err != nil {
		t.Fatalf("close the job: %v", err)
	}
}

// TestTheExpiredInstancesAreReaped checks the maintenance of the node.
func TestTheExpiredInstancesAreReaped(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()

	if _, err := orch.Store().CreateInstance(ctx, store.Instance{
		ID:        "ins_expired",
		UserID:    "usr_owner",
		Runtime:   "fake",
		State:     store.InstanceReady,
		ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("create the instance: %v", err)
	}
	reaped, err := orch.ReapExpired(ctx)
	if err != nil {
		t.Fatalf("reap the instances: %v", err)
	}
	if reaped != 1 {
		t.Errorf("the node reaped %d instances, want 1", reaped)
	}
	instance, err := orch.Store().InstanceByID(ctx, "ins_expired")
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if instance.State != store.InstanceDestroyed {
		t.Errorf("the instance is in the state %q, want %q", instance.State, store.InstanceDestroyed)
	}
}

// TestAnInstanceCanBeDestroyed checks the removal of an instance.
func TestAnInstanceCanBeDestroyed(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	instance := readyInstance(t, orch, owner())

	if err := orch.Destroy(ctx, owner(), instance.ID, ""); err != nil {
		t.Fatalf("destroy the instance: %v", err)
	}
	destroyed, err := orch.Store().InstanceByID(ctx, instance.ID)
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if destroyed.State != store.InstanceDestroyed {
		t.Errorf("the instance is in the state %q, want %q", destroyed.State, store.InstanceDestroyed)
	}
}

// TestTheConsoleOfAnInstance checks the console endpoint of an instance.
func TestTheConsoleOfAnInstance(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	instance := readyInstance(t, orch, owner())

	endpoint, err := orch.Console(ctx, owner(), instance.ID, "")
	if err != nil {
		t.Fatalf("the console returned %v", err)
	}
	if endpoint.URL == "" {
		t.Error("the console endpoint carries no url")
	}
	if endpoint.Kind == "" {
		t.Error("the console endpoint carries no kind")
	}
}

// TestAFailingBackendFailsTheJob checks that a backend that cannot work
// ends the job with a failure.
func TestAFailingBackendFailsTheJob(t *testing.T) {
	orch, backend := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	backend.prepareErr = errors.New("the backend is not available")

	result, err := orch.Create(ctx, orchestrator.CreateRequest{
		Actor:      owner(),
		TemplateID: "test-template",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	job := waitForJobState(t, orch, result.Job.ID, store.JobFailed)
	if !job.State.Terminal() {
		t.Errorf("the state %q is not terminal", job.State)
	}
	if job.ErrorCode == "" {
		t.Error("the failed job carries no error code")
	}
	if job.ErrorMessage == "" {
		t.Error("the failed job carries no error message")
	}
}
