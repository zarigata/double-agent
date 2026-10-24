// Package local implements the development-only host process runtime backend.
//
// The backend runs workloads as ordinary processes of the daemon user. It
// offers no isolation whatsoever: a workload shares the host filesystem, the
// host network and the host process table, and it can read everything the
// daemon user can read.
//
// It exists so that the control plane can be developed and tested on
// machines without a container runtime. It stays disabled until an
// administrator turns it on explicitly, and the doctor command reports it as
// unsupported.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zarigata/double-agent/internal/runtime"
)

const (
	// defaultMaxOutputBytes is the output allowance used when a request
	// does not ask for one.
	defaultMaxOutputBytes int64 = 8 << 20

	// maxArtifactBytes bounds the size of a file copied out of an
	// instance.
	maxArtifactBytes int64 = 1 << 30

	// keepaliveSeconds is how long the placeholder process of an instance
	// sleeps. The instance is stopped by a signal, not by the sleep
	// ending.
	keepaliveSeconds = "2147483647"
)

// errDisabled is returned by every operation of a backend that was not
// enabled by the administrator. It reports an unsupported runtime, because a
// host process backend is never a supported way to run a workload.
var errDisabled = fmt.Errorf("%w: local: the host process backend is disabled; enable runtime.local.enabled to use it", runtime.ErrNotSupported)

// Config configures the development-only host process backend.
type Config struct {
	// Enabled turns the backend on. Every operation fails while it is
	// false, and Health reports the reason.
	Enabled bool
	// WorkspaceRoot is the directory that holds instance workspaces.
	WorkspaceRoot string
}

// Runtime is the host process implementation of runtime.Runtime.
type Runtime struct {
	workspaceRoot string
	enabled       bool

	mu        sync.Mutex
	instances map[string]*instance
}

// instance is the backend view of a running instance.
type instance struct {
	// id is the control plane identifier of the instance.
	id string
	// workspace is the host directory of the instance.
	workspace string
	// state is the lifecycle state of the instance.
	state runtime.State
	// keepalive is the placeholder process that holds the instance open.
	keepalive *os.Process
	// groups holds the process groups of the running guest commands.
	groups map[int]struct{}
}

// New returns a host process backend.
func New(cfg Config) (*Runtime, error) {
	root := strings.TrimSpace(cfg.WorkspaceRoot)
	if root == "" {
		return nil, errors.New("local: workspace root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("local: resolve workspace root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("local: create workspace root: %w", err)
	}
	// The root is resolved so that every later comparison of a host path
	// uses the same spelling.
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("local: resolve workspace root: %w", err)
	}
	absolute = resolved
	return &Runtime{
		workspaceRoot: absolute,
		enabled:       cfg.Enabled,
		instances:     make(map[string]*instance),
	}, nil
}

// Kind reports the runtime family of the backend.
//
// The backend stands in for the container runtime during development, so it
// reports the container family. The doctor command reports the backend
// itself as unsupported.
func (r *Runtime) Kind() runtime.Kind { return runtime.KindContainer }

// Name reports the backend identifier.
func (r *Runtime) Name() string { return "local" }

// Health reports whether the backend may be used.
//
// The backend is a development helper with no isolation, so it reports
// itself as unhealthy until an administrator enables it explicitly.
func (r *Runtime) Health(ctx context.Context) error {
	if !r.enabled {
		return errDisabled
	}
	return nil
}

// Prepare makes the backend ready to host workloads.
func (r *Runtime) Prepare(ctx context.Context, recipe runtime.Recipe) error {
	if !r.enabled {
		return errDisabled
	}
	return nil
}

