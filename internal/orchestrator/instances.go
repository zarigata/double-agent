package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
)

// ExecRequest is the input of an exec request against a running instance.
type ExecRequest struct {
	// Actor is the authenticated caller.
	Actor policy.Actor
	// InstanceID selects the instance.
	InstanceID string
	// Argv is the command to run. It is never passed through a shell.
	Argv []string
	// Cwd is the working directory inside the instance.
	Cwd string
	// Env holds additional environment variables.
	Env map[string]string
	// Timeout bounds the execution.
	Timeout time.Duration
	// MaxOutputBytes caps the captured output.
	MaxOutputBytes int64
	// CorrelationID ties audit records together.
	CorrelationID string
}

// ExecResponse is the outcome of an exec request.
type ExecResponse struct {
	// ExitCode is the exit status of the command.
	ExitCode int
	// Stdout is the captured standard output.
	Stdout []byte
	// Stderr is the captured standard error.
	Stderr []byte
	// Truncated reports that the output hit the size cap.
	Truncated bool
	// Duration is the wall clock time of the execution.
	Duration time.Duration
}

// Exec runs a command inside a running instance.
func (o *Orchestrator) Exec(ctx context.Context, req ExecRequest) (ExecResponse, error) {
	if len(req.Argv) == 0 {
		return ExecResponse{}, errors.New("orchestrator: exec requires a command")
	}
	decision := policy.Evaluate(req.Actor, policy.ScopeInstancesExec)
	if !decision.Allowed {
		o.audit(ctx, req.Actor, "instance.exec", "deny", req.CorrelationID, decision.Reason)
		return ExecResponse{}, fmt.Errorf("%w: %s", ErrPolicyDenied, decision.Reason)
	}

	instance, backend, err := o.resolveInstance(ctx, req.Actor, req.InstanceID, policy.ScopeInstancesExec)
	if err != nil {
		return ExecResponse{}, err
	}
	if instance.State.Terminal() {
		return ExecResponse{}, fmt.Errorf("%w: instance %s is %s", ErrInstanceNotFound, instance.ID, instance.State)
	}

	execReq := runtime.ExecRequest{
		Argv:           req.Argv,
		Cwd:            req.Cwd,
		Env:            req.Env,
		Timeout:        req.Timeout,
		MaxOutputBytes: req.MaxOutputBytes,
	}
	handle, err := backend.Exec(ctx, runtime.ID(instance.RuntimeID), execReq)
	if err != nil {
		o.audit(ctx, req.Actor, "instance.exec", "allow", req.CorrelationID, err.Error())
		return ExecResponse{}, fmt.Errorf("orchestrator: exec failed: %w", err)
	}
	result := <-handle.Done
	o.audit(ctx, req.Actor, "instance.exec", "allow", req.CorrelationID, "")
	return ExecResponse{
		ExitCode:  result.ExitCode,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		Truncated: result.Truncated,
		Duration:  result.Duration,
	}, nil
}

// Stop stops a running instance, giving it a grace period to shut down.
func (o *Orchestrator) Stop(ctx context.Context, actor policy.Actor, instanceID string, grace time.Duration, correlationID string) error {
	decision := policy.Evaluate(actor, policy.ScopeInstancesStop)
	if !decision.Allowed {
		o.audit(ctx, actor, "instance.stop", "deny", correlationID, decision.Reason)
		return fmt.Errorf("%w: %s", ErrPolicyDenied, decision.Reason)
	}
	instance, backend, err := o.resolveInstance(ctx, actor, instanceID, policy.ScopeInstancesStop)
	if err != nil {
		return err
	}
	if instance.State.Terminal() {
		return nil
	}
	if grace <= 0 {
		grace = 10 * time.Second
	}
	if err := o.store.UpdateInstanceState(ctx, instance.ID, store.InstanceStopping); err != nil {
		return fmt.Errorf("orchestrator: mark instance stopping: %w", err)
	}
	if err := backend.Stop(ctx, runtime.ID(instance.RuntimeID), grace); err != nil {
		return fmt.Errorf("orchestrator: stop instance: %w", err)
	}
	if err := o.store.UpdateInstanceState(ctx, instance.ID, store.InstanceDestroyed); err != nil {
		return fmt.Errorf("orchestrator: mark instance destroyed: %w", err)
	}
	o.audit(ctx, actor, "instance.stop", "allow", correlationID, "")
	return nil
}

