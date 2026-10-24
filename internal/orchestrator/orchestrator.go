// Package orchestrator implements the control plane workflow: it resolves
// templates, enforces the node policy, drives the execution backend and
// records the outcome as a job.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
	"github.com/zarigata/double-agent/internal/templates"
)

// Errors surfaced to the API layer. The API maps them onto the stable error
// codes documented in docs/MCP_API_AND_DISCOVERY.md.
var (
	// ErrQuotaExceeded is returned when a node or user quota is reached.
	ErrQuotaExceeded = errors.New("orchestrator: quota exceeded")
	// ErrPolicyDenied is returned when the authorization rules refuse a request.
	ErrPolicyDenied = errors.New("orchestrator: request denied by policy")
	// ErrTemplateNotFound is returned when a template identifier is unknown.
	ErrTemplateNotFound = errors.New("orchestrator: template not found")
	// ErrInstanceNotFound is returned when an instance identifier is unknown.
	ErrInstanceNotFound = errors.New("orchestrator: instance not found")
	// ErrConflict is returned when an idempotent request differs from the
	// original one.
	ErrConflict = errors.New("orchestrator: request conflicts with an existing one")
	// ErrInvalidRequest is returned when the request itself is malformed.
	ErrInvalidRequest = errors.New("orchestrator: invalid request")
)

// Clock returns the current time. Tests replace it to get deterministic
// timestamps.
type Clock func() time.Time

// Orchestrator coordinates the control plane workflow.
type Orchestrator struct {
	store     *store.Store
	catalogue *templates.Catalogue
	runtimes  *runtime.Registry
	log       *slog.Logger
	now       Clock

	// limits mirrors the node policy configuration.
	limits Limits

	// artifacts is the controlled file store of the node. The exports of
	// the workloads join it.
	artifacts *artifacts.Store

	// retention is the lifetime of an exported artifact.
	retention time.Duration

	// dir is the node data directory that holds instance workspaces.
	dir string

	// mu guards the in-flight job bookkeeping.
	mu sync.Mutex
	// inflight maps a job identifier to its cancel function.
	inflight map[string]context.CancelFunc
}

// Limits are the resource ceilings enforced by the orchestrator.
type Limits struct {
	MaxConcurrentJobs   int
	MaxInstances        int
	MaxInstancesPerUser int
	DefaultTTL          time.Duration
	MaxTTL              time.Duration
	// MaxArtifactBytes is the largest single file an export may produce.
	MaxArtifactBytes int64
	// MaxExportBytes is the largest total an export request may produce.
	MaxExportBytes int64
}

// defaultArtifactSize is the largest artifact the node accepts when the
// configuration does not name a limit.
const defaultArtifactSize int64 = 1 << 30

// Options configures a new Orchestrator.
type Options struct {
	Store     *store.Store
	Catalogue *templates.Catalogue
	Runtimes  *runtime.Registry
	Logger    *slog.Logger
	Limits    Limits
	Clock     Clock
	// DataDir is the node data directory that holds instance workspaces.
	DataDir string
	// Artifacts is the controlled file store of the node. The exports of
	// the workloads join it. When it is empty the orchestrator opens the
	// store that lives beside the workspaces of the node.
	Artifacts *artifacts.Store
	// ArtifactRetention is the lifetime of an exported artifact.
	ArtifactRetention time.Duration
}

