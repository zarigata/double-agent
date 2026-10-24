package mcpsrv

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// Execution defaults of the instance tools.
const (
	// defaultExecTimeout bounds a command that does not ask for a timeout.
	// A command without a deadline could hold a worker forever.
	defaultExecTimeout = 15 * time.Minute
	// maxExecTimeout is the longest lifetime a command may ask for.
	maxExecTimeout = time.Hour
	// defaultStopGrace is the time an instance may take to shut down.
	defaultStopGrace = 10 * time.Second
	// maxExecOutputBytes caps the output a command may produce. The cap
	// keeps a runaway command from filling the memory of the daemon.
	maxExecOutputBytes int64 = 8 << 20
)

// GPURequest asks for accelerators.
type GPURequest struct {
	// Vendor is the family of the accelerator, for example "nvidia".
	Vendor string `json:"vendor,omitempty" jsonschema:"The family of the accelerator. Leave it empty to accept any family."`
	// Count is the number of accelerators the workload needs.
	Count int `json:"count,omitempty" jsonschema:"The number of accelerators the workload needs."`
	// Required reports whether the workload may run without an accelerator.
	Required bool `json:"required,omitempty" jsonschema:"True when the workload cannot run without an accelerator."`
}

// ResourcesRequest is the resource envelope a workload asks for.
type ResourcesRequest struct {
	// CPU is the number of cores the workload needs.
	CPU float64 `json:"cpu,omitempty" jsonschema:"The number of cores the workload needs."`
	// MemoryMB is the memory the workload needs, in megabytes.
	MemoryMB int `json:"memory_mb,omitempty" jsonschema:"The memory the workload needs, in megabytes."`
	// GPU asks for accelerators.
	GPU *GPURequest `json:"gpu,omitempty" jsonschema:"The accelerators the workload needs."`
}

