package proxmox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zarigata/double-agent/internal/runtime"
)

const (
	// namePrefix marks the guests the backend owns.
	//
	// The prefix is part of the name of every guest the backend creates,
	// so a guest an operator created by hand is never mistaken for a guest
	// of the backend.
	namePrefix = "da-"

	// ownershipMarker is written into the description of every guest the
	// backend creates. It names the owner of the guest, so a guest that
	// lost its name can still be recognised.
	ownershipMarker = "double-agent"

	// defaultBridge is the bridge a guest is attached to when the
	// configuration does not name one.
	defaultBridge = "vmbr0"

	// defaultSCSIHW is the SCSI controller of a guest.
	//
	// The controller of the template is normalised, so the disk layout of
	// a guest does not depend on the template it was cloned from.
	defaultSCSIHW = "virtio-scsi-single"

	// defaultMaxOutputBytes is the output allowance used when a request
	// does not ask for one.
	defaultMaxOutputBytes int64 = 8 << 20

	// defaultAgentReadyTimeout bounds the wait for the guest agent of a
	// freshly started guest. The agent of a guest that boots under software
	// emulation needs minutes, not seconds.
	defaultAgentReadyTimeout = 90 * time.Second

	// agentPollInterval is how often the agent of a guest is pinged while
	// the backend waits for it.
	agentPollInterval = 2 * time.Second

	// maxArtifactBytes bounds the size of a file copied out of an
	// instance.
	maxArtifactBytes int64 = 1 << 30

	// maxInputBytes is the size of the standard input the guest agent
	// accepts. The Proxmox API rejects anything larger.
	maxInputBytes = 64 << 10

	// uploadChunk is the size of a piece of a file that is written into a
	// guest. Base64 makes the piece a third bigger, so it has to stay
	// below the limit of the guest agent.
	uploadChunk = 45 << 10

	// execPollInterval is how often the state of a command inside a guest
	// is polled.
	execPollInterval = 250 * time.Millisecond

	// defaultStopGrace is the time a guest is given to shut down cleanly
	// when the request does not ask for a grace period.
	defaultStopGrace = 30 * time.Second
)

// ErrGuestAgentUnavailable reports that the guest agent of a guest does not
// answer.
//
// The guest agent is the only channel the backend has to run a command
// inside a guest, so a template without the agent can only be used for a
// workload that needs no command at all.
var ErrGuestAgentUnavailable = fmt.Errorf("%w: proxmox: the qemu guest agent is not available", runtime.ErrAgentUnavailable)

// Config configures the Proxmox VE backend.
type Config struct {
	// Endpoint is the address of the pveproxy, e.g.
	// "https://192.168.1.79:8006". A bare host or host:port pair is
	// assumed to be an HTTPS endpoint.
	Endpoint string
	// Username is the Proxmox user, e.g. "root@pam".
	Username string
	// Password is the password of the user.
	Password string
	// TokenID names an API token, e.g. "root@pam!doubleagent". An API
	// token needs no ticket and no CSRF token.
	TokenID string
	// TokenSecret is the secret of the API token.
	TokenSecret string
	// Insecure skips the verification of the TLS certificate of the
	// server. A Proxmox installation uses a self signed certificate, so
	// this is often needed. It stays off by default, because an
	// installation that is reached over a network an attacker controls
	// must not be talked to with an unverified certificate.
	Insecure bool
	// Node is the node the guests are created on, e.g. "pve".
	Node string
	// Storage is the storage the disks of the guests are placed on,
	// e.g. "local-lvm".
	Storage string
	// TemplateVMID is the guest that is cloned for a new instance. It is
	// the default source, a recipe may name another one.
	TemplateVMID int
	// WorkspaceRoot is the directory that holds the instance workspaces.
	// It is the only directory the backend writes exported files to.
	WorkspaceRoot string
	// DefaultNetwork is the bridge the guests are attached to, e.g.
	// "vmbr0".
	DefaultNetwork string
	// AllowedTemplates restricts the guests that may be cloned. An empty
	// list allows every guest, a filled list allows only the guests it
	// names.
	AllowedTemplates []int
	// Bridge is the bridge the guests are attached to. It wins over
	// DefaultNetwork.
	Bridge string
	// Format is the disk format of the clone, e.g. "raw" or "qcow2".
	// An empty format lets Proxmox pick the format of the storage.
	Format string
	// SCSIHW is the SCSI controller of the clone. An empty controller
	// uses the default of the backend.
	SCSIHW string
	// AgentReadyTimeout bounds how long the backend waits for the guest
	// agent of a freshly started guest before a command is refused. Zero
	// uses the default of the backend.
	AgentReadyTimeout time.Duration
	// HTTPClient overrides the HTTP client that reaches the server. It
	// is meant for tests and for callers that need custom TLS settings.
	HTTPClient *http.Client
}

