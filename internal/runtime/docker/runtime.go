package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zarigata/double-agent/internal/runtime"
)

const (
	// workspaceMountPath is where the instance workspace is mounted inside
	// the container.
	workspaceMountPath = "/workspace"

	// egressNone, egressRestricted and egressAllowed are the egress
	// policies of the runtime contract.
	egressNone       = "none"
	egressRestricted = "restricted"
	egressAllowed    = "allowed"

	// restrictedNetwork is the dedicated bridge network used by the
	// "restricted" egress policy.
	restrictedNetwork = "double-agent-restricted"

	// defaultPidsLimit caps the number of processes inside a container.
	// The runtime contract has no field for it, so the backend applies a
	// conservative default that keeps a runaway fork bomb harmless.
	defaultPidsLimit int64 = 1024

	// defaultMaxOutputBytes is the output allowance used when a request
	// does not ask for one.
	defaultMaxOutputBytes int64 = 8 << 20

	// maxArtifactBytes bounds the size of a file copied out of an
	// instance.
	maxArtifactBytes int64 = 1 << 30

	// readonlyRootCapability is the recipe capability that asks for a
	// read-only container root filesystem.
	readonlyRootCapability = "fs.readonly-root"
)

const (
	// LabelInstance, LabelJob and LabelUser are the container labels the
	// control plane uses to find the resources of an instance.
	LabelInstance = "double-agent.instance"
	LabelJob      = "double-agent.job"
	LabelUser     = "double-agent.user"
)

// minimalCapabilities are the only capabilities that are added back after
// every capability was dropped.
//
// CHOWN, SETUID and SETGID are needed by the usual "install a package" and
// "run as another user" steps of a workload, and NET_BIND_SERVICE lets a
// service inside the instance bind a privileged port. Nothing else is
// granted: the workload keeps the privileges of an unprivileged process.
var minimalCapabilities = []string{"CHOWN", "SETUID", "SETGID", "NET_BIND_SERVICE"}

// securityOptions are the daemon security options every container gets.
//
// no-new-privileges stops a setuid binary inside the image from gaining
// privileges that the container does not have.
var securityOptions = []string{"no-new-privileges:true"}

// keepaliveCommand is the command a container runs between exec sessions.
// Docker has no idle state, so the container needs a process that does not
// exit. The value is the longest sleep a 32 bit implementation accepts.
var keepaliveCommand = []string{"sleep", "2147483647"}

// Runtime is the Docker Engine implementation of runtime.Runtime.
type Runtime struct {
	client         *Client
	workspaceRoot  string
	allowedImages  []string
	pullImages     bool
	defaultNetwork string

	mu        sync.Mutex
	instances map[string]instanceInfo
}

// instanceInfo remembers what the backend created for an instance.
type instanceInfo struct {
	// containerID is the Docker container identifier.
	containerID string
	// workspace is the host directory that is bound into the container.
	workspace string
}

// New returns a Docker Engine backend.
//
// The workspace root is created when it does not exist yet, because the
// backend owns that directory tree and every bind has to point inside it.
func New(cfg Config) (*Runtime, error) {
	root := strings.TrimSpace(cfg.WorkspaceRoot)
	if root == "" {
		return nil, errors.New("docker: workspace root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("docker: resolve workspace root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("docker: create workspace root: %w", err)
	}
	// The root is resolved so that every later comparison of a host path
	// uses the same spelling. Without it a symlinked root would make
	// every bind look like an escape.
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("docker: resolve workspace root: %w", err)
	}
	absolute = resolved
	client, err := NewClient(cfg.Host, cfg.HTTPClient)
	if err != nil {
		return nil, err
	}
	network := strings.ToLower(strings.TrimSpace(cfg.DefaultNetwork))
	if network == "" {
		network = egressNone
	}
	switch network {
	case egressNone, egressRestricted, egressAllowed:
	default:
		return nil, fmt.Errorf("docker: default network %q is not an egress policy", cfg.DefaultNetwork)
	}
	return &Runtime{
		client:         client,
		workspaceRoot:  absolute,
		allowedImages:  append([]string(nil), cfg.AllowedImages...),
		pullImages:     cfg.PullImages,
		defaultNetwork: network,
		instances:      make(map[string]instanceInfo),
	}, nil
}

