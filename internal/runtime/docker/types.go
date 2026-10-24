package docker

// The structs below mirror the Docker Engine API payloads. The field names
// are the API names, not Go names, because the daemon is strict about them.

// ContainerCreateRequest is the body of POST /containers/create.
type ContainerCreateRequest struct {
	// Image is the image reference the container is created from.
	Image string `json:"Image"`
	// Cmd is the command the container runs.
	Cmd []string `json:"Cmd,omitempty"`
	// Env holds the container environment as "KEY=value" entries.
	Env []string `json:"Env,omitempty"`
	// Labels are the container labels.
	Labels map[string]string `json:"Labels,omitempty"`
	// WorkingDir is the directory the command is started in.
	WorkingDir string `json:"WorkingDir,omitempty"`
	// HostConfig carries the isolation and resource settings.
	HostConfig *HostConfig `json:"HostConfig,omitempty"`
	// Tty allocates a pseudo terminal. It stays off: the backend reads
	// framed streams and never a terminal.
	Tty bool `json:"Tty"`
	// OpenStdin keeps stdin open. It stays off so that a workload can
	// never block waiting for input.
	OpenStdin bool `json:"OpenStdin"`
	// AttachStdout and AttachStderr are off because the backend reads
	// the output of exec sessions instead.
	AttachStdout bool `json:"AttachStdout"`
	AttachStderr bool `json:"AttachStderr"`
}

// HostConfig is the isolation and resource part of a container creation
// request.
//
// Only the fields the backend controls are declared. Anything that is left
// out keeps the daemon default, which is the safe one for an unprivileged
// workload: the container gets its own PID, IPC, UTS and mount namespaces
// and never the host ones.
type HostConfig struct {
	// Binds are the host paths exposed inside the container. The backend
	// only ever binds the instance workspace.
	Binds []string `json:"Binds"`
	// Memory is the memory limit in bytes.
	Memory int64 `json:"Memory"`
	// MemorySwap is the combined memory and swap limit in bytes. It
	// equals Memory so that the workload cannot swap.
	MemorySwap int64 `json:"MemorySwap"`
	// NanoCpus is the CPU quota in billionths of a CPU.
	NanoCpus int64 `json:"NanoCpus"`
	// NetworkMode is the network the container is attached to.
	NetworkMode string `json:"NetworkMode"`
	// CapDrop lists the capabilities that are removed. The backend always
	// drops all of them.
	CapDrop []string `json:"CapDrop"`
	// CapAdd lists the capabilities that are added back.
	CapAdd []string `json:"CapAdd,omitempty"`
	// SecurityOpt holds the daemon security options.
	SecurityOpt []string `json:"SecurityOpt,omitempty"`
	// PidsLimit caps the number of processes inside the container.
	PidsLimit *int64 `json:"PidsLimit,omitempty"`
	// ReadonlyRootfs mounts the container root filesystem read-only.
	ReadonlyRootfs bool `json:"ReadonlyRootfs"`
	// AutoRemove is always false: the control plane decides when an
	// instance is removed.
	AutoRemove bool `json:"AutoRemove"`
	// Privileged is always false.
	Privileged bool `json:"Privileged"`
}

// ExecCreateRequest is the body of POST /containers/{id}/exec.
type ExecCreateRequest struct {
	// AttachStdin is off: the backend does not feed the daemon.
	AttachStdin bool `json:"AttachStdin"`
	// AttachStdout and AttachStderr ask for the output of the session.
	AttachStdout bool `json:"AttachStdout"`
	AttachStderr bool `json:"AttachStderr"`
	// Cmd is the command line, passed as an argument vector.
	Cmd []string `json:"Cmd"`
	// Env holds the session environment as "KEY=value" entries. It is the
	// only channel through which secret values reach the workload.
	Env []string `json:"Env,omitempty"`
	// WorkingDir is the directory the command runs in.
	WorkingDir string `json:"WorkingDir,omitempty"`
	// User is the user the command runs as. It stays empty so that the
	// image default applies.
	User string `json:"User,omitempty"`
	// Tty is off: the backend reads framed streams.
	Tty bool `json:"Tty"`
	// Privileged is always false.
	Privileged bool `json:"Privileged"`
}