// Runtime is the Proxmox VE implementation of runtime.Runtime.
//
// The backend provisions disposable guests by cloning a template of the
// operator. The template is never changed: the clone gets its own name, its
// own disks and its own network device.
type Runtime struct {
	client           *Client
	node             string
	storage          string
	bridge           string
	format           string
	scsiHW           string
	templateVMID     int
	allowedTemplates map[int]bool
	workspaceRoot    string
	agentReadyWait   time.Duration

	mu        sync.Mutex
	instances map[string]int
}

// New returns a Proxmox VE backend.
//
// The constructor does not contact the server: the first ticket is fetched
// when the first call is made, so a backend can be built while the server is
// still starting.
func New(cfg Config) (*Runtime, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("proxmox: endpoint is required")
	}
	if strings.TrimSpace(cfg.Node) == "" {
		return nil, errors.New("proxmox: node is required")
	}
	if cfg.TemplateVMID <= 0 {
		return nil, errors.New("proxmox: template vmid must be positive")
	}
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(cfg.WorkspaceRoot)
	if root == "" {
		root = filepath.Join(os.TempDir(), "double-agent", "proxmox")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("proxmox: resolve workspace root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("proxmox: create workspace root: %w", err)
	}
	bridge := strings.TrimSpace(cfg.Bridge)
	if bridge == "" {
		bridge = strings.TrimSpace(cfg.DefaultNetwork)
	}
	if bridge == "" {
		bridge = defaultBridge
	}
	scsiHW := strings.TrimSpace(cfg.SCSIHW)
	if scsiHW == "" {
		scsiHW = defaultSCSIHW
	}
	allowed := make(map[int]bool, len(cfg.AllowedTemplates))
	for _, vmid := range cfg.AllowedTemplates {
		allowed[vmid] = true
	}
	agentReadyWait := cfg.AgentReadyTimeout
	if agentReadyWait <= 0 {
		agentReadyWait = defaultAgentReadyTimeout
	}
	return &Runtime{
		client:           client,
		node:             client.Node(),
		storage:          client.Storage(),
		bridge:           bridge,
		format:           strings.TrimSpace(cfg.Format),
		scsiHW:           scsiHW,
		templateVMID:     cfg.TemplateVMID,
		allowedTemplates: allowed,
		workspaceRoot:    absolute,
		agentReadyWait:   agentReadyWait,
		instances:        make(map[string]int),
	}, nil
}

// Kind reports the runtime family of the backend.
//
// A guest of the backend is a virtual machine, so the backend reports the
// virtual machine family.
func (r *Runtime) Kind() runtime.Kind { return runtime.KindVM }

// Name reports the backend identifier.
func (r *Runtime) Name() string { return "proxmox" }

// Prepare makes the backend ready to host the workload of a recipe.
//
// It checks that the template of the recipe exists on the node and that it
// really is a template: a guest that is not a template would be copied with
// its disks, which is not what an operator expects.
func (r *Runtime) Prepare(ctx context.Context, recipe runtime.Recipe) error {
	if recipe.Runtime != "" && recipe.Runtime != runtime.KindVM {
		return fmt.Errorf("proxmox: the backend runs virtual machines, not %q workloads", recipe.Runtime)
	}
	source, err := r.sourceTemplate(recipe)
	if err != nil {
		return err
	}
	if err := r.checkTemplateAllowed(source); err != nil {
		return err
	}
	guests, err := r.client.ListQemu(ctx, r.node)
	if err != nil {
		return fmt.Errorf("proxmox: list the guests of node %s: %w", r.node, err)
	}
	for _, guest := range guests {
		if guest.VMID != source {
			continue
		}
		if !guest.Template.Enabled() {
			return fmt.Errorf("proxmox: guest %d is not a template", source)
		}
		return nil
	}
	return fmt.Errorf("proxmox: template %d does not exist on node %s", source, r.node)
}