// Kind reports the runtime family of the backend.
func (r *Runtime) Kind() runtime.Kind { return runtime.KindContainer }

// Name reports the backend identifier.
func (r *Runtime) Name() string { return "docker" }

// Prepare makes the backend ready to host the workload of a recipe.
//
// It enforces the image policy and, when the backend is allowed to pull,
// fetches an image the daemon does not have yet.
func (r *Runtime) Prepare(ctx context.Context, recipe runtime.Recipe) error {
	image := strings.TrimSpace(recipe.Image)
	if image == "" {
		return errors.New("docker: recipe has no image")
	}
	if err := r.checkImageAllowed(image); err != nil {
		return err
	}
	present, err := r.client.ImageExists(ctx, image)
	if err != nil {
		return fmt.Errorf("docker: inspect image %s: %w", image, err)
	}
	if present {
		return nil
	}
	if !r.pullImages {
		return fmt.Errorf("docker: image %s is not available on the daemon and pulling is disabled", image)
	}
	if err := r.client.ImagePull(ctx, image); err != nil {
		return fmt.Errorf("docker: pull image %s: %w", image, err)
	}
	return nil
}

// Create provisions a container for an instance.
func (r *Runtime) Create(ctx context.Context, spec runtime.InstanceSpec) (runtime.Instance, error) {
	image := strings.TrimSpace(spec.Recipe.Image)
	if image == "" {
		return runtime.Instance{}, errors.New("docker: instance has no image")
	}
	if err := r.checkImageAllowed(image); err != nil {
		return runtime.Instance{}, err
	}
	workspace, err := r.instanceWorkspace(spec)
	if err != nil {
		return runtime.Instance{}, err
	}
	binds, err := r.buildBinds(spec, workspace)
	if err != nil {
		return runtime.Instance{}, err
	}
	network, err := r.networkFor(spec.Recipe.Network.Egress)
	if err != nil {
		return runtime.Instance{}, err
	}
	if network == restrictedNetwork {
		if err := r.ensureRestrictedNetwork(ctx); err != nil {
			return runtime.Instance{}, err
		}
	}
	name := containerName(spec.ID)
	// The environment of the recipe is not secret, but the secrets of the
	// instance are not part of it: they are materialized through the
	// environment of an exec session, which is the only place a secret
	// value ever appears.
	containerID, _, err := r.client.ContainerCreate(ctx, name, ContainerCreateRequest{
		Image:      image,
		Cmd:        keepaliveCommand,
		Env:        environmentList(spec.Recipe.Environment),
		Labels:     r.labels(spec),
		WorkingDir: workspaceMountPath,
		HostConfig: r.hostConfig(spec.Recipe, binds, network),
	})
	if err != nil {
		if IsConflict(err) {
			return runtime.Instance{}, fmt.Errorf("%w: container %s", runtime.ErrAlreadyExists, name)
		}
		return runtime.Instance{}, fmt.Errorf("docker: create container: %w", err)
	}
	if err := r.client.ContainerStart(ctx, containerID); err != nil {
		// A container that cannot start is of no use to anybody, so it
		// is removed again instead of lingering on the daemon.
		_ = r.client.ContainerRemove(ctx, containerID, true)
		return runtime.Instance{}, fmt.Errorf("docker: start container: %w", err)
	}
	r.remember(spec.ID, instanceInfo{containerID: containerID, workspace: workspace})
	return runtime.Instance{
		ID:      spec.ID,
		Backend: r.Name(),
		Runtime: runtime.ID(containerID),
		State:   runtime.StateReady,
		Created: time.Now(),
	}, nil
}

// Start transitions a created instance into a running state.
func (r *Runtime) Start(ctx context.Context, id runtime.ID) error {
	if err := r.client.ContainerStart(ctx, r.containerRef(id)); err != nil {
		if IsNotFound(err) {
			return fmt.Errorf("%w: instance %s", runtime.ErrNotFound, id)
		}
		return fmt.Errorf("docker: start container: %w", err)
	}
	return nil
}