// New creates an Orchestrator.
func New(opts Options) (*Orchestrator, error) {
	if opts.Store == nil {
		return nil, errors.New("orchestrator: store is required")
	}
	if opts.Catalogue == nil {
		return nil, errors.New("orchestrator: catalogue is required")
	}
	if opts.Runtimes == nil {
		return nil, errors.New("orchestrator: runtime registry is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now().UTC() }
	}
	if opts.Limits.MaxConcurrentJobs <= 0 {
		opts.Limits.MaxConcurrentJobs = 4
	}
	if opts.Limits.DefaultTTL <= 0 {
		opts.Limits.DefaultTTL = time.Hour
	}
	if opts.Limits.MaxTTL <= 0 {
		opts.Limits.MaxTTL = 4 * time.Hour
	}
	if opts.Limits.MaxArtifactBytes <= 0 {
		opts.Limits.MaxArtifactBytes = defaultArtifactSize
	}
	if opts.Limits.MaxExportBytes <= 0 {
		opts.Limits.MaxExportBytes = defaultArtifactSize
	}
	if opts.DataDir == "" {
		opts.DataDir = "/var/lib/double-agent"
	}
	if opts.ArtifactRetention <= 0 {
		opts.ArtifactRetention = 7 * 24 * time.Hour
	}
	if opts.Artifacts == nil {
		// The node keeps the artifact store beside the workspaces. A
		// deployment that moves the store passes it explicitly.
		store, err := artifacts.New(artifacts.Options{
			Root:    filepath.Join(opts.DataDir, "artifacts"),
			MaxSize: defaultArtifactSize,
		})
		if err != nil {
			return nil, fmt.Errorf("orchestrator: open the artifact store: %w", err)
		}
		opts.Artifacts = store
	}
	return &Orchestrator{
		dir:       opts.DataDir,
		store:     opts.Store,
		catalogue: opts.Catalogue,
		runtimes:  opts.Runtimes,
		log:       opts.Logger.With("component", "orchestrator"),
		now:       opts.Clock,
		limits:    opts.Limits,
		artifacts: opts.Artifacts,
		retention: opts.ArtifactRetention,
		inflight:  map[string]context.CancelFunc{},
	}, nil
}

// Catalogue exposes the template catalogue.
func (o *Orchestrator) Catalogue() *templates.Catalogue { return o.catalogue }

// Runtimes exposes the runtime registry.
func (o *Orchestrator) Runtimes() *runtime.Registry { return o.runtimes }

// Store exposes the metadata store.
func (o *Orchestrator) Store() *store.Store { return o.store }

// Artifacts exposes the controlled file store of the node.
func (o *Orchestrator) Artifacts() *artifacts.Store { return o.artifacts }

// DataDir returns the node data directory.
func (o *Orchestrator) DataDir() string { return o.dir }

// CreateRequest is the input of a create-instance request.
type CreateRequest struct {
	// Actor is the authenticated caller.
	Actor policy.Actor
	// TemplateID selects the template.
	TemplateID string
	// TemplateVersion pins a version. Empty selects the latest.
	TemplateVersion string
	// Name is the human readable instance name.
	Name string
	// Env overrides the template environment.
	Env map[string]string
	// TTL is the requested lifetime. Zero uses the template default.
	TTL time.Duration
	// IdempotencyKey makes the request replayable.
	IdempotencyKey string
	// SecretRefs are the secrets the workload may read.
	SecretRefs []string
	// CorrelationID ties audit records together.
	CorrelationID string
}

// CreateResult is the outcome of a create-instance request.
type CreateResult struct {
	// Job is the observable job record.
	Job store.Job
	// Instance is the created instance, when the job already produced one.
	Instance *store.Instance
	// Replayed reports that an existing job was returned for an idempotent
	// request.
	Replayed bool
}

// Create validates a request, enforces the node policy and starts the
// creation workflow.
func (o *Orchestrator) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	// An idempotent request must return the original job rather than
	// starting a second one, so this check happens before any side effect.
	if req.IdempotencyKey != "" {
		existing, err := o.store.JobByIdempotencyKey(ctx, req.Actor.ID, req.IdempotencyKey)
		if err == nil {
			return CreateResult{Job: existing, Replayed: true}, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return CreateResult{}, fmt.Errorf("orchestrator: look up idempotency key: %w", err)
		}
	}

	decision := policy.Evaluate(req.Actor, policy.ScopeInstancesCreate)
	if !decision.Allowed {
		o.audit(ctx, req.Actor, "instance.create", "deny", req.CorrelationID, decision.Reason)
		return CreateResult{}, fmt.Errorf("%w: %s", ErrPolicyDenied, decision.Reason)
	}

	tmpl, ok := o.catalogue.Get(req.TemplateID, req.TemplateVersion)
	if !ok {
		return CreateResult{}, fmt.Errorf("%w: %s", ErrTemplateNotFound, req.TemplateID)
	}

	ttl, err := o.resolveTTL(tmpl, req.TTL)
	if err != nil {
		return CreateResult{}, err
	}

	if err := o.checkQuotas(ctx, req.Actor); err != nil {
		o.audit(ctx, req.Actor, "instance.create", "deny", req.CorrelationID, err.Error())
		return CreateResult{}, err
	}

	backend := tmpl.Spec.Backend
	if backend == "" {
		backend = o.runtimes.Default
	}
	rt, ok := o.runtimes.Get(backend)
	if !ok {
		return CreateResult{}, fmt.Errorf("%w: %s", ErrPolicyDenied, backend)
	}

	recipe, err := o.buildRecipe(tmpl, rt, req)
	if err != nil {
		return CreateResult{}, err
	}

	jobID := ids.New(ids.PrefixJob)
	instanceID := ids.New(ids.PrefixInstance)
	job := store.Job{
		ID:             jobID,
		UserID:         req.Actor.ID,
		Kind:           store.JobKindInstanceCreate,
		State:          store.JobQueued,
		TemplateID:     tmpl.Spec.ID,
		Runtime:        backend,
		InstanceID:     instanceID,
		IdempotencyKey: req.IdempotencyKey,
		Request: map[string]any{
			"template_id":      tmpl.Spec.ID,
			"template_version": tmpl.Spec.Version,
			"name":             req.Name,
			"ttl_seconds":      int64(ttl.Seconds()),
		},
	}
	if _, err := o.store.CreateJob(ctx, job); err != nil {
		return CreateResult{}, fmt.Errorf("orchestrator: create job: %w", err)
	}
	if err := o.store.SetJobRecipe(ctx, jobID, recipeToMap(recipe)); err != nil {
		return CreateResult{}, fmt.Errorf("orchestrator: store recipe: %w", err)
	}

	o.audit(ctx, req.Actor, "instance.create", "allow", req.CorrelationID, "")

	// The creation runs in the background so that the API can answer
	// immediately with the job identifier.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	o.mu.Lock()
	o.inflight[jobID] = cancel
	o.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			o.mu.Lock()
			delete(o.inflight, jobID)
			o.mu.Unlock()
		}()
		o.runCreate(runCtx, jobID, instanceID, req, tmpl, rt, recipe, ttl)
	}()

	return CreateResult{Job: job}, nil
}