// Create provisions a guest for an instance.
//
// The guest is a full clone of the template, so it has its own disks and the
// template of the operator is never touched. The clone is configured with
// the resources of the recipe, gets a network device of its own and the
// guest agent, and is started.
func (r *Runtime) Create(ctx context.Context, spec runtime.InstanceSpec) (runtime.Instance, error) {
	source, err := r.sourceTemplate(spec.Recipe)
	if err != nil {
		return runtime.Instance{}, err
	}
	if err := r.checkTemplateAllowed(source); err != nil {
		return runtime.Instance{}, err
	}
	vmid, err := r.client.NextID(ctx)
	if err != nil {
		return runtime.Instance{}, fmt.Errorf("proxmox: allocate a guest id: %w", err)
	}
	upid, err := r.client.Clone(ctx, CloneRequest{
		SourceVMID:  source,
		NewVMID:     vmid,
		Name:        guestName(spec.Name),
		Description: guestDescription(spec),
		Target:      r.node,
		Storage:     r.storage,
		Format:      r.format,
		Full:        true,
	})
	if err != nil {
		return runtime.Instance{}, fmt.Errorf("proxmox: clone template %d: %w", source, err)
	}
	if err := r.client.WaitForTask(ctx, upid); err != nil {
		return runtime.Instance{}, fmt.Errorf("proxmox: clone template %d: %w", source, err)
	}
	// A guest that cannot be configured or started is of no use to
	// anybody, so it is removed again instead of lingering on the node.
	if err := r.configure(ctx, vmid, spec); err != nil {
		r.discard(ctx, vmid)
		return runtime.Instance{}, err
	}
	if err := r.start(ctx, vmid); err != nil {
		r.discard(ctx, vmid)
		return runtime.Instance{}, err
	}
	r.remember(spec.ID, vmid)
	return runtime.Instance{
		ID:      spec.ID,
		Backend: r.Name(),
		Runtime: runtime.ID(strconv.Itoa(vmid)),
		State:   runtime.StateReady,
		Created: time.Now(),
	}, nil
}

// configure applies the resources of a recipe to a guest.
func (r *Runtime) configure(ctx context.Context, vmid int, spec runtime.InstanceSpec) error {
	values := url.Values{}
	if cores := coresFor(spec.Recipe.Resources.CPU); cores > 0 {
		values.Set("cores", strconv.Itoa(cores))
	}
	if memory := spec.Recipe.Resources.MemoryMB; memory > 0 {
		values.Set("memory", strconv.Itoa(memory))
	}
	values.Set("scsihw", r.scsiHW)
	values.Set("net0", r.networkDevice())
	// The guest agent is the channel the backend runs commands through,
	// so a guest without it could only be used for a workload that needs
	// no command at all.
	values.Set("agent", "1")
	values.Set("name", guestName(spec.Name))
	values.Set("description", guestDescription(spec))
	if err := r.client.SetConfig(ctx, vmid, values); err != nil {
		return fmt.Errorf("proxmox: configure guest %d: %w", vmid, err)
	}
	return nil
}

// networkDevice builds the network device of a guest.
//
// The device gets a freshly generated address, so two guests of the backend
// never share one. The bridge is the one the operator configured: the
// backend does not attach a guest to a network of its own, because a guest
// that is reachable from the outside defeats the purpose of a disposable
// workload.
func (r *Runtime) networkDevice() string {
	mac, err := generateMAC()
	if err != nil {
		// A guest without an address of its own is useless, but there
		// is no way to report the failure here, so Proxmox picks the
		// address.
		return fmt.Sprintf("virtio,bridge=%s", r.bridge)
	}
	return fmt.Sprintf("virtio=%s,bridge=%s", mac, r.bridge)
}

// start starts a guest and waits for the task that starts it.
//
// A guest that already runs is left alone: the control plane starts an
// instance both when it creates it and when it hands it over, and Proxmox,
// unlike a container engine, refuses to start a running guest.
func (r *Runtime) start(ctx context.Context, vmid int) error {
	status, err := r.client.StatusCurrent(ctx, vmid)
	if err == nil {
		switch status.Status {
		case "running", "starting", "prelaunch", "migratepending":
			return nil
		}
	} else if IsNotFound(err) {
		return fmt.Errorf("proxmox: start guest %d: %w", vmid, err)
	}
	upid, err := r.client.Start(ctx, vmid)
	if err != nil {
		return fmt.Errorf("proxmox: start guest %d: %w", vmid, err)
	}
	if err := r.client.WaitForTask(ctx, upid); err != nil {
		// Two starts can race: the guest was started between the check above
		// and this task. A guest that runs now was started, which is all the
		// caller asked for.
		if status, stateErr := r.client.StatusCurrent(ctx, vmid); stateErr == nil && status.Status == "running" {
			return nil
		}
		return fmt.Errorf("proxmox: start guest %d: %w", vmid, err)
	}
	return nil
}