// Exec runs a command inside an instance.
//
// The command is passed as an argument vector and is never handed to a
// shell, so a recipe cannot smuggle a second command into the instance.
// The environment of the session is the only channel through which secret
// values reach the workload, and it is never echoed back.
//
// The standard input of the request is not supported. The backend never
// attaches an input stream, so a workload cannot block waiting for a
// console.
func (r *Runtime) Exec(ctx context.Context, id runtime.ID, req runtime.ExecRequest) (runtime.ExecHandle, error) {
	if len(req.Argv) == 0 {
		return runtime.ExecHandle{}, errors.New("docker: exec needs an argument vector")
	}
	container := r.containerRef(id)
	workingDir, err := r.execWorkingDir(req.Cwd)
	if err != nil {
		return runtime.ExecHandle{}, err
	}
	execID, err := r.client.ExecCreate(ctx, container, ExecCreateRequest{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          req.Argv,
		Env:          environmentList(req.Env),
		WorkingDir:   workingDir,
		Tty:          false,
		Privileged:   false,
	})
	if err != nil {
		if IsNotFound(err) {
			return runtime.ExecHandle{}, fmt.Errorf("%w: instance %s", runtime.ErrNotFound, id)
		}
		return runtime.ExecHandle{}, fmt.Errorf("docker: create exec: %w", err)
	}
	sessionCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		sessionCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	stream, mediaType, err := r.client.ExecStart(sessionCtx, execID, false)
	if err != nil {
		cancel()
		return runtime.ExecHandle{}, fmt.Errorf("docker: start exec: %w", err)
	}
	done := make(chan runtime.ExecResult, 1)
	go func() {
		defer close(done)
		defer cancel()
		defer stream.Close()
		done <- r.collectExec(sessionCtx, execID, stream, mediaType, req)
	}()
	return runtime.ExecHandle{ID: execID, Done: done}, nil
}

// collectExec reads the output of a session and reports its outcome.
func (r *Runtime) collectExec(ctx context.Context, execID string, stream io.Reader, mediaType string, req runtime.ExecRequest) runtime.ExecResult {
	started := time.Now()
	limit := req.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	budget := &outputBudget{limit: limit}
	stdout := &limitedBuffer{budget: budget}
	stderr := &limitedBuffer{budget: budget}
	var result runtime.ExecResult
	readErr := demuxStream(stream, stdout, stderr, streamFramed(mediaType))
	result.Stdout = stdout.Bytes()
	result.Stderr = stderr.Bytes()
	result.Truncated = budget.truncated
	if readErr != nil {
		// A session that failed mid-flight has no exit code, so the
		// failure is reported on stderr with a negative exit code.
		result.ExitCode = -1
		result.Stderr = append(result.Stderr, []byte(redact(readErr.Error(), req.Env)+"\n")...)
		result.Duration = time.Since(started)
		return result
	}
	info, err := r.client.ExecInspect(ctx, execID)
	if err != nil {
		result.ExitCode = -1
		result.Stderr = append(result.Stderr, []byte(redact(err.Error(), req.Env)+"\n")...)
		result.Duration = time.Since(started)
		return result
	}
	result.ExitCode = info.ExitCode
	result.Duration = time.Since(started)
	return result
}

// Console reports that the backend has no console endpoint.
//
// Containers have no graphical console in the MVP. The desktop runtime
// serves one through the control plane instead.
func (r *Runtime) Console(ctx context.Context, id runtime.ID) (runtime.ConsoleEndpoint, error) {
	return runtime.ConsoleEndpoint{}, fmt.Errorf("%w: the docker backend has no console endpoint", runtime.ErrNotSupported)
}