// runCreate performs the actual instance creation.
func (o *Orchestrator) runCreate(ctx context.Context, jobID, instanceID string, req CreateRequest,
	tmpl templates.Template, rt runtime.Runtime, recipe runtime.Recipe, ttl time.Duration) {
	if err := o.store.UpdateJobState(ctx, jobID, store.JobPreparing, "", ""); err != nil {
		o.log.Error("failed to mark job preparing", "job", jobID, "error", err)
	}

	workspace := o.workspacePath(instanceID)
	instance := store.Instance{
		ID:         instanceID,
		JobID:      jobID,
		UserID:     req.Actor.ID,
		TemplateID: tmpl.Spec.ID,
		Runtime:    rt.Name(),
		Name:       req.Name,
		State:      store.InstanceRequested,
		Resources:  resourcesToMap(recipe.Resources),
		Workspace:  workspace,
		ExpiresAt:  o.now().Add(ttl),
	}
	if _, err := o.store.CreateInstance(ctx, instance); err != nil {
		o.failJob(ctx, jobID, instanceID, "internal", fmt.Sprintf("could not record instance: %v", err))
		return
	}
	// The state moves to preparing only once the record exists, otherwise the
	// update finds no row and the transition is silently lost.
	if err := o.store.UpdateInstanceState(ctx, instanceID, store.InstancePreparing); err != nil {
		o.log.Error("failed to mark instance preparing", "instance", instanceID, "error", err)
	}

	spec := runtime.InstanceSpec{
		ID:        runtime.ID(instanceID),
		Name:      instanceName(req.Name, instanceID),
		Recipe:    recipe,
		Workspace: workspace,
		TTL:       ttl,
		Labels:    instanceLabels(req, tmpl, jobID, instanceID),
		Secrets:   map[string]string{},
	}

	if err := rt.Prepare(ctx, recipe); err != nil {
		o.failJob(ctx, jobID, instanceID, "runtime_unavailable",
			fmt.Sprintf("could not prepare the backend: %v", err))
		return
	}

	created, err := rt.Create(ctx, spec)
	if err != nil {
		o.failJob(ctx, jobID, instanceID, "create_failed",
			fmt.Sprintf("could not create the instance: %v", err))
		return
	}
	if err := o.store.SetInstanceRuntimeID(ctx, instanceID, string(created.Runtime)); err != nil {
		o.log.Error("failed to record runtime id", "instance", instanceID, "error", err)
	}
	if err := o.store.UpdateInstanceState(ctx, instanceID, store.InstanceStarting); err != nil {
		o.log.Error("failed to mark instance starting", "instance", instanceID, "error", err)
	}
	if err := o.store.UpdateJobState(ctx, jobID, store.JobRunning, "", ""); err != nil {
		o.log.Error("failed to mark job running", "job", jobID, "error", err)
	}

	if err := rt.Start(ctx, created.ID); err != nil {
		o.failJob(ctx, jobID, instanceID, "start_failed",
			fmt.Sprintf("could not start the instance: %v", err))
		return
	}
	if err := o.store.UpdateInstanceState(ctx, instanceID, store.InstanceReady); err != nil {
		o.log.Error("failed to mark instance ready", "instance", instanceID, "error", err)
	}

	// The steps declared by the template run inside the instance. A failure
	// stops the workflow but keeps the instance alive so the operator can
	// inspect it.
	for i, step := range recipe.Steps {
		execReq := runtime.ExecRequest{
			Argv:    step.Argv,
			Cwd:     step.Cwd,
			Env:     step.Env,
			Timeout: time.Duration(step.TimeoutSeconds) * time.Second,
		}
		handle, err := rt.Exec(ctx, created.ID, execReq)
		if err != nil {
			if !step.ContinueOnError {
				o.failJob(ctx, jobID, instanceID, "step_failed",
					fmt.Sprintf("step %d could not start: %v", i, err))
				return
			}
			continue
		}
		result := <-handle.Done
		if result.ExitCode != 0 && !step.ContinueOnError {
			o.failJob(ctx, jobID, instanceID, "step_failed",
				fmt.Sprintf("step %d exited with code %d", i, result.ExitCode))
			return
		}
	}

	if err := o.store.UpdateJobState(ctx, jobID, store.JobCompleted, "", ""); err != nil {
		o.log.Error("failed to complete job", "job", jobID, "error", err)
	}
}