// discard removes a guest the backend created but could not hand over.
//
// The guest carries the ownership marker, so it is removed without the
// check that protects the guests of the operator.
func (r *Runtime) discard(ctx context.Context, vmid int) {
	upid, err := r.client.Destroy(ctx, vmid)
	if err != nil {
		return
	}
	_ = r.client.WaitForTask(ctx, upid)
}

// Start transitions a created instance into a running state.
func (r *Runtime) Start(ctx context.Context, id runtime.ID) error {
	vmid, err := r.vmid(id)
	if err != nil {
		return err
	}
	return r.start(ctx, vmid)
}

// Stop halts an instance, giving it grace to shut down.
//
// The guest is asked to shut down first, so it can stop cleanly. A guest
// that ignores the request is cut off afterwards, because an instance that
// never stops would keep its resources.
func (r *Runtime) Stop(ctx context.Context, id runtime.ID, grace time.Duration) error {
	vmid, err := r.vmid(id)
	if err != nil {
		return err
	}
	if grace <= 0 {
		grace = defaultStopGrace
	}
	status, err := r.client.StatusCurrent(ctx, vmid)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("proxmox: inspect guest %d: %w", vmid, err)
	}
	if status.Status != "running" {
		return nil
	}
	upid, err := r.client.Shutdown(ctx, vmid, int(grace.Seconds()))
	if err == nil {
		if err := r.client.WaitForTask(ctx, upid); err == nil {
			return nil
		}
	}
	// The guest did not shut down in time, so its power is cut.
	upid, err = r.client.Stop(ctx, vmid)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("proxmox: stop guest %d: %w", vmid, err)
	}
	if err := r.client.WaitForTask(ctx, upid); err != nil {
		return fmt.Errorf("proxmox: stop guest %d: %w", vmid, err)
	}
	return nil
}

// Destroy removes an instance and the storage that belongs to it.
//
// Only a guest that carries the ownership marker of the backend is deleted.
// A guest an operator created by hand is never touched, even when the
// control plane asks for it.
func (r *Runtime) Destroy(ctx context.Context, id runtime.ID) error {
	vmid, err := r.vmid(id)
	if err != nil {
		return err
	}
	config, err := r.client.GetConfig(ctx, vmid)
	if err != nil {
		if IsNotFound(err) {
			r.forget(id)
			return nil
		}
		return fmt.Errorf("proxmox: read the configuration of guest %d: %w", vmid, err)
	}
	if !ownedByAgent(config.Name, config.Description) {
		return fmt.Errorf("proxmox: guest %d (%q) does not carry the %s ownership marker, refusing to delete it", vmid, config.Name, ownershipMarker)
	}
	// Proxmox refuses to delete a guest that runs, so it is stopped
	// first.
	if err := r.Stop(ctx, id, defaultStopGrace); err != nil {
		return err
	}
	upid, err := r.client.Destroy(ctx, vmid)
	if err != nil {
		if IsNotFound(err) {
			r.forget(id)
			return nil
		}
		return fmt.Errorf("proxmox: delete guest %d: %w", vmid, err)
	}
	if err := r.client.WaitForTask(ctx, upid); err != nil {
		return fmt.Errorf("proxmox: delete guest %d: %w", vmid, err)
	}
	r.forget(id)
	return nil
}

// Inspect reports the observed state of an instance.
func (r *Runtime) Inspect(ctx context.Context, id runtime.ID) (runtime.ObservedState, error) {
	vmid, err := r.vmid(id)
	if err != nil {
		return runtime.ObservedState{}, err
	}
	status, err := r.client.StatusCurrent(ctx, vmid)
	if err != nil {
		if IsNotFound(err) {
			return runtime.ObservedState{State: runtime.StateDestroyed}, nil
		}
		return runtime.ObservedState{}, fmt.Errorf("proxmox: inspect guest %d: %w", vmid, err)
	}
	return observedState(status), nil
}