// CopyIn uploads a file into the instance workspace.
func (r *Runtime) CopyIn(ctx context.Context, id runtime.ID, src runtime.ArtifactRef, dst runtime.GuestPath) error {
	relative, err := workspaceRelative(string(dst))
	if err != nil {
		return err
	}
	workspace := r.workspaceFor(id)
	target := filepath.Join(workspace, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("docker: create workspace directory: %w", err)
	}
	info, err := os.Lstat(src.Path)
	if err != nil {
		return fmt.Errorf("docker: read artifact %s: %w", src.Path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("docker: artifact %s is not a regular file", src.Path)
	}
	content, err := os.ReadFile(src.Path)
	if err != nil {
		return fmt.Errorf("docker: read artifact %s: %w", src.Path, err)
	}
	archive, err := buildTar([]tarFile{{Name: path.Base(relative), Mode: 0o644, Data: content}})
	if err != nil {
		return err
	}
	directory := path.Dir(path.Join(workspaceMountPath, relative))
	if err := r.client.CopyToContainer(ctx, r.containerRef(id), directory, bytes.NewReader(archive)); err != nil {
		return fmt.Errorf("docker: upload %s: %w", relative, err)
	}
	return nil
}

// CopyOut downloads a file from the instance workspace.
//
// The file is written outside of the instance workspace so that the workload
// cannot read it back. The control plane is expected to import the artifact
// into its artifact store right away.
func (r *Runtime) CopyOut(ctx context.Context, id runtime.ID, src runtime.GuestPath) (runtime.ArtifactRef, error) {
	relative, err := workspaceRelative(string(src))
	if err != nil {
		return runtime.ArtifactRef{}, err
	}
	containerPath := path.Join(workspaceMountPath, relative)
	stream, err := r.client.CopyFromContainer(ctx, r.containerRef(id), containerPath)
	if err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("docker: download %s: %w", relative, err)
	}
	defer stream.Close()
	name, content, err := extractTar(stream, path.Base(relative), maxArtifactBytes)
	if err != nil {
		return runtime.ArtifactRef{}, err
	}
	destination := filepath.Join(r.workspaceRoot, "exports", sanitizeName(string(id)), filepath.Base(name))
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("docker: create export directory: %w", err)
	}
	if err := os.WriteFile(destination, content, 0o640); err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("docker: write export %s: %w", destination, err)
	}
	digest := sha256.Sum256(content)
	return runtime.ArtifactRef{
		Path:   destination,
		Name:   filepath.Base(name),
		MIME:   http.DetectContentType(content),
		Digest: hex.EncodeToString(digest[:]),
		Size:   int64(len(content)),
	}, nil
}

// Stop halts an instance, giving it a grace period to shut down.
func (r *Runtime) Stop(ctx context.Context, id runtime.ID, grace time.Duration) error {
	if err := r.client.ContainerStop(ctx, r.containerRef(id), grace); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("docker: stop container: %w", err)
	}
	return nil
}

// Destroy removes an instance and the storage that belongs to it.
func (r *Runtime) Destroy(ctx context.Context, id runtime.ID) error {
	if err := r.client.ContainerRemove(ctx, r.containerRef(id), true); err != nil && !IsNotFound(err) {
		return fmt.Errorf("docker: remove container: %w", err)
	}
	info, ok := r.forget(id)
	if !ok {
		return nil
	}
	if err := os.RemoveAll(info.workspace); err != nil {
		return fmt.Errorf("docker: remove workspace %s: %w", info.workspace, err)
	}
	return nil
}

// Inspect reports the observed state of an instance.
func (r *Runtime) Inspect(ctx context.Context, id runtime.ID) (runtime.ObservedState, error) {
	info, err := r.client.ContainerInspect(ctx, r.containerRef(id))
	if err != nil {
		if IsNotFound(err) {
			return runtime.ObservedState{State: runtime.StateDestroyed}, nil
		}
		return runtime.ObservedState{}, fmt.Errorf("docker: inspect container: %w", err)
	}
	return observedState(info), nil
}

// Health reports whether the daemon is reachable and usable.
func (r *Runtime) Health(ctx context.Context) error {
	if err := r.client.Ping(ctx); err != nil {
		return fmt.Errorf("%w: docker: daemon is not reachable: %w", runtime.ErrUnavailable, err)
	}
	if _, err := r.client.Info(ctx); err != nil {
		return fmt.Errorf("docker: daemon did not report its state: %w", err)
	}
	return nil
}

// labels returns the container labels of an instance.
//
// The three canonical labels let the control plane find the container of an
// instance without keeping state of its own.
func (r *Runtime) labels(spec runtime.InstanceSpec) map[string]string {
	labels := make(map[string]string, len(spec.Labels)+3)
	for key, value := range spec.Labels {
		labels[key] = value
	}
	labels[LabelInstance] = string(spec.ID)
	labels[LabelJob] = labelValue(spec.Labels, LabelJob, "job")
	labels[LabelUser] = labelValue(spec.Labels, LabelUser, "user")
	return labels
}

