package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/store"
)

// TestReconcileFailsInterruptedJobs checks that a job that was still running
// when the daemon stopped is marked failed instead of waiting forever.
func TestReconcileFailsInterruptedJobs(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	if _, err := orch.Store().CreateJob(ctx, store.Job{
		ID: "job_interrupted", UserID: "usr_owner", Kind: store.JobKindInstanceCreate,
		State: store.JobRunning, TemplateID: "alpine-shell",
	}); err != nil {
		t.Fatalf("create the job: %v", err)
	}

	report, err := orch.ReconcileAfterRestart(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.FailedJobs != 1 {
		t.Errorf("failed jobs = %d, want 1", report.FailedJobs)
	}
	job, err := orch.Store().JobByID(ctx, "job_interrupted")
	if err != nil {
		t.Fatalf("read the job: %v", err)
	}
	if job.State != store.JobFailed {
		t.Errorf("job state = %q, want %q", job.State, store.JobFailed)
	}
	if job.ErrorCode != "daemon_restarted" {
		t.Errorf("error code = %q, want daemon_restarted", job.ErrorCode)
	}
	// A finished job is left alone.
	if _, err := orch.Store().CreateJob(ctx, store.Job{
		ID: "job_done", UserID: "usr_owner", Kind: store.JobKindInstanceCreate,
		State: store.JobCompleted, TemplateID: "alpine-shell",
	}); err != nil {
		t.Fatalf("create the job: %v", err)
	}
	report, err = orch.ReconcileAfterRestart(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.Empty() {
		t.Errorf("a clean node reported %v, want nothing to recover", report)
	}
}

// TestReconcileFailsAnInstanceWithoutAResource checks that an instance whose
// creation never reached the backend can never become ready and is marked
// failed.
func TestReconcileFailsAnInstanceWithoutAResource(t *testing.T) {
	orch, _ := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	if _, err := orch.Store().CreateInstance(ctx, store.Instance{
		ID: "ins_stuck", UserID: "usr_owner", Runtime: "fake",
		State: store.InstancePreparing, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create the instance: %v", err)
	}

	report, err := orch.ReconcileAfterRestart(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.FailedInstances != 1 {
		t.Errorf("failed instances = %d, want 1", report.FailedInstances)
	}
	instance, err := orch.Store().InstanceByID(ctx, "ins_stuck")
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if instance.State != store.InstanceFailed {
		t.Errorf("instance state = %q, want %q", instance.State, store.InstanceFailed)
	}
}

// TestReconcileAlignsAnInstanceWithItsBackend checks that an instance whose
// resource disappeared while the daemon was down is marked destroyed.
func TestReconcileAlignsAnInstanceWithItsBackend(t *testing.T) {
	orch, backend := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	if _, err := orch.Store().CreateInstance(ctx, store.Instance{
		ID: "ins_gone", UserID: "usr_owner", Runtime: "fake", RuntimeID: "fake-ins_gone",
		State: store.InstanceReady, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create the instance: %v", err)
	}
	// The backend lost the resource while the daemon was down: an inspect
	// of an unknown instance is an error, so the record is left untouched.
	if _, err := orch.ReconcileAfterRestart(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	instance, err := orch.Store().InstanceByID(ctx, "ins_gone")
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if instance.State != store.InstanceReady {
		t.Fatalf("an instance with an unreadable backend was changed: %q", instance.State)
	}

	// The resource is gone for good: the backend reports it as destroyed and
	// the record follows.
	backend.Destroy(ctx, "fake-ins_gone")
	report, err := orch.ReconcileAfterRestart(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.CorrectedInstances != 1 {
		t.Errorf("corrected instances = %d, want 1", report.CorrectedInstances)
	}
	instance, err = orch.Store().InstanceByID(ctx, "ins_gone")
	if err != nil {
		t.Fatalf("read the instance: %v", err)
	}
	if instance.State != store.InstanceDestroyed {
		t.Errorf("instance state = %q, want %q", instance.State, store.InstanceDestroyed)
	}
}

// TestReconcileKeepsARunningInstance checks that an instance that survived
// the restart in its backend keeps its ready state.
func TestReconcileKeepsARunningInstance(t *testing.T) {
	orch, backend := newOrchestrator(t, orchestrator.Limits{MaxInstances: 4, MaxInstancesPerUser: 4})
	ctx := context.Background()
	if _, err := orch.Store().CreateInstance(ctx, store.Instance{
		ID: "ins_live", UserID: "usr_owner", Runtime: "fake", RuntimeID: "fake-ins_live",
		State: store.InstanceReady, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create the instance: %v", err)
	}
	backend.Start(ctx, "fake-ins_live")

	report, err := orch.ReconcileAfterRestart(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.Empty() {
		t.Errorf("a healthy node reported %v, want nothing to recover", report)
	}
}