// Health reports whether the backend is usable.
//
// The server has to answer and the node of the guests has to be online,
// because a guest that cannot be started on an offline node is of no use.
func (r *Runtime) Health(ctx context.Context) error {
	if _, err := r.client.Version(ctx); err != nil {
		return fmt.Errorf("%w: proxmox: the api is not reachable: %w", runtime.ErrUnavailable, err)
	}
	nodes, err := r.client.Nodes(ctx)
	if err != nil {
		return fmt.Errorf("%w: proxmox: the nodes of the cluster are not reachable: %w", runtime.ErrUnavailable, err)
	}
	for _, node := range nodes {
		if node.Node != r.node {
			continue
		}
		if node.Status != "online" {
			return fmt.Errorf("%w: proxmox: node %s is %s", runtime.ErrUnavailable, r.node, node.Status)
		}
		return nil
	}
	return fmt.Errorf("%w: proxmox: node %s is not part of the cluster", runtime.ErrUnavailable, r.node)
}

// Console reports that the backend has no console endpoint.
//
// Proxmox serves a console over a websocket that needs a ticket of its own.
// The control plane does not proxy it yet, so the backend does not hand out
// a link that would not work.
func (r *Runtime) Console(ctx context.Context, id runtime.ID) (runtime.ConsoleEndpoint, error) {
	return runtime.ConsoleEndpoint{}, fmt.Errorf("%w: the proxmox backend has no console endpoint", runtime.ErrNotSupported)
}

// waitForAgent blocks until the guest agent of a guest answers.
//
// A freshly started guest needs time before its agent is up, and the agent is
// the only channel the backend has, so a command that arrives too early is
// waited for instead of refused. The wait is bounded: a guest whose agent
// never answers still produces the error that names the missing piece.
func (r *Runtime) waitForAgent(ctx context.Context, vmid int) error {
	deadline := time.Now().Add(r.agentReadyWait)
	for {
		err := r.client.AgentPing(ctx, vmid)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrGuestAgentUnavailable) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: guest %d: the agent did not answer within %s: install qemu-guest-agent in the template and let the guest finish booting", ErrGuestAgentUnavailable, vmid, r.agentReadyWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(agentPollInterval):
		}
	}
}

// Exec runs a command inside an instance.
//
// The command is run by the guest agent of the guest, so the template has to
// carry the guest agent. The command is passed as an argument vector and is
// never handed to a shell, so a recipe cannot smuggle a second command into
// the guest.
func (r *Runtime) Exec(ctx context.Context, id runtime.ID, req runtime.ExecRequest) (runtime.ExecHandle, error) {
	if len(req.Argv) == 0 {
		return runtime.ExecHandle{}, errors.New("proxmox: exec needs an argument vector")
	}
	vmid, err := r.vmid(id)
	if err != nil {
		return runtime.ExecHandle{}, err
	}
	if err := r.waitForAgent(ctx, vmid); err != nil {
		return runtime.ExecHandle{}, err
	}
	input, err := readInput(req.Stdin)
	if err != nil {
		return runtime.ExecHandle{}, err
	}
	pid, err := r.client.AgentExec(ctx, vmid, AgentExecRequest{
		Command:   commandLine(req),
		InputData: input,
	})
	if err != nil {
		return runtime.ExecHandle{}, guestAgentError(vmid, err)
	}
	done := make(chan runtime.ExecResult, 1)
	go func() {
		defer close(done)
		done <- r.collectExec(ctx, vmid, pid, req)
	}()
	return runtime.ExecHandle{ID: fmt.Sprintf("%d-%d", vmid, pid), Done: done}, nil
}

// collectExec waits for a command inside a guest and reports its outcome.
func (r *Runtime) collectExec(ctx context.Context, vmid, pid int, req runtime.ExecRequest) runtime.ExecResult {
	started := time.Now()
	limit := req.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	budget := &outputBudget{limit: limit}
	sessionCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		sessionCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()
	for {
		status, err := r.client.AgentExecStatus(sessionCtx, vmid, pid)
		if err != nil {
			return runtime.ExecResult{
				ExitCode: -1,
				Stderr:   []byte(guestAgentError(vmid, err).Error() + "\n"),
				Duration: time.Since(started),
			}
		}
		if status.Exited.Enabled() {
			stdout := budget.take([]byte(status.OutData))
			stderr := budget.take([]byte(status.ErrData))
			result := runtime.ExecResult{
				ExitCode:  int(status.ExitCode),
				Stdout:    stdout,
				Stderr:    stderr,
				Truncated: budget.truncated || status.OutTruncated.Enabled(),
				Duration:  time.Since(started),
			}
			if status.OutTruncated.Enabled() {
				result.Stderr = append(result.Stderr, []byte("proxmox: the guest agent cut the output short\n")...)
			}
			return result
		}
		select {
		case <-sessionCtx.Done():
			return runtime.ExecResult{
				ExitCode: -1,
				Stderr:   []byte("proxmox: the command did not finish before the deadline\n"),
				Duration: time.Since(started),
			}
		case <-time.After(execPollInterval):
		}
	}
}