// Create provisions the workspace of an instance.
func (r *Runtime) Create(ctx context.Context, spec runtime.InstanceSpec) (runtime.Instance, error) {
	if !r.enabled {
		return runtime.Instance{}, errDisabled
	}
	workspace, err := r.workspace(spec)
	if err != nil {
		return runtime.Instance{}, err
	}
	if err := os.MkdirAll(filepath.Join(workspace, "tmp"), 0o750); err != nil {
		return runtime.Instance{}, fmt.Errorf("local: create instance workspace: %w", err)
	}
	inst := &instance{
		id:        string(spec.ID),
		workspace: workspace,
		state:     runtime.StateRequested,
		groups:    make(map[int]struct{}),
	}
	r.mu.Lock()
	if _, exists := r.instances[inst.id]; exists {
		r.mu.Unlock()
		return runtime.Instance{}, fmt.Errorf("%w: instance %s", runtime.ErrAlreadyExists, spec.ID)
	}
	r.instances[inst.id] = inst
	r.mu.Unlock()
	return runtime.Instance{
		ID:      spec.ID,
		Backend: r.Name(),
		Runtime: runtime.ID(inst.id),
		State:   runtime.StateRequested,
		Created: time.Now(),
	}, nil
}

// Start transitions a created instance into a running state.
//
// The instance is represented by a placeholder process. It exists so that
// the instance owns a process group that can be signalled as a whole.
func (r *Runtime) Start(ctx context.Context, id runtime.ID) error {
	if !r.enabled {
		return errDisabled
	}
	inst, err := r.instance(id)
	if err != nil {
		return err
	}
	if inst.keepalive != nil {
		return nil
	}
	cmd := exec.Command("sleep", keepaliveSeconds)
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("local: start instance %s: %w", id, err)
	}
	r.mu.Lock()
	inst.keepalive = cmd.Process
	inst.state = runtime.StateReady
	r.mu.Unlock()
	// The placeholder is reaped in the background; the backend does not
	// wait for it.
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

// Exec runs a command as a host process.
//
// The command is passed as an argument vector and is never handed to a shell,
// so a recipe cannot smuggle a second command into the host. The process runs
// in a process group of its own, which lets a timeout kill the whole tree
// instead of leaving orphans behind.
func (r *Runtime) Exec(ctx context.Context, id runtime.ID, req runtime.ExecRequest) (runtime.ExecHandle, error) {
	if !r.enabled {
		return runtime.ExecHandle{}, errDisabled
	}
	if len(req.Argv) == 0 {
		return runtime.ExecHandle{}, errors.New("local: exec needs an argument vector")
	}
	inst, err := r.instance(id)
	if err != nil {
		return runtime.ExecHandle{}, err
	}
	workingDir, err := r.workingDir(inst, req.Cwd)
	if err != nil {
		return runtime.ExecHandle{}, err
	}
	runCtx := ctx
	cancel := context.CancelFunc(func() {})
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	cmd := exec.CommandContext(runCtx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir = workingDir
	cmd.Env = localEnv(inst.workspace, req.Env)
	cmd.Stdin = req.Stdin
	setProcessGroup(cmd)
	limit := req.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	budget := &outputBudget{limit: limit}
	out := &limitedBuffer{budget: budget}
	errOut := &limitedBuffer{budget: budget}
	// The output is written into buffers that never block, so a command
	// that produces a lot of output cannot deadlock on a full pipe.
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		cancel()
		return runtime.ExecHandle{}, fmt.Errorf("local: start command: %w", err)
	}
	group := cmd.Process.Pid
	r.trackGroup(inst, group)
	finished := make(chan struct{})
	var reaped atomic.Bool
	done := make(chan runtime.ExecResult, 1)
	go func() {
		defer close(done)
		defer cancel()
		defer r.untrackGroup(inst, group)
		result := r.collect(runCtx, cmd, budget, out, errOut, finished, &reaped)
		done <- result
	}()
	// A command that outlives its deadline is killed as a group, so the
	// children of the command die with it. The group is only signalled
	// while the command is still running: the identifier of a group is
	// reused by the host once its leader is gone.
	go func() {
		select {
		case <-runCtx.Done():
			if !reaped.Load() {
				_ = signalProcessGroup(group, os.Kill)
			}
		case <-finished:
		}
	}()
	return runtime.ExecHandle{ID: fmt.Sprintf("%d", group), Done: done}, nil
}