// labelValue reads a label from the instance labels. Both the prefixed and
// the plain spelling are accepted.
func labelValue(labels map[string]string, prefixed, plain string) string {
	if value, ok := labels[prefixed]; ok && value != "" {
		return value
	}
	return labels[plain]
}

// hostConfig builds the isolation and resource configuration of a container.
func (r *Runtime) hostConfig(recipe runtime.Recipe, binds []string, network string) *HostConfig {
	memory := int64(recipe.Resources.MemoryMB) << 20
	pids := defaultPidsLimit
	return &HostConfig{
		Binds:       binds,
		Memory:      memory,
		MemorySwap:  memory,
		NanoCpus:    int64(recipe.Resources.CPU * 1e9),
		NetworkMode: network,
		CapDrop:     []string{"ALL"},
		CapAdd:      minimalCapabilities,
		SecurityOpt: securityOptions,
		PidsLimit:   &pids,
		// The container root filesystem stays writable unless the recipe
		// asks for a read-only one. The writable surface of the host is
		// limited to the workspace bind either way.
		ReadonlyRootfs: hasCapability(recipe.Capabilities, readonlyRootCapability),
		// The control plane decides when an instance is removed.
		AutoRemove: false,
		// The backend never creates a privileged container.
		Privileged: false,
	}
}

// networkFor maps an egress policy onto a Docker network.
//
// "none" keeps the instance off every network, "restricted" puts it on a
// dedicated internal bridge network and "allowed" uses the default bridge.
func (r *Runtime) networkFor(policy string) (string, error) {
	policy = strings.ToLower(strings.TrimSpace(policy))
	if policy == "" {
		policy = r.defaultNetwork
	}
	switch policy {
	case egressNone:
		return "none", nil
	case egressRestricted:
		return restrictedNetwork, nil
	case egressAllowed:
		return "bridge", nil
	default:
		return "", fmt.Errorf("docker: unknown egress policy %q", policy)
	}
}

// ensureRestrictedNetwork creates the dedicated bridge network of the
// "restricted" egress policy when it does not exist yet.
//
// The network is internal, so it has no route to the outside world, and
// inter-container communication is disabled so that two instances of the
// same daemon cannot reach each other.
func (r *Runtime) ensureRestrictedNetwork(ctx context.Context) error {
	exists, err := r.client.NetworkExists(ctx, restrictedNetwork)
	if err != nil {
		return fmt.Errorf("docker: inspect network %s: %w", restrictedNetwork, err)
	}
	if exists {
		return nil
	}
	_, err = r.client.NetworkCreate(ctx, NetworkCreateRequest{
		Name:           restrictedNetwork,
		Driver:         "bridge",
		Internal:       true,
		Attachable:     false,
		EnableIPv6:     false,
		CheckDuplicate: true,
		Options: map[string]string{
			"com.docker.network.bridge.enable_icc":           "false",
			"com.docker.network.bridge.enable_ip_masquerade": "false",
		},
	})
	if err != nil {
		if IsConflict(err) {
			// Another instance created the network first.
			return nil
		}
		return fmt.Errorf("docker: create network %s: %w", restrictedNetwork, err)
	}
	return nil
}

// buildBinds returns the host paths that are exposed to the instance.
//
// Only the instance workspace is bound, and it is bound read-write at
// /workspace. Every other bind has to point inside the workspace root, so a
// recipe can never mount a host directory, a runtime socket or a device.
func (r *Runtime) buildBinds(spec runtime.InstanceSpec, workspace string) ([]string, error) {
	binds := []string{workspace + ":" + workspaceMountPath + ":rw"}
	for _, mount := range spec.Recipe.Mounts {
		if mount.Target == "" {
			return nil, fmt.Errorf("docker: mount %q has no target", mount.Source)
		}
		target := mount.Target
		if !strings.HasPrefix(target, "/") {
			target = "/" + target
		}
		if target == workspaceMountPath || strings.HasPrefix(target, workspaceMountPath+"/") {
			return nil, fmt.Errorf("docker: mount target %s is reserved for the instance workspace", mount.Target)
		}
		source, err := r.resolveHostPath(mount.Source)
		if err != nil {
			return nil, err
		}
		mode := "rw"
		if mount.ReadOnly {
			mode = "ro"
		}
		binds = append(binds, source+":"+target+":"+mode)
	}
	return binds, nil
}