// CopyIn uploads a file into the guest.
//
// Proxmox has no call that moves a file into a guest, so the file is
// written through the guest agent. The content is handed over as base64, so
// a binary file survives the trip.
func (r *Runtime) CopyIn(ctx context.Context, id runtime.ID, src runtime.ArtifactRef, dst runtime.GuestPath) error {
	vmid, err := r.vmid(id)
	if err != nil {
		return err
	}
	target, err := guestPath(string(dst))
	if err != nil {
		return err
	}
	info, err := os.Stat(src.Path)
	if err != nil {
		return fmt.Errorf("proxmox: read artifact %s: %w", src.Path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("proxmox: artifact %s is not a regular file", src.Path)
	}
	if info.Size() > maxArtifactBytes {
		return fmt.Errorf("proxmox: artifact %s is larger than %d bytes", src.Path, maxArtifactBytes)
	}
	content, err := os.ReadFile(src.Path)
	if err != nil {
		return fmt.Errorf("proxmox: read artifact %s: %w", src.Path, err)
	}
	// The guest agent accepts a limited amount of input at a time, so a
	// big file is written in pieces.
	for offset := 0; offset < len(content); offset += uploadChunk {
		end := offset + uploadChunk
		if end > len(content) {
			end = len(content)
		}
		redirect := ">"
		if offset > 0 {
			redirect = ">>"
		}
		command := []string{"sh", "-c", "base64 -d " + redirect + " " + shellQuote(target)}
		result, err := r.run(ctx, vmid, command, base64.StdEncoding.EncodeToString(content[offset:end]))
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("proxmox: write %s in guest %d: %s", target, vmid, strings.TrimSpace(string(result.Stderr)))
		}
	}
	return nil
}

// CopyOut downloads a file from the guest.
//
// The file is read through the guest agent and written into the workspace
// of the instance, which is the only place the backend writes to.
func (r *Runtime) CopyOut(ctx context.Context, id runtime.ID, src runtime.GuestPath) (runtime.ArtifactRef, error) {
	vmid, err := r.vmid(id)
	if err != nil {
		return runtime.ArtifactRef{}, err
	}
	source, err := guestPath(string(src))
	if err != nil {
		return runtime.ArtifactRef{}, err
	}
	result, err := r.run(ctx, vmid, []string{"base64", source}, "")
	if err != nil {
		return runtime.ArtifactRef{}, err
	}
	if result.ExitCode != 0 {
		return runtime.ArtifactRef{}, fmt.Errorf("proxmox: read %s from guest %d: %s", source, vmid, strings.TrimSpace(string(result.Stderr)))
	}
	content, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(result.Stdout)))
	if err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("proxmox: decode %s from guest %d: %w", source, vmid, err)
	}
	destination := filepath.Join(r.workspaceRoot, "exports", sanitizeName(string(id)), filepath.Base(source))
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("proxmox: create export directory: %w", err)
	}
	if err := os.WriteFile(destination, content, 0o640); err != nil {
		return runtime.ArtifactRef{}, fmt.Errorf("proxmox: write export %s: %w", destination, err)
	}
	digest := sha256.Sum256(content)
	return runtime.ArtifactRef{
		Path:   destination,
		Name:   filepath.Base(source),
		MIME:   http.DetectContentType(content),
		Digest: hex.EncodeToString(digest[:]),
		Size:   int64(len(content)),
	}, nil
}

// run executes a command inside a guest and waits for its outcome.
func (r *Runtime) run(ctx context.Context, vmid int, argv []string, input string) (runtime.ExecResult, error) {
	if err := r.waitForAgent(ctx, vmid); err != nil {
		return runtime.ExecResult{}, err
	}
	pid, err := r.client.AgentExec(ctx, vmid, AgentExecRequest{Command: argv, InputData: input})
	if err != nil {
		return runtime.ExecResult{}, guestAgentError(vmid, err)
	}
	return r.collectExec(ctx, vmid, pid, runtime.ExecRequest{MaxOutputBytes: maxArtifactBytes}), nil
}