// ExecStartRequest is the body of POST /exec/{id}/start.
type ExecStartRequest struct {
	// Detach is false so that the daemon streams the output back.
	Detach bool `json:"Detach"`
	// Tty mirrors the Tty flag of the exec session.
	Tty bool `json:"Tty"`
}

// ExecInspect is the answer of GET /exec/{id}/json.
type ExecInspect struct {
	// ID is the exec session identifier.
	ID string `json:"ID"`
	// ContainerID is the container the session belongs to.
	ContainerID string `json:"ContainerID"`
	// Running reports whether the session is still running.
	Running bool `json:"Running"`
	// ExitCode is the exit status of the finished session.
	ExitCode int `json:"ExitCode"`
	// Pid is the process identifier inside the container.
	Pid int `json:"Pid"`
}

// ContainerInspect is the answer of GET /containers/{id}/json.
type ContainerInspect struct {
	// ID is the container identifier.
	ID string `json:"Id"`
	// Name is the container name.
	Name string `json:"Name"`
	// Created is the creation timestamp in RFC 3339 form.
	Created string `json:"Created"`
	// State is the runtime state of the container.
	State *ContainerState `json:"State"`
	// Config is the configuration the container was created with.
	Config *ContainerConfig `json:"Config"`
	// HostConfig is the isolation configuration of the container.
	HostConfig *HostConfig `json:"HostConfig"`
}

// ContainerState is the runtime state of a container.
type ContainerState struct {
	// Status is one of created, running, paused, restarting, removing,
	// exited or dead.
	Status string `json:"Status"`
	// Running reports whether the container is running.
	Running bool `json:"Running"`
	// Paused reports whether the container is paused.
	Paused bool `json:"Paused"`
	// Restarting reports whether the container is restarting.
	Restarting bool `json:"Restarting"`
	// ExitCode is the exit status of a stopped container.
	ExitCode int `json:"ExitCode"`
	// Error carries the daemon message of a failed container.
	Error string `json:"Error"`
	// StartedAt is the start timestamp in RFC 3339 form.
	StartedAt string `json:"StartedAt"`
	// FinishedAt is the stop timestamp in RFC 3339 form.
	FinishedAt string `json:"FinishedAt"`
}

// ContainerConfig is the configuration a container was created with.
type ContainerConfig struct {
	// Image is the image the container runs.
	Image string `json:"Image"`
	// Cmd is the command the container runs.
	Cmd []string `json:"Cmd"`
	// Env holds the container environment.
	Env []string `json:"Env"`
	// Labels are the container labels.
	Labels map[string]string `json:"Labels"`
	// WorkingDir is the working directory of the container.
	WorkingDir string `json:"WorkingDir"`
}

// DaemonInfo is the answer of GET /info.
type DaemonInfo struct {
	// ID is the daemon identifier.
	ID string `json:"ID"`
	// Name is the daemon host name.
	Name string `json:"Name"`
	// ServerVersion is the daemon version.
	ServerVersion string `json:"ServerVersion"`
	// OperatingSystem is the host operating system.
	OperatingSystem string `json:"OperatingSystem"`
	// Architecture is the host architecture.
	Architecture string `json:"Architecture"`
	// NCPU is the number of host CPUs.
	NCPU int `json:"NCPU"`
	// MemTotal is the host memory in bytes.
	MemTotal int64 `json:"MemTotal"`
	// SecurityOptions lists the daemon security features that are on.
	SecurityOptions []string `json:"SecurityOptions"`
}

// NetworkCreateRequest is the body of POST /networks/create.
type NetworkCreateRequest struct {
	// Name is the network name.
	Name string `json:"Name"`
	// Driver is the network driver. Only bridge is used.
	Driver string `json:"Driver"`
	// Internal keeps the network isolated from the outside world.
	Internal bool `json:"Internal"`
	// Attachable lets a container join the network manually.
	Attachable bool `json:"Attachable"`
	// EnableIPv6 stays off.
	EnableIPv6 bool `json:"EnableIPv6"`
	// CheckDuplicate rejects a second network with the same name.
	CheckDuplicate bool `json:"CheckDuplicate"`
	// Options holds the driver specific options.
	Options map[string]string `json:"Options,omitempty"`
}