// Destroy stops and removes an instance together with its workspace.
func (o *Orchestrator) Destroy(ctx context.Context, actor policy.Actor, instanceID string, correlationID string) error {
	decision := policy.Evaluate(actor, policy.ScopeInstancesDestroy)
	if !decision.Allowed {
		o.audit(ctx, actor, "instance.destroy", "deny", correlationID, decision.Reason)
		return fmt.Errorf("%w: %s", ErrPolicyDenied, decision.Reason)
	}
	instance, backend, err := o.resolveInstance(ctx, actor, instanceID, policy.ScopeInstancesDestroy)
	if err != nil {
		return err
	}
	if !instance.State.Terminal() && instance.RuntimeID != "" {
		// A best effort stop keeps the backend tidy; the destroy below is
		// what actually guarantees the resources are released.
		_ = backend.Stop(ctx, runtime.ID(instance.RuntimeID), 5*time.Second)
	}
	if instance.RuntimeID != "" {
		if err := backend.Destroy(ctx, runtime.ID(instance.RuntimeID)); err != nil {
			return fmt.Errorf("orchestrator: destroy instance: %w", err)
		}
	}
	if err := o.store.UpdateInstanceState(ctx, instance.ID, store.InstanceDestroyed); err != nil {
		return fmt.Errorf("orchestrator: mark instance destroyed: %w", err)
	}
	o.removeWorkspace(instance.ID)
	o.audit(ctx, actor, "instance.destroy", "allow", correlationID, "")
	return nil
}

// Console returns a short lived console endpoint for an instance.
func (o *Orchestrator) Console(ctx context.Context, actor policy.Actor, instanceID string, correlationID string) (runtime.ConsoleEndpoint, error) {
	decision := policy.Evaluate(actor, policy.ScopeInstancesRead)
	if !decision.Allowed {
		o.audit(ctx, actor, "instance.console", "deny", correlationID, decision.Reason)
		return runtime.ConsoleEndpoint{}, fmt.Errorf("%w: %s", ErrPolicyDenied, decision.Reason)
	}
	instance, backend, err := o.resolveInstance(ctx, actor, instanceID, policy.ScopeInstancesRead)
	if err != nil {
		return runtime.ConsoleEndpoint{}, err
	}
	endpoint, err := backend.Console(ctx, runtime.ID(instance.RuntimeID))
	if err != nil {
		return runtime.ConsoleEndpoint{}, fmt.Errorf("orchestrator: console: %w", err)
	}
	o.audit(ctx, actor, "instance.console", "allow", correlationID, "")
	return endpoint, nil
}

// resolveInstance loads an instance and its backend, enforcing ownership.
func (o *Orchestrator) resolveInstance(ctx context.Context, actor policy.Actor, instanceID string, scope policy.Scope) (store.Instance, runtime.Runtime, error) {
	decision := policy.Evaluate(actor, scope)
	if !decision.Allowed {
		return store.Instance{}, nil, fmt.Errorf("%w: %s", ErrPolicyDenied, decision.Reason)
	}
	instance, err := o.store.InstanceByID(ctx, instanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Instance{}, nil, fmt.Errorf("%w: %s", ErrInstanceNotFound, instanceID)
		}
		return store.Instance{}, nil, fmt.Errorf("orchestrator: load instance: %w", err)
	}
	if !policy.Owns(actor, instance.UserID) {
		return store.Instance{}, nil, fmt.Errorf("%w: the instance belongs to another user", ErrPolicyDenied)
	}
	backend, ok := o.runtimes.Get(instance.Runtime)
	if !ok {
		return store.Instance{}, nil, fmt.Errorf("%w", runtime.ErrUnknownBackend{Name: instance.Runtime})
	}
	return instance, backend, nil
}