// sourceTemplate returns the guest that is cloned for a recipe.
//
// The template catalogue has no field for a Proxmox guest identifier, so the
// image of the recipe carries it: a recipe whose image is a plain number
// names the guest that is cloned, every other recipe uses the template of
// the backend configuration. The catalogue is not changed by this backend,
// so the convention is documented here and in the package comment.
func (r *Runtime) sourceTemplate(recipe runtime.Recipe) (int, error) {
	image := strings.TrimSpace(recipe.Image)
	if image != "" {
		if vmid, err := strconv.Atoi(image); err == nil {
			if vmid <= 0 {
				return 0, fmt.Errorf("proxmox: image %q is not a guest id", image)
			}
			return vmid, nil
		}
	}
	return r.templateVMID, nil
}

// checkTemplateAllowed reports whether a guest may be cloned.
//
// The policy of the operator is the only thing that decides which guests
// are copied, so a workload can never turn a guest of the operator into a
// template of the backend.
func (r *Runtime) checkTemplateAllowed(vmid int) error {
	if len(r.allowedTemplates) == 0 {
		return nil
	}
	if r.allowedTemplates[vmid] {
		return nil
	}
	return fmt.Errorf("proxmox: guest %d is not allowed to be cloned", vmid)
}

// guestName returns the name of the guest of an instance.
//
// The name carries the ownership prefix of the backend, so a guest an
// operator created by hand is never mistaken for a guest of the backend.
// Proxmox only accepts a DNS name, so the name is reduced to that character
// set.
func guestName(name string) string {
	value := dnsName(name)
	if value == "" {
		value = "instance"
	}
	return namePrefix + value
}

// dnsName reduces a name to the character set Proxmox accepts for the name
// of a guest.
func dnsName(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '.', r == '_', r == ' ':
			builder.WriteRune('-')
		}
	}
	value := strings.Trim(builder.String(), "-")
	// The prefix of the backend is part of the name, so the name has to
	// stay inside the length a DNS name may have.
	if len(value) > 60 {
		value = strings.Trim(value[:60], "-")
	}
	return value
}

// guestDescription returns the description of the guest of an instance.
//
// The description carries the ownership marker, so a guest is recognised
// even when its name was changed by hand.
func guestDescription(spec runtime.InstanceSpec) string {
	return fmt.Sprintf("%s instance=%s", ownershipMarker, spec.ID)
}

// ownedByAgent reports whether a guest belongs to the backend.
//
// The name of the guest carries the ownership prefix and the description
// carries the ownership marker. A guest that carries neither was created by
// hand, so the backend never deletes it.
func ownedByAgent(name, description string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), namePrefix) {
		return true
	}
	return strings.Contains(strings.ToLower(description), ownershipMarker)
}

// vmid returns the Proxmox guest identifier of an instance.
//
// The runtime identifier of a guest is its VMID, so the identifier can be
// used directly. The map of the created guests is the fallback for a
// control plane that hands the instance identifier back.
func (r *Runtime) vmid(id runtime.ID) (int, error) {
	if value, err := strconv.Atoi(string(id)); err == nil && value > 0 {
		return value, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if vmid, ok := r.instances[string(id)]; ok {
		return vmid, nil
	}
	return 0, fmt.Errorf("proxmox: instance %q does not name a guest", id)
}

// remember notes the guest of an instance.
func (r *Runtime) remember(id runtime.ID, vmid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances[string(id)] = vmid
}

// forget drops the guest of an instance.
func (r *Runtime) forget(id runtime.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.instances, string(id))
}

// observedState maps the state of a guest onto the state of the contract.
//
// The contract has no state for a guest that is switched off, so a stopped
// guest is reported as destroyed: it holds no resources and is of no use to
// a workload any more.
func observedState(status *QemuStatus) runtime.ObservedState {
	state := runtime.StateFailed
	switch status.Status {
	case "running":
		state = runtime.StateReady
	case "stopped":
		state = runtime.StateDestroyed
	case "paused", "suspended":
		state = runtime.StateStopping
	case "starting", "prelaunch", "migratepending":
		state = runtime.StateStarting
	case "internal-error":
		state = runtime.StateFailed
	}
	return runtime.ObservedState{State: state, Message: status.Status}
}