// resolveTTL clamps the requested lifetime to the node policy.
func (o *Orchestrator) resolveTTL(tmpl templates.Template, requested time.Duration) (time.Duration, error) {
	ttl := requested
	if ttl <= 0 {
		ttl = tmpl.Spec.DefaultTTL()
	}
	if ttl <= 0 {
		ttl = o.limits.DefaultTTL
	}
	if ttl > o.limits.MaxTTL {
		return 0, fmt.Errorf("%w: the requested lifetime exceeds the maximum of %s",
			ErrQuotaExceeded, o.limits.MaxTTL)
	}
	return ttl, nil
}

// checkQuotas verifies that the node and the user have room for one more
// instance.
func (o *Orchestrator) checkQuotas(ctx context.Context, actor policy.Actor) error {
	if o.limits.MaxInstances > 0 {
		live, err := o.store.CountLiveInstances(ctx)
		if err != nil {
			return fmt.Errorf("orchestrator: count instances: %w", err)
		}
		if live >= o.limits.MaxInstances {
			return fmt.Errorf("%w: the node already hosts %d instances", ErrQuotaExceeded, live)
		}
	}
	if o.limits.MaxInstancesPerUser > 0 {
		live, err := o.store.CountLiveInstancesByUser(ctx, actor.ID)
		if err != nil {
			return fmt.Errorf("orchestrator: count user instances: %w", err)
		}
		if live >= o.limits.MaxInstancesPerUser {
			return fmt.Errorf("%w: the user already hosts %d instances",
				ErrQuotaExceeded, live)
		}
	}
	return nil
}

// buildRecipe turns a template and a request into an executable recipe.
func (o *Orchestrator) buildRecipe(tmpl templates.Template, rt runtime.Runtime, req CreateRequest) (runtime.Recipe, error) {
	env := map[string]string{}
	for k, v := range tmpl.Spec.Environment {
		env[k] = v
	}
	for k, v := range req.Env {
		env[k] = v
	}
	network := tmpl.Spec.Network
	if network.Egress == "" {
		network.Egress = "none"
	}
	recipe := runtime.Recipe{
		TemplateID:      tmpl.Spec.ID,
		TemplateVersion: tmpl.Spec.Version,
		Image:           tmpl.Spec.Image,
		ImageDigest:     tmpl.Spec.ImageDigest,
		Runtime:         tmpl.Spec.Runtime,
		Backend:         rt.Name(),
		Resources:       tmpl.Spec.Resources,
		Environment:     env,
		Network:         network,
		Capabilities:    tmpl.Spec.Capabilities,
		Steps:           tmpl.Spec.Steps,
	}
	recipe.Digest = recipeDigest(recipe)
	return recipe, nil
}