// ExtendTTL pushes the expiry of an instance further into the future.
func (o *Orchestrator) ExtendTTL(ctx context.Context, actor policy.Actor, instanceID string, ttl time.Duration, correlationID string) (time.Time, error) {
	decision := policy.Evaluate(actor, policy.ScopeInstancesCreate)
	if !decision.Allowed {
		return time.Time{}, fmt.Errorf("%w: %s", ErrPolicyDenied, decision.Reason)
	}
	instance, _, err := o.resolveInstance(ctx, actor, instanceID, policy.ScopeInstancesCreate)
	if err != nil {
		return time.Time{}, err
	}
	if ttl <= 0 {
		ttl = o.limits.DefaultTTL
	}
	if o.limits.MaxTTL > 0 {
		maxExpiry := o.now().Add(o.limits.MaxTTL)
		if instance.ExpiresAt.After(maxExpiry) {
			ttl = maxExpiry.Sub(o.now())
		}
	}
	expiry := o.now().Add(ttl)
	if err := o.store.ExtendInstanceTTL(ctx, instance.ID, expiry); err != nil {
		return time.Time{}, fmt.Errorf("orchestrator: extend ttl: %w", err)
	}
	o.audit(ctx, actor, "instance.extend", "allow", correlationID, "")
	return expiry, nil
}

// CancelJob cancels a job that has not reached a terminal state.
func (o *Orchestrator) CancelJob(ctx context.Context, actor policy.Actor, jobID string, correlationID string) error {
	decision := policy.Evaluate(actor, policy.ScopeJobsCancel)
	if !decision.Allowed {
		o.audit(ctx, actor, "job.cancel", "deny", correlationID, decision.Reason)
		return fmt.Errorf("%w: %s", ErrPolicyDenied, decision.Reason)
	}
	job, err := o.store.JobByID(ctx, jobID)
	if err != nil {
		return fmt.Errorf("orchestrator: load job: %w", err)
	}
	if !policy.Owns(actor, job.UserID) {
		return fmt.Errorf("%w: the job belongs to another user", ErrPolicyDenied)
	}
	if job.State.Terminal() {
		return fmt.Errorf("%w: the job already finished with state %s", ErrConflict, job.State)
	}

	o.mu.Lock()
	cancel := o.inflight[jobID]
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := o.store.UpdateJobState(ctx, jobID, store.JobCanceled, "canceled", "the job was canceled by the operator"); err != nil {
		return fmt.Errorf("orchestrator: cancel job: %w", err)
	}
	o.audit(ctx, actor, "job.cancel", "allow", correlationID, "")
	return nil
}

// removeWorkspace deletes the workspace directory of an instance.
func (o *Orchestrator) removeWorkspace(instanceID string) {
	dir := o.workspacePath(instanceID)
	if err := os.RemoveAll(dir); err != nil {
		o.log.Error("failed to remove workspace", "instance", instanceID, "error", err)
	}
}

// ReapExpired stops and destroys the instances whose TTL has elapsed.
func (o *Orchestrator) ReapExpired(ctx context.Context) (int, error) {
	expired, err := o.store.ExpiredInstances(ctx, o.now())
	if err != nil {
		return 0, fmt.Errorf("orchestrator: list expired instances: %w", err)
	}
	reaped := 0
	for _, instance := range expired {
		backend, ok := o.runtimes.Get(instance.Runtime)
		if ok && instance.RuntimeID != "" {
			_ = backend.Stop(ctx, runtime.ID(instance.RuntimeID), 5*time.Second)
			_ = backend.Destroy(ctx, runtime.ID(instance.RuntimeID))
		}
		if err := o.store.UpdateInstanceState(ctx, instance.ID, store.InstanceDestroyed); err != nil {
			o.log.Error("failed to mark expired instance destroyed", "instance", instance.ID, "error", err)
			continue
		}
		o.removeWorkspace(instance.ID)
		reaped++
	}
	return reaped, nil
}

// NewAuditID returns a fresh audit identifier.
func NewAuditID() string { return ids.New(ids.PrefixAudit) }
