// Package runtime defines the contract between the Double Agent control plane
// and the execution backends that host disposable workloads.
//
// The interface mirrors docs/ARCHITECTURE.md. Implementations must never run
// guest workloads on the host itself: a "local" implementation exists only for
// development and is explicitly flagged as such by the doctor command.
package runtime

import (
	"context"
	"errors"
	"io"
	"time"
)

// Errors returned by runtime implementations. Callers map these onto the
// stable machine-readable codes documented in docs/MCP_API_AND_DISCOVERY.md.
var (
	ErrNotFound      = errors.New("runtime: instance not found")
	ErrNotSupported  = errors.New("runtime: operation not supported")
	ErrUnavailable   = errors.New("runtime: backend unavailable")
	ErrAlreadyExists = errors.New("runtime: instance already exists")
)

// Kind identifies a runtime backend family.
type Kind string

const (
	// KindContainer is a Linux container runtime (Docker first).
	KindContainer Kind = "container"
	// KindVM is a virtual machine runtime (libvirt/QEMU/KVM).
	KindVM Kind = "vm"
)

// ID identifies an instance inside a runtime backend.
type ID string

// String implements fmt.Stringer.
func (i ID) String() string { return string(i) }

// Resources describes the resource envelope requested for an instance.
//
// The YAML names match the JSON ones so that a template manifest and the
// API describe a workload with the same keys.
type Resources struct {
	CPU       float64 `json:"cpu" yaml:"cpu"`
	MemoryMB  int     `json:"memory_mb" yaml:"memory_mb"`
	DiskMB    int     `json:"disk_mb,omitempty" yaml:"disk_mb,omitempty"`
	GPUCount  int     `json:"gpu_count,omitempty" yaml:"gpu_count,omitempty"`
	GPUVendor string  `json:"gpu_vendor,omitempty" yaml:"gpu_vendor,omitempty"`
}

// NetworkPolicy captures the egress rules applied to an instance.
type NetworkPolicy struct {
	// Egress is one of "none", "restricted" or "allowed".
	Egress string `json:"egress" yaml:"egress"`
	// Allowlists are CIDRs or hostnames permitted when Egress is restricted.
	Allowlist []string `json:"allowlist,omitempty" yaml:"allowlist,omitempty"`
	// AllowedDNS are hostnames permitted when Egress is restricted.
	AllowedDNS []string `json:"allowed_dns,omitempty" yaml:"allowed_dns,omitempty"`
}

// Mount is a host path exposed inside the instance.
type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

// Step is a single command executed inside an instance.
type Step struct {
	Argv            []string          `json:"argv" yaml:"argv"`
	Cwd             string            `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	Env             map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	TimeoutSeconds  int               `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
	ContinueOnError bool              `json:"continue_on_error,omitempty" yaml:"continue_on_error,omitempty"`
	// Outputs are glob patterns relative to the instance workspace that are
	// exported as artifacts when the step completes.
	Outputs []string `json:"outputs,omitempty" yaml:"outputs,omitempty"`
}

// Recipe is a resolved, reproducible execution plan. It is produced by the
// scheduler and is what makes a job reproducible.
type Recipe struct {
	TemplateID      string            `json:"template_id"`
	TemplateVersion string            `json:"template_version"`
	Image           string            `json:"image"`
	ImageDigest     string            `json:"image_digest,omitempty"`
	Runtime         Kind              `json:"runtime"`
	Backend         string            `json:"backend"`
	Resources       Resources         `json:"resources"`
	Environment     map[string]string `json:"environment,omitempty"`
	Network         NetworkPolicy     `json:"network"`
	Mounts          []Mount           `json:"mounts,omitempty"`
	Capabilities    []string          `json:"capabilities,omitempty"`
	// Steps is an ordered list of commands executed after the instance is
	// ready. Empty for bare interactive instances.
	Steps []Step `json:"steps,omitempty"`
	// Digest is a stable hash of the recipe contents.
	Digest string `json:"digest"`
}