// workspacePath returns the workspace directory of an instance.
func (o *Orchestrator) workspacePath(instanceID string) string {
	return o.storeDir("instances", instanceID)
}

// storeDir returns a path inside the node data directory.
func (o *Orchestrator) storeDir(parts ...string) string {
	return filepath.Join(append([]string{o.dir}, parts...)...)
}

// dataDir returns the node data directory.
func (o *Orchestrator) dataDir() string {
	if o.dir != "" {
		return o.dir
	}
	return "/var/lib/double-agent"
}

// failJob records a terminal failure for a job and its instance.
func (o *Orchestrator) failJob(ctx context.Context, jobID, instanceID, code, message string) {
	o.log.Error("job failed", "job", jobID, "code", code, "message", message)
	if err := o.store.UpdateJobState(ctx, jobID, store.JobFailed, code, message); err != nil {
		o.log.Error("failed to record job failure", "job", jobID, "error", err)
	}
	if instanceID != "" {
		if err := o.store.UpdateInstanceState(ctx, instanceID, store.InstanceFailed); err != nil {
			o.log.Error("failed to record instance failure", "instance", instanceID, "error", err)
		}
	}
}

// audit writes an audit record for a decision.
func (o *Orchestrator) audit(ctx context.Context, actor policy.Actor, action, decision, correlationID, reason string) {
	actorType := actor.Type
	if actorType == "" {
		actorType = store.ActorUser
	}
	event := store.AuditEvent{
		ID:            ids.New(ids.PrefixAudit),
		Timestamp:     o.now(),
		ActorType:     actorType,
		ActorID:       actor.ID,
		Action:        action,
		Decision:      decision,
		CorrelationID: correlationID,
		Result:        reason,
	}
	if _, err := o.store.AppendAuditEvent(ctx, event); err != nil {
		o.log.Error("failed to write audit event", "action", action, "error", err)
	}
}

// instanceName builds a deterministic container name.
func instanceName(requested, instanceID string) string {
	if requested != "" {
		return requested
	}
	return "double-agent-" + instanceID
}

// instanceLabels builds the labels attached to an instance.
func instanceLabels(req CreateRequest, tmpl templates.Template, jobID, instanceID string) map[string]string {
	labels := map[string]string{
		"double-agent.instance": instanceID,
		"double-agent.job":      jobID,
		"double-agent.user":     req.Actor.ID,
		"double-agent.template": tmpl.Spec.ID,
	}
	for k, v := range tmpl.Spec.Labels {
		labels["double-agent.label."+k] = v
	}
	return labels
}

// recipeToMap converts a recipe into a JSON friendly map for storage.
func recipeToMap(r runtime.Recipe) map[string]any {
	out := map[string]any{
		"template_id":      r.TemplateID,
		"template_version": r.TemplateVersion,
		"image":            r.Image,
		"runtime":          string(r.Runtime),
		"backend":          r.Backend,
		"digest":           r.Digest,
		"resources": map[string]any{
			"cpu":       r.Resources.CPU,
			"memory_mb": r.Resources.MemoryMB,
			"disk_mb":   r.Resources.DiskMB,
		},
		"network": map[string]any{
			"egress":      r.Network.Egress,
			"allowlist":   r.Network.Allowlist,
			"allowed_dns": r.Network.AllowedDNS,
		},
	}
	if r.ImageDigest != "" {
		out["image_digest"] = r.ImageDigest
	}
	if len(r.Environment) > 0 {
		out["environment"] = r.Environment
	}
	if len(r.Capabilities) > 0 {
		out["capabilities"] = r.Capabilities
	}
	if len(r.Steps) > 0 {
		out["steps"] = len(r.Steps)
	}
	return out
}

// resourcesToMap converts a resource envelope into a JSON friendly map.
func resourcesToMap(r runtime.Resources) map[string]any {
	return map[string]any{
		"cpu":       r.CPU,
		"memory_mb": r.MemoryMB,
		"disk_mb":   r.DiskMB,
		"gpu_count": r.GPUCount,
	}
}

// recipeDigest computes the stable digest of a recipe.
func recipeDigest(r runtime.Recipe) string {
	encoded, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return ids.Fingerprint(encoded)
}