// generateMAC returns a random address for a network device.
//
// The address is locally administered and unicast, so it never collides
// with the hardware of the operator.
func generateMAC() (string, error) {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("proxmox: generate a mac address: %w", err)
	}
	// The first octet marks the address as locally administered and
	// unicast.
	buffer[0] = buffer[0]&0xfe | 0x02
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", buffer[0], buffer[1], buffer[2], buffer[3], buffer[4], buffer[5]), nil
}

// coresFor returns the number of cores a recipe asks for.
//
// Proxmox counts whole cores, so a fraction of a core is rounded up. A
// recipe that asks for no cpu at all keeps the value of the template.
func coresFor(cpu float64) int {
	if cpu <= 0 {
		return 0
	}
	cores := int(math.Ceil(cpu))
	if cores < 1 {
		cores = 1
	}
	return cores
}

// commandLine builds the argument vector that is handed to the guest agent.
//
// The guest agent of Proxmox has no parameter for the environment or for the
// working directory, so both are expressed with the tools of the guest: the
// environment is set with env(1) and the working directory with a small
// shell wrapper. The command itself is never handed to a shell, so a recipe
// cannot smuggle a second command into the guest.
func commandLine(req runtime.ExecRequest) []string {
	argv := make([]string, 0, len(req.Argv)+len(req.Env)+6)
	if len(req.Env) > 0 {
		argv = append(argv, "env")
		keys := make([]string, 0, len(req.Env))
		for key := range req.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			argv = append(argv, key+"="+req.Env[key])
		}
	}
	if cwd := strings.TrimSpace(req.Cwd); cwd != "" {
		// The wrapper changes into the directory and then runs the
		// command with the arguments it was given.
		argv = append(argv, "sh", "-c", `cd "$1" || exit 1; shift; exec "$@"`, "sh", cwd)
	}
	return append(argv, req.Argv...)
}

// readInput reads the standard input of a command.
//
// The guest agent accepts a limited amount of input, so a bigger stream is
// refused instead of being cut short without a notice.
func readInput(stdin io.Reader) (string, error) {
	if stdin == nil {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(stdin, maxInputBytes+1))
	if err != nil {
		return "", fmt.Errorf("proxmox: read the input of the command: %w", err)
	}
	if len(data) > maxInputBytes {
		return "", fmt.Errorf("proxmox: the guest agent accepts at most %d bytes of input", maxInputBytes)
	}
	return string(data), nil
}

// guestPath validates a path inside a guest.
func guestPath(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errors.New("proxmox: empty path")
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(trimmed, "/"))
	if cleaned == "/" {
		return "", fmt.Errorf("proxmox: path %q does not name a file", value)
	}
	return cleaned, nil
}

// shellQuote quotes a value for a shell.
//
// The value is used as a redirection target, so it has to survive the shell
// of the guest unchanged.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// sanitizeName maps an identifier onto a name that is safe on a filesystem.
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
	return name
}

// guestAgentError explains a failure of the guest agent.
//
// A template without the guest agent is a common mistake, so the error says
// what has to be installed in the template.
func guestAgentError(vmid int, err error) error {
	if isGuestAgentError(err) {
		return fmt.Errorf("%w: guest %d: install qemu-guest-agent in the template and enable it with agent=1: %w", ErrGuestAgentUnavailable, vmid, err)
	}
	return err
}

// isGuestAgentError reports whether a failure was caused by the guest agent.
//
// Proxmox reports a missing guest agent as "QEMU guest agent is not
// running", a guest without the agent channel as "No QEMU guest agent
// configured" and a guest that is off as "VM <id> is not running".
func isGuestAgentError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	message := strings.ToLower(apiErr.Message)
	for _, needle := range []string{"guest agent", "not running"} {
		if strings.Contains(message, needle) {
			return true
		}
	}
	return false
}

// outputBudget bounds the number of bytes kept from the output of a
// command.
//
// The budget is shared by the standard output and the standard error, so the
// limit of a request is the limit of the whole answer.
type outputBudget struct {
	limit     int64
	used      int64
	truncated bool
}

// take keeps as much of the data as the budget allows.
func (b *outputBudget) take(data []byte) []byte {
	if b.used >= b.limit {
		if len(data) > 0 {
			b.truncated = true
		}
		return nil
	}
	room := b.limit - b.used
	if int64(len(data)) > room {
		b.truncated = true
		data = data[:room]
	}
	b.used += int64(len(data))
	return data
}