// instanceWorkspace returns the host directory of an instance.
func (r *Runtime) instanceWorkspace(spec runtime.InstanceSpec) (string, error) {
	directory := strings.TrimSpace(spec.Workspace)
	if directory == "" {
		directory = filepath.Join(r.workspaceRoot, sanitizeName(string(spec.ID)))
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("docker: resolve instance workspace: %w", err)
	}
	if err := ensureWithin(r.workspaceRoot, absolute); err != nil {
		return "", fmt.Errorf("docker: instance workspace: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return "", fmt.Errorf("docker: create instance workspace: %w", err)
	}
	return absolute, nil
}

// resolveHostPath maps a mount source onto a host path inside the workspace
// root.
func (r *Runtime) resolveHostPath(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("docker: mount has no source")
	}
	if !filepath.IsAbs(source) {
		return "", fmt.Errorf("docker: mount source %q must be an absolute path", source)
	}
	clean := filepath.Clean(source)
	if err := ensureWithin(r.workspaceRoot, clean); err != nil {
		return "", fmt.Errorf("docker: mount source %q: %w", source, err)
	}
	if err := os.MkdirAll(clean, 0o750); err != nil {
		return "", fmt.Errorf("docker: create mount source %q: %w", source, err)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("docker: resolve mount source %q: %w", source, err)
	}
	if err := ensureWithin(r.workspaceRoot, resolved); err != nil {
		return "", fmt.Errorf("docker: mount source %q: %w", source, err)
	}
	if isRuntimeSocket(resolved) {
		return "", fmt.Errorf("docker: mount source %q is a runtime socket", source)
	}
	return resolved, nil
}

// checkImageAllowed enforces the image policy of the backend.
func (r *Runtime) checkImageAllowed(image string) error {
	if len(r.allowedImages) == 0 {
		return nil
	}
	for _, allowed := range r.allowedImages {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if strings.HasSuffix(allowed, "*") {
			if strings.HasPrefix(image, strings.TrimSuffix(allowed, "*")) {
				return nil
			}
			continue
		}
		if allowed == image {
			return nil
		}
	}
	return fmt.Errorf("docker: image %q is not allowed (allowed: %s)", image, strings.Join(r.allowedImages, ", "))
}

// remember records what the backend created for an instance.
func (r *Runtime) remember(id runtime.ID, info instanceInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances[string(id)] = info
}

// forget drops the record of an instance and returns it.
func (r *Runtime) forget(id runtime.ID) (instanceInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.instances[string(id)]
	if ok {
		delete(r.instances, string(id))
	}
	return info, ok
}

// containerRef returns the Docker reference of an instance.
//
// The in-memory record is authoritative while the daemon runs. After a
// restart the deterministic container name is used instead, so an instance
// that outlived the control plane can still be found.
func (r *Runtime) containerRef(id runtime.ID) string {
	r.mu.Lock()
	info, ok := r.instances[string(id)]
	r.mu.Unlock()
	if ok {
		return info.containerID
	}
	if isContainerID(string(id)) {
		return string(id)
	}
	return containerName(id)
}

// workspaceFor returns the host workspace of an instance.
func (r *Runtime) workspaceFor(id runtime.ID) string {
	r.mu.Lock()
	info, ok := r.instances[string(id)]
	r.mu.Unlock()
	if ok {
		return info.workspace
	}
	return filepath.Join(r.workspaceRoot, sanitizeName(string(id)))
}

// execWorkingDir maps the working directory of a request onto a container
// path.
//
// A relative path is resolved inside the instance workspace. An absolute
// path addresses the container filesystem, which is private to the instance.
func (r *Runtime) execWorkingDir(cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return workspaceMountPath, nil
	}
	if strings.HasPrefix(cwd, "/") {
		return cwd, nil
	}
	relative, err := workspaceRelative(cwd)
	if err != nil {
		return "", err
	}
	return path.Join(workspaceMountPath, relative), nil
}