// InstanceCreateInput asks for a new instance.
type InstanceCreateInput struct {
	// Template is the identifier of the template to instantiate.
	Template string `json:"template" jsonschema:"The identifier of the template, for example \"alpine-shell\"."`
	// TemplateVersion pins the version of the template. An empty version
	// selects the latest published one.
	TemplateVersion string `json:"template_version,omitempty" jsonschema:"The published version to use. Leave it empty to use the latest version."`
	// Resources is the resource envelope the workload needs.
	Resources *ResourcesRequest `json:"resources,omitempty" jsonschema:"The resources the workload needs. The node applies the envelope of the template and records the request."`
	// TTLSeconds is the lifetime of the instance.
	TTLSeconds int64 `json:"ttl_seconds,omitempty" jsonschema:"The lifetime of the instance, in seconds. Leave it empty to use the default of the template."`
	// IdempotencyKey makes the call replayable: the same key always returns
	// the same job instead of starting a second workload.
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"A key that identifies the request. Repeating a call with the same key returns the first job instead of starting a second one."`
	// Name is the human readable name of the instance.
	Name string `json:"name,omitempty" jsonschema:"The name of the instance. Leave it empty to let the node name it."`
	// Environment holds the environment variables of the workload. The values
	// are not secret material: a secret is requested with secret_refs.
	Environment map[string]string `json:"environment,omitempty" jsonschema:"The environment variables of the workload. Do not put a secret here."`
	// SecretRefs are the opaque references of the secrets the workload may
	// read. The values are never returned by a tool.
	SecretRefs []string `json:"secret_refs,omitempty" jsonschema:"The opaque references of the secrets the workload may read, for example [\"secret/npm-read-token\"]."`
}

// InstanceCreateOutput is the answer of a creation request.
//
// The shape follows docs/MCP_API_AND_DISCOVERY.md: the client receives the
// job to watch and a link that points at it.
type InstanceCreateOutput struct {
	// JobID is the job that prepares the instance.
	JobID string `json:"job_id"`
	// InstanceID is the instance the job produces.
	InstanceID string `json:"instance_id"`
	// State is the state of the job.
	State string `json:"state"`
	// Links point at the agent-readable resources of the job and of the
	// instance.
	Links map[string]string `json:"links"`
	// Replayed reports that an earlier job was returned for an idempotent
	// request.
	Replayed bool `json:"replayed,omitempty"`
	// RequestedResources are the resources the call asked for.
	RequestedResources *ResourcesRequest `json:"requested_resources,omitempty"`
	// EffectiveResources are the resources the node granted.
	EffectiveResources map[string]any `json:"effective_resources,omitempty"`
}

// InstanceGetInput selects an instance.
type InstanceGetInput struct {
	// InstanceID identifies the instance.
	InstanceID string `json:"instance_id" jsonschema:"The identifier of the instance, for example \"ins_01J...\"."`
}

// InstanceGetOutput carries one instance.
type InstanceGetOutput struct {
	// Instance is the requested instance.
	Instance InstanceView `json:"instance"`
}

// InstanceExecInput describes a command to run inside an instance.
type InstanceExecInput struct {
	// InstanceID identifies the instance.
	InstanceID string `json:"instance_id" jsonschema:"The identifier of the instance, for example \"ins_01J...\"."`
	// Argv is the command, as an argument vector.
	Argv []string `json:"argv" jsonschema:"The command and its arguments, for example [\"npm\", \"test\"]. The arguments are never handed to a shell."`
	// Cwd is the working directory inside the instance.
	Cwd string `json:"cwd,omitempty" jsonschema:"The working directory inside the instance, for example \"/workspace\"."`
	// TimeoutSeconds bounds the execution.
	TimeoutSeconds int64 `json:"timeout_seconds,omitempty" jsonschema:"The longest lifetime of the command, in seconds. The node caps it at one hour."`
	// Environment holds the environment variables of the command. The values
	// are not secret material: a secret is requested with secret_refs.
	Environment map[string]string `json:"environment,omitempty" jsonschema:"The environment variables of the command. Do not put a secret here."`
	// SecretRefs are the opaque references of the secrets the command may
	// read. The values are never returned by a tool.
	SecretRefs []string `json:"secret_refs,omitempty" jsonschema:"The opaque references of the secrets the command may read, for example [\"secret/npm-read-token\"]."`
}

// InstanceExecOutput is the outcome of a command.
type InstanceExecOutput struct {
	// InstanceID is the instance the command ran in.
	InstanceID string `json:"instance_id"`
	// ExitCode is the exit status of the command.
	ExitCode int `json:"exit_code"`
	// Stdout is the captured standard output.
	Stdout string `json:"stdout"`
	// Stderr is the captured standard error.
	Stderr string `json:"stderr"`
	// Truncated reports that the output hit the size cap of the node.
	Truncated bool `json:"truncated"`
	// DurationMS is the wall clock time of the run, in milliseconds.
	DurationMS int64 `json:"duration_ms"`
	// SecretRefs are the references the call carried. Only the references
	// are echoed: a value never leaves the guest.
	SecretRefs []string `json:"secret_refs,omitempty"`
}

// InstanceStopInput selects the instance to stop.
type InstanceStopInput struct {
	// InstanceID identifies the instance.
	InstanceID string `json:"instance_id" jsonschema:"The identifier of the instance, for example \"ins_01J...\"."`
	// GraceSeconds is the time the instance may take to shut down.
	GraceSeconds int64 `json:"grace_seconds,omitempty" jsonschema:"The grace period of the shutdown, in seconds. The node waits ten seconds by default."`
}

// InstanceStopOutput is the answer of a stop request.
type InstanceStopOutput struct {
	// InstanceID is the instance that was stopped.
	InstanceID string `json:"instance_id"`
	// State is the state the instance entered.
	State string `json:"state"`
}

// InstanceDestroyInput selects the instance to destroy.
type InstanceDestroyInput struct {
	// InstanceID identifies the instance.
	InstanceID string `json:"instance_id" jsonschema:"The identifier of the instance, for example \"ins_01J...\"."`
}

// InstanceDestroyOutput is the answer of a destroy request.
type InstanceDestroyOutput struct {
	// InstanceID is the instance that was destroyed.
	InstanceID string `json:"instance_id"`
	// State is the state the instance entered.
	State string `json:"state"`
}

// InstanceExtendTTLInput selects the instance to extend.
type InstanceExtendTTLInput struct {
	// InstanceID identifies the instance.
	InstanceID string `json:"instance_id" jsonschema:"The identifier of the instance, for example \"ins_01J...\"."`
	// TTLSeconds is the new lifetime of the instance.
	TTLSeconds int64 `json:"ttl_seconds" jsonschema:"The new lifetime of the instance, in seconds, counted from now."`
}

// InstanceExtendTTLOutput is the answer of an extension.
type InstanceExtendTTLOutput struct {
	// InstanceID is the instance that was extended.
	InstanceID string `json:"instance_id"`
	// ExpiresAt is the new moment the instance is reaped.
	ExpiresAt time.Time `json:"expires_at"`
}

// addInstanceTools registers the tools of the instances.
func (s *Server) addInstanceTools(server *mcp.Server, actor policy.Actor) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "instance.create",
		Description: "Create a disposable instance from a template. The node answers with the " +
			"job that prepares the instance and with the identifier of the instance; the " +
			"work happens in the background, so watch the job with job.get. The call is " +
			"replayable: send the same idempotency key to get the first job back instead " +
			"of starting a second workload.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input InstanceCreateInput) (*mcp.CallToolResult, InstanceCreateOutput, error) {
		if failure := requireScope(actor, policy.ScopeInstancesCreate, correlationIDFrom(ctx)); failure != nil {
			return nil, InstanceCreateOutput{}, failure
		}
		out, err := s.createInstance(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "instance.get",
		Description: "Read the state of an instance: the template it was created from, the " +
			"backend that hosts it, the resources it received and the moment it expires.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input InstanceGetInput) (*mcp.CallToolResult, InstanceGetOutput, error) {
		if failure := requireScope(actor, policy.ScopeInstancesRead, correlationIDFrom(ctx)); failure != nil {
			return nil, InstanceGetOutput{}, failure
		}
		out, err := s.getInstance(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "instance.exec",
		Description: "Run a command inside an instance. The command is an argument vector, " +
			"never a shell string, so it is never interpreted by a shell. The answer " +
			"carries the exit code, the standard output and the standard error of the run.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input InstanceExecInput) (*mcp.CallToolResult, InstanceExecOutput, error) {
		if failure := requireScope(actor, policy.ScopeInstancesExec, correlationIDFrom(ctx)); failure != nil {
			return nil, InstanceExecOutput{}, failure
		}
		out, err := s.execInstance(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "instance.stop",
		Description: "Stop an instance and give it a grace period to shut down. The " +
			"workspace of the instance is kept, so the instance may be started again by " +
			"the operator of the node.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input InstanceStopInput) (*mcp.CallToolResult, InstanceStopOutput, error) {
		if failure := requireScope(actor, policy.ScopeInstancesStop, correlationIDFrom(ctx)); failure != nil {
			return nil, InstanceStopOutput{}, failure
		}
		out, err := s.stopInstance(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "instance.destroy",
		Description: "Destroy an instance: the workload is stopped, the workspace is " +
			"removed and the resources of the instance are released. The call is final.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input InstanceDestroyInput) (*mcp.CallToolResult, InstanceDestroyOutput, error) {
		if failure := requireScope(actor, policy.ScopeInstancesDestroy, correlationIDFrom(ctx)); failure != nil {
			return nil, InstanceDestroyOutput{}, failure
		}
		out, err := s.destroyInstance(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "instance.extend_ttl",
		Description: "Push the expiry of an instance further into the future. The node " +
			"never extends an instance beyond the maximum lifetime of the node.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input InstanceExtendTTLInput) (*mcp.CallToolResult, InstanceExtendTTLOutput, error) {
		if failure := requireScope(actor, policy.ScopeInstancesCreate, correlationIDFrom(ctx)); failure != nil {
			return nil, InstanceExtendTTLOutput{}, failure
		}
		out, err := s.extendInstanceTTL(ctx, actor, input)
		return nil, out, err
	})
}

// createInstance starts the creation of an instance.
func (s *Server) createInstance(ctx context.Context, actor policy.Actor, input InstanceCreateInput) (InstanceCreateOutput, error) {
	correlationID := correlationIDFrom(ctx)
	template := strings.TrimSpace(input.Template)
	if template == "" {
		return InstanceCreateOutput{}, invalidRequest("the template is required", correlationID)
	}
	if input.Resources != nil && input.Resources.GPU != nil &&
		input.Resources.GPU.Count > 0 && input.Resources.GPU.Required {
		return InstanceCreateOutput{}, &ToolError{
			Code:          CodeCapabilityUnavailable,
			Message:       "no template of this node provides a GPU",
			CorrelationID: correlationID,
			Details:       map[string]any{"template": template},
		}
	}
	if input.Resources != nil {
		// The resource envelope of an instance comes from its template, so
		// the request is recorded but never applied. The answer carries the
		// envelope that was granted.
		s.log.Warn("the resource envelope of a request is not applied",
			"correlation_id", correlationID, "template", template)
	}
	for _, ref := range input.SecretRefs {
		if failure := validateSecretRef(ref, correlationID); failure != nil {
			return InstanceCreateOutput{}, failure
		}
	}

	result, err := s.orch.Create(ctx, orchestrator.CreateRequest{
		Actor:           actor,
		TemplateID:      template,
		TemplateVersion: strings.TrimSpace(input.TemplateVersion),
		Name:            strings.TrimSpace(input.Name),
		Env:             input.Environment,
		TTL:             time.Duration(input.TTLSeconds) * time.Second,
		IdempotencyKey:  strings.TrimSpace(input.IdempotencyKey),
		SecretRefs:      input.SecretRefs,
		CorrelationID:   correlationID,
	})
	if err != nil {
		return InstanceCreateOutput{}, classify(err, correlationID)
	}

	state := string(result.Job.State)
	if state == string(store.JobQueued) {
		// The workflow prepares the instance right away, so a queued job is
		// already preparing.
		state = string(store.JobPreparing)
	}
	out := InstanceCreateOutput{
		JobID:              result.Job.ID,
		InstanceID:         result.Job.InstanceID,
		State:              state,
		Replayed:           result.Replayed,
		RequestedResources: input.Resources,
		Links: map[string]string{
			"status":   "double-agent://jobs/" + result.Job.ID,
			"instance": "double-agent://instances/" + result.Job.InstanceID,
		},
	}
	if resources, ok := result.Job.Recipe["resources"].(map[string]any); ok {
		out.EffectiveResources = resources
	}
	return out, nil
}

// execInstance runs a command inside an instance.
func (s *Server) execInstance(ctx context.Context, actor policy.Actor, input InstanceExecInput) (InstanceExecOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.InstanceID)
	if id == "" {
		return InstanceExecOutput{}, invalidRequest("the instance identifier is required", correlationID)
	}
	if len(input.Argv) == 0 {
		return InstanceExecOutput{}, invalidRequest("the command is required", correlationID)
	}
	argv := make([]string, 0, len(input.Argv))
	for _, arg := range input.Argv {
		if strings.TrimSpace(arg) == "" {
			return InstanceExecOutput{}, invalidRequest("an argument of the command is empty", correlationID)
		}
		argv = append(argv, arg)
	}
	// The references are opaque: the node resolves them inside the guest and
	// no value ever reaches a log or an answer.
	for _, ref := range input.SecretRefs {
		if failure := validateSecretRef(ref, correlationID); failure != nil {
			return InstanceExecOutput{}, failure
		}
	}
	timeout := time.Duration(input.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	if timeout > maxExecTimeout {
		timeout = maxExecTimeout
	}

	response, err := s.orch.Exec(ctx, orchestrator.ExecRequest{
		Actor:          actor,
		InstanceID:     id,
		Argv:           argv,
		Cwd:            strings.TrimSpace(input.Cwd),
		Env:            input.Environment,
		Timeout:        timeout,
		MaxOutputBytes: maxExecOutputBytes,
		CorrelationID:  correlationID,
	})
	if err != nil {
		return InstanceExecOutput{}, classifyRuntime(err, correlationID)
	}
	out := InstanceExecOutput{
		InstanceID: id,
		ExitCode:   response.ExitCode,
		Stdout:     string(response.Stdout),
		Stderr:     string(response.Stderr),
		Truncated:  response.Truncated,
		DurationMS: response.Duration.Milliseconds(),
	}
	if len(input.SecretRefs) > 0 {
		out.SecretRefs = input.SecretRefs
	}
	return out, nil
}

// stopInstance stops an instance.
func (s *Server) stopInstance(ctx context.Context, actor policy.Actor, input InstanceStopInput) (InstanceStopOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.InstanceID)
	if id == "" {
		return InstanceStopOutput{}, invalidRequest("the instance identifier is required", correlationID)
	}
	grace := time.Duration(input.GraceSeconds) * time.Second
	if grace <= 0 {
		grace = defaultStopGrace
	}
	if err := s.orch.Stop(ctx, actor, id, grace, correlationID); err != nil {
		return InstanceStopOutput{}, classify(err, correlationID)
	}
	return InstanceStopOutput{InstanceID: id, State: string(store.InstanceStopping)}, nil
}

// destroyInstance destroys an instance.
func (s *Server) destroyInstance(ctx context.Context, actor policy.Actor, input InstanceDestroyInput) (InstanceDestroyOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.InstanceID)
	if id == "" {
		return InstanceDestroyOutput{}, invalidRequest("the instance identifier is required", correlationID)
	}
	if err := s.orch.Destroy(ctx, actor, id, correlationID); err != nil {
		return InstanceDestroyOutput{}, classify(err, correlationID)
	}
	return InstanceDestroyOutput{InstanceID: id, State: string(store.InstanceDestroyed)}, nil
}

// extendInstanceTTL pushes the expiry of an instance further away.
func (s *Server) extendInstanceTTL(ctx context.Context, actor policy.Actor, input InstanceExtendTTLInput) (InstanceExtendTTLOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.InstanceID)
	if id == "" {
		return InstanceExtendTTLOutput{}, invalidRequest("the instance identifier is required", correlationID)
	}
	ttl := time.Duration(input.TTLSeconds) * time.Second
	if ttl <= 0 {
		return InstanceExtendTTLOutput{}, invalidRequest("the lifetime is required", correlationID)
	}
	expires, err := s.orch.ExtendTTL(ctx, actor, id, ttl, correlationID)
	if err != nil {
		return InstanceExtendTTLOutput{}, classify(err, correlationID)
	}
	return InstanceExtendTTLOutput{InstanceID: id, ExpiresAt: expires}, nil
}

// validateSecretRef checks the shape of an opaque secret reference.
//
// The reference names a secret, it never carries one: a value that looks like
// a secret is refused before it reaches the node.
func validateSecretRef(ref, correlationID string) *ToolError {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return invalidRequest("a secret reference is empty", correlationID)
	}
	if len(trimmed) > 256 {
		return invalidRequest("a secret reference is too long", correlationID)
	}
	for _, r := range trimmed {
		if r <= ' ' || r == 0x7f {
			return invalidRequest("a secret reference holds an invalid character", correlationID)
		}
	}
	return nil
}

// getInstance reads the state of an instance.
func (s *Server) getInstance(ctx context.Context, actor policy.Actor, input InstanceGetInput) (InstanceGetOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.InstanceID)
	if id == "" {
		return InstanceGetOutput{}, invalidRequest("the instance identifier is required", correlationID)
	}
	instance, err := s.store.InstanceByID(ctx, id)
	if err != nil {
		return InstanceGetOutput{}, classify(err, correlationID)
	}
	if !ownsResource(actor, instance.UserID) {
		return InstanceGetOutput{}, permissionDenied(
			"the instance belongs to another account",
			correlationID,
			map[string]any{"instance_id": id},
		)
	}
	return InstanceGetOutput{Instance: viewInstance(instance)}, nil
}
