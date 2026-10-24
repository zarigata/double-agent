package orchestrator

import (
	"context"
	"fmt"

	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
)

// ReconcileReport is the outcome of one reconciliation pass after a restart.
type ReconcileReport struct {
	// FailedJobs counts the jobs that the restart interrupted and that are
	// now marked failed.
	FailedJobs int
	// FailedInstances counts the instances that never received a backend
	// resource and can therefore never become ready.
	FailedInstances int
	// CorrectedInstances counts the instances whose recorded state was
	// aligned with the state the backend reports.
	CorrectedInstances int
}

// Empty reports whether the pass found nothing to recover.
func (r ReconcileReport) Empty() bool {
	return r.FailedJobs == 0 && r.FailedInstances == 0 && r.CorrectedInstances == 0
}

// ReconcileAfterRestart recovers the state a restart interrupted.
//
// A job runs inside the process of the daemon, so a job that was queued,
// preparing, running or exporting when the daemon stopped is orphaned: no
// process will ever finish it. It is marked failed with the code
// daemon_restarted, which lets a client retry through the idempotency key.
//
// The instances of those jobs live on in their backends. Their recorded state
// is aligned with the state the backend reports: an instance whose resource
// is gone is marked destroyed or failed, and an instance that is in fact
// running keeps working. An instance that never received a backend resource
// can never become ready and is marked failed.
func (o *Orchestrator) ReconcileAfterRestart(ctx context.Context) (ReconcileReport, error) {
	report := ReconcileReport{}

	// Every job that is not terminal at startup is orphaned, because no job
	// of a previous process is running any more.
	jobs, err := o.store.ListJobs(ctx, store.JobFilter{
		States: []store.JobState{store.JobQueued, store.JobPreparing, store.JobRunning, store.JobExporting},
	})
	if err != nil {
		return report, fmt.Errorf("orchestrator: list interrupted jobs: %w", err)
	}
	for _, job := range jobs {
		if err := o.store.UpdateJobState(ctx, job.ID, store.JobFailed,
			"daemon_restarted", "the daemon restarted while the job was running"); err != nil {
			o.log.Error("failed to reconcile an interrupted job", "job", job.ID, "error", err)
			continue
		}
		o.log.Warn("marked an interrupted job failed", "job", job.ID, "kind", job.Kind)
		report.FailedJobs++
	}

	instances, err := o.store.ListInstances(ctx, "", []store.InstanceState{
		store.InstanceRequested, store.InstancePreparing, store.InstanceStarting,
		store.InstanceReady, store.InstanceStopping,
	})
	if err != nil {
		return report, fmt.Errorf("orchestrator: list live instances: %w", err)
	}
	for _, instance := range instances {
		if instance.RuntimeID == "" {
			// The creation never handed the instance to a backend, so no
			// backend can complete it.
			if err := o.store.UpdateInstanceState(ctx, instance.ID, store.InstanceFailed); err != nil {
				o.log.Error("failed to reconcile an instance", "instance", instance.ID, "error", err)
				continue
			}
			o.log.Warn("marked an instance failed that never reached a backend", "instance", instance.ID)
			report.FailedInstances++
			continue
		}
		backend, ok := o.runtimes.Get(instance.Runtime)
		if !ok {
			// The backend of the instance is not registered any more; the
			// operator changed the configuration. The record is left alone.
			o.log.Warn("the runtime of an instance is not registered", "instance", instance.ID, "runtime", instance.Runtime)
			continue
		}
		observed, err := backend.Inspect(ctx, runtime.ID(instance.RuntimeID))
		if err != nil {
			o.log.Warn("could not inspect an instance after the restart", "instance", instance.ID, "error", err)
			continue
		}
		corrected, err := o.alignInstanceState(ctx, instance, observed.State)
		if err != nil {
			continue
		}
		report.CorrectedInstances += corrected
	}
	return report, nil
}

// alignInstanceState moves an instance whose recorded state disagrees with
// the backend onto the state the backend reports.
//
// Transient backend states change nothing: the instance is on its way and the
// next observation or the TTL reaper settles it.
func (o *Orchestrator) alignInstanceState(ctx context.Context, instance store.Instance, observed runtime.State) (int, error) {
	target := store.InstanceState("")
	switch observed {
	case runtime.StateReady:
		target = store.InstanceReady
	case runtime.StateDestroyed:
		target = store.InstanceDestroyed
	case runtime.StateFailed:
		target = store.InstanceFailed
	default:
		return 0, nil
	}
	if store.InstanceState(instance.State) == target {
		return 0, nil
	}
	if err := o.store.UpdateInstanceState(ctx, instance.ID, target); err != nil {
		o.log.Error("failed to reconcile an instance", "instance", instance.ID, "error", err)
		return 0, err
	}
	if target == store.InstanceDestroyed {
		o.removeWorkspace(instance.ID)
	}
	o.log.Info("aligned an instance with its backend", "instance", instance.ID, "state", target)
	return 1, nil
}