// collect waits for a command and reports its outcome.
func (r *Runtime) collect(ctx context.Context, cmd *exec.Cmd, budget *outputBudget, out, errOut *limitedBuffer, finished chan struct{}, reaped *atomic.Bool) runtime.ExecResult {
	started := time.Now()
	waitErr := cmd.Wait()
	reaped.Store(true)
	close(finished)
	result := runtime.ExecResult{
		Stdout:    out.Bytes(),
		Stderr:    errOut.Bytes(),
		Truncated: budget.truncated,
		Duration:  time.Since(started),
	}
	if waitErr == nil {
		result.ExitCode = 0
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		if ctx.Err() != nil {
			result.ExitCode = -1
			result.Stderr = append(result.Stderr, []byte("local: command timed out\n")...)
		}
		return result
	}
	result.ExitCode = -1
	result.Stderr = append(result.Stderr, []byte(waitErr.Error()+"\n")...)
	return result
}

// Console reports that the backend has no console endpoint.
func (r *Runtime) Console(ctx context.Context, id runtime.ID) (runtime.ConsoleEndpoint, error) {
	return runtime.ConsoleEndpoint{}, fmt.Errorf("%w: the local backend has no console endpoint", runtime.ErrNotSupported)
}

// CopyIn copies a file into the instance workspace.
func (r *Runtime) CopyIn(ctx context.Context, id runtime.ID, src runtime.ArtifactRef, dst runtime.GuestPath) error {
	if !r.enabled {
		return errDisabled
	}
	inst, err := r.instance(id)
	if err != nil {
		return err
	}
	relative, err := workspaceRelative(string(dst))
	if err != nil {
		return err
	}
	info, err := os.Lstat(src.Path)
	if err != nil {
		return fmt.Errorf("local: read artifact %s: %w", src.Path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("local: artifact %s is not a regular file", src.Path)
	}
	target := filepath.Join(inst.workspace, filepath.FromSlash(relative))
	if err := ensureWithin(inst.workspace, target); err != nil {
		return fmt.Errorf("local: copy in: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("local: create workspace directory: %w", err)
	}
	if err := copyFile(src.Path, target, 0o640); err != nil {
		return fmt.Errorf("local: copy in: %w", err)
	}
	return nil
}

// CopyOut copies a file out of the instance workspace.
//
// The copy is written outside of the instance workspace so that the workload
// cannot read it back. The control plane is expected to import the artifact
// into its artifact store right away.
func (r *Runtime) CopyOut(ctx context.Context, id runtime.ID, src runtime.GuestPath) (runtime.ArtifactRef, error) {
	if !r.enabled {
		return runtime.ArtifactRef{}, errDisabled
	}
	inst, err := r.instance(id)
	if err != nil {
		return runtime.ArtifactRef{}, err
	}
	relative, err := workspaceRelative(string(src))
	if err != nil {
		return runtime.ArtifactRef{}, err
	}
	source := filepath.Join(inst.workspace, filepath.FromSlash(relative))
	if err := ensureWithin(inst.workspace, source); err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("local: copy out: %w", err)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("local: read %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return runtime.ArtifactRef{}, fmt.Errorf("local: %s is not a regular file", source)
	}
	if info.Size() > maxArtifactBytes {
		return runtime.ArtifactRef{}, fmt.Errorf("local: %s is larger than the %d byte limit", source, maxArtifactBytes)
	}
	content, err := os.ReadFile(source)
	if err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("local: read %s: %w", source, err)
	}
	destination := filepath.Join(r.workspaceRoot, "exports", sanitizeName(inst.id), filepath.Base(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("local: create export directory: %w", err)
	}
	if err := os.WriteFile(destination, content, 0o640); err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("local: write export %s: %w", destination, err)
	}
	digest := sha256.Sum256(content)
	return runtime.ArtifactRef{
		Path:   destination,
		Name:   filepath.Base(relative),
		MIME:   http.DetectContentType(content),
		Digest: hex.EncodeToString(digest[:]),
		Size:   int64(len(content)),
	}, nil
}

// Stop halts an instance, giving it a grace period to shut down.
func (r *Runtime) Stop(ctx context.Context, id runtime.ID, grace time.Duration) error {
	if !r.enabled {
		return errDisabled
	}
	inst, err := r.instance(id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	groups := make([]int, 0, len(inst.groups))
	for group := range inst.groups {
		groups = append(groups, group)
	}
	keepalive := inst.keepalive
	inst.state = runtime.StateStopping
	r.mu.Unlock()
	for _, group := range groups {
		_ = signalProcessGroup(group, os.Interrupt)
	}
	if keepalive != nil {
		_ = signalProcessGroup(keepalive.Pid, os.Interrupt)
	}
	if grace > 0 {
		select {
		case <-time.After(grace):
		case <-ctx.Done():
		}
	}
	for _, group := range groups {
		_ = signalProcessGroup(group, os.Kill)
	}
	if keepalive != nil {
		_ = signalProcessGroup(keepalive.Pid, os.Kill)
	}
	return nil
}

// Destroy stops an instance and removes the storage that belongs to it.
func (r *Runtime) Destroy(ctx context.Context, id runtime.ID) error {
	if !r.enabled {
		return errDisabled
	}
	inst, err := r.instance(id)
	if err != nil {
		return err
	}
	_ = r.Stop(ctx, id, 0)
	r.mu.Lock()
	delete(r.instances, inst.id)
	r.mu.Unlock()
	if err := os.RemoveAll(inst.workspace); err != nil {
		return fmt.Errorf("local: remove workspace %s: %w", inst.workspace, err)
	}
	return nil
}

// Inspect reports the observed state of an instance.
func (r *Runtime) Inspect(ctx context.Context, id runtime.ID) (runtime.ObservedState, error) {
	if !r.enabled {
		return runtime.ObservedState{}, errDisabled
	}
	inst, err := r.instance(id)
	if err != nil {
		return runtime.ObservedState{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return runtime.ObservedState{State: inst.state}, nil
}

// workspace returns the host directory of an instance.
func (r *Runtime) workspace(spec runtime.InstanceSpec) (string, error) {
	directory := strings.TrimSpace(spec.Workspace)
	if directory == "" {
		directory = filepath.Join(r.workspaceRoot, sanitizeName(string(spec.ID)))
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("local: resolve instance workspace: %w", err)
	}
	if err := ensureWithin(r.workspaceRoot, absolute); err != nil {
		return "", fmt.Errorf("local: instance workspace: %w", err)
	}
	return absolute, nil
}

// instance returns the record of an instance.
func (r *Runtime) instance(id runtime.ID) (*instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[string(id)]
	if !ok {
		return nil, fmt.Errorf("%w: instance %s", runtime.ErrNotFound, id)
	}
	return inst, nil
}

// workingDir maps the working directory of a request onto a host path.
//
// Only paths inside the instance workspace are accepted: the backend has no
// isolation, so a working directory outside the workspace would hand the
// workload the whole host filesystem.
func (r *Runtime) workingDir(inst *instance, cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return inst.workspace, nil
	}
	relative, err := workspaceRelative(cwd)
	if err != nil {
		return "", err
	}
	target := filepath.Join(inst.workspace, filepath.FromSlash(relative))
	if err := ensureWithin(inst.workspace, target); err != nil {
		return "", fmt.Errorf("local: working directory: %w", err)
	}
	if err := os.MkdirAll(target, 0o750); err != nil {
		return "", fmt.Errorf("local: create working directory: %w", err)
	}
	return target, nil
}

// trackGroup records the process group of a running command.
func (r *Runtime) trackGroup(inst *instance, group int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst.groups[group] = struct{}{}
}

// untrackGroup forgets the process group of a finished command.
func (r *Runtime) untrackGroup(inst *instance, group int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(inst.groups, group)
}

// localEnv assembles the environment of a guest process.
//
// The host environment is not inherited: only a small, predictable base is
// used so that host credentials do not leak into the workload.
func localEnv(workspace string, extra map[string]string) []string {
	base := map[string]string{
		"PATH":   "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":   workspace,
		"TMPDIR": filepath.Join(workspace, "tmp"),
	}
	for key, value := range extra {
		base[key] = value
	}
	keys := make([]string, 0, len(base))
	for key := range base {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+base[key])
	}
	return env
}