// observedState maps the daemon state of a container onto the state machine
// of the runtime contract.
//
// The contract has no "stopped" state, so a container that exited cleanly is
// reported as stopping: it is no longer running and awaits cleanup. A
// container that exited with a failure is reported as failed.
func observedState(info *ContainerInspect) runtime.ObservedState {
	observed := runtime.ObservedState{State: runtime.StateFailed}
	if info.State == nil {
		return observed
	}
	state := info.State
	observed.Message = state.Error
	observed.Started, _ = time.Parse(time.RFC3339Nano, state.StartedAt)
	switch state.Status {
	case "running":
		observed.State = runtime.StateReady
	case "created":
		observed.State = runtime.StateStarting
	case "restarting":
		observed.State = runtime.StateStarting
	case "paused", "removing":
		observed.State = runtime.StateStopping
	case "exited":
		observed.State = runtime.StateStopping
		if state.ExitCode != 0 {
			observed.State = runtime.StateFailed
		}
	case "dead":
		observed.State = runtime.StateFailed
	default:
		observed.State = runtime.StateFailed
	}
	if !state.Running && state.Status != "created" {
		code := state.ExitCode
		observed.ExitCode = &code
	}
	return observed
}

// environmentList renders an environment map as the "KEY=value" list the
// Docker API expects. The order is stable so that requests are reproducible.
func environmentList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	list := make([]string, 0, len(env))
	for key, value := range env {
		list = append(list, key+"="+value)
	}
	sort.Strings(list)
	return list
}

// hasCapability reports whether a recipe asks for a capability.
func hasCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), wanted) {
			return true
		}
	}
	return false
}

// containerName derives the Docker container name of an instance.
//
// The name is deterministic so that an instance can still be found after the
// control plane restarted and lost its in-memory state.
func containerName(id runtime.ID) string {
	return "double-agent-" + sanitizeName(string(id))
}

// sanitizeName maps an identifier onto the character set Docker accepts for
// container names.
func sanitizeName(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	name := builder.String()
	if name == "" {
		return "instance"
	}
	// A container name has to start with an alphanumeric character.
	first := name[0]
	if !(first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z' || first >= '0' && first <= '9') {
		name = "i" + name
	}
	return name
}

// isContainerID reports whether the value looks like a container identifier.
func isContainerID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// redact removes the values of a session environment from a message.
//
// The environment of a session can carry secret values, so it must never
// reach a log line or an error message.
func redact(message string, env map[string]string) string {
	for _, value := range env {
		if len(value) < 4 {
			continue
		}
		message = strings.ReplaceAll(message, value, "[redacted]")
	}
	return message
}

// isRuntimeSocket reports whether a path names a runtime socket. Such a path
// is never bound into an instance, not even by accident.
func isRuntimeSocket(path string) bool {
	for _, socket := range []string{
		"/var/run/docker.sock",
		"/run/docker.sock",
		"/run/containerd/containerd.sock",
		"/var/run/containerd/containerd.sock",
		"/run/libvirt/libvirt-sock",
		"/var/run/libvirt/libvirt-sock",
	} {
		if path == socket {
			return true
		}
	}
	return false
}

// ensureWithin reports whether path stays inside root.
func ensureWithin(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("%s is outside %s", path, root)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside %s", path, root)
	}
	return nil
}

// workspaceRelative maps a guest path onto a path relative to the instance
// workspace.
//
// A guest path is either relative to the workspace or absolute inside the
// workspace mount. Anything that would leave the workspace is rejected.
func workspaceRelative(guest string) (string, error) {
	value := strings.TrimSpace(guest)
	if value == "" {
		return "", errors.New("docker: empty path")
	}
	value = filepath.ToSlash(value)
	if strings.HasPrefix(value, "/") {
		if value == workspaceMountPath {
			return "", fmt.Errorf("docker: path %q names the workspace itself", guest)
		}
		if !strings.HasPrefix(value, workspaceMountPath+"/") {
			return "", fmt.Errorf("docker: path %q is outside the instance workspace", guest)
		}
		value = strings.TrimPrefix(value, workspaceMountPath+"/")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", fmt.Errorf("docker: path %q escapes the instance workspace", guest)
		}
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+value), "/")
	if cleaned == "" {
		return "", fmt.Errorf("docker: path %q does not name a file", guest)
	}
	return cleaned, nil
}