// InstanceSpec is the request handed to a runtime backend.
type InstanceSpec struct {
	ID        ID
	Name      string
	Recipe    Recipe
	Workspace string
	TTL       time.Duration
	Labels    map[string]string
	// Secrets are opaque references resolved by the control plane and
	// materialized only inside the instance.
	Secrets map[string]string
}

// State enumerates instance lifecycle states.
type State string

const (
	StateRequested State = "requested"
	StatePreparing State = "preparing"
	StateStarting  State = "starting"
	StateReady     State = "ready"
	StateStopping  State = "stopping"
	StateDestroyed State = "destroyed"
	StateFailed    State = "failed"
)

// Terminal reports whether the state is final.
func (s State) Terminal() bool {
	return s == StateDestroyed || s == StateFailed
}

// Instance is the backend's view of a created instance.
type Instance struct {
	ID       ID
	Backend  string
	Runtime  ID
	State    State
	Created  time.Time
	ExitCode *int
}

// ExecRequest describes a command to run inside an instance.
type ExecRequest struct {
	Argv           []string
	Cwd            string
	Env            map[string]string
	Timeout        time.Duration
	Stdin          io.Reader
	SecretRefs     []string
	MaxOutputBytes int64
}

// ExecResult is the outcome of an exec call.
type ExecResult struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	Duration  time.Duration
}

// ExecHandle is a running exec session.
type ExecHandle struct {
	ID   string
	Done <-chan ExecResult
}

// ObservedState is a runtime's report about an instance.
type ObservedState struct {
	State    State
	ExitCode *int
	Started  time.Time
	// Message carries a human-readable diagnostic, never a secret.
	Message string
}

// ConsoleEndpoint describes how to reach a guest console.
type ConsoleEndpoint struct {
	// URL is an opaque, short-lived token URL served by the control plane.
	URL string
	// Kind is "vnc" or "rdp".
	Kind string
}

// ArtifactRef points at a file that lives outside the instance.
type ArtifactRef struct {
	// Path is the host path of the file.
	Path string
	// Name is the display name recorded in metadata.
	Name string
	// MIME is the detected content type.
	MIME string
	// Digest is the sha256 of the content, hex encoded.
	Digest string
	// Size is the length in bytes.
	Size int64
}

// GuestPath is a path inside an instance workspace.
type GuestPath string

// Runtime is the contract every execution backend implements.
type Runtime interface {
	// Kind reports the runtime family implemented by this backend.
	Kind() Kind
	// Name reports the backend identifier, e.g. "docker" or "local".
	Name() string
	// Prepare makes the backend ready to host workloads.
	Prepare(ctx context.Context, recipe Recipe) error
	// Create provisions a new instance.
	Create(ctx context.Context, spec InstanceSpec) (Instance, error)
	// Start transitions a created instance into a running state.
	Start(ctx context.Context, id ID) error
	// Exec runs a command inside an instance.
	Exec(ctx context.Context, id ID, req ExecRequest) (ExecHandle, error)
	// Console returns a short-lived console endpoint for the instance.
	Console(ctx context.Context, id ID) (ConsoleEndpoint, error)
	// CopyIn uploads a file into the instance workspace.
	CopyIn(ctx context.Context, id ID, src ArtifactRef, dst GuestPath) error
	// CopyOut downloads a file from the instance workspace.
	CopyOut(ctx context.Context, id ID, src GuestPath) (ArtifactRef, error)
	// Stop halts the instance, giving it grace to shut down.
	Stop(ctx context.Context, id ID, grace time.Duration) error
	// Destroy removes the instance and its storage.
	Destroy(ctx context.Context, id ID) error
	// Inspect reports the current observed state of the instance.
	Inspect(ctx context.Context, id ID) (ObservedState, error)
	// Health reports whether the backend is usable.
	Health(ctx context.Context) error
}
