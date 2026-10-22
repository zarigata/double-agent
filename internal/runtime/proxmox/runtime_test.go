package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/zarigata/double-agent/internal/runtime"
)

// fakeRequest is one request the fake answered.
type fakeRequest struct {
	// Method and Path identify the call.
	Method string
	Path   string
	// Form holds the parameters of a form encoded call.
	Form url.Values
	// Body holds the parameters of a JSON call.
	Body map[string]any
	// Header holds the headers of the call.
	Header http.Header
}

// fakeGuest is a guest of the fake node.
type fakeGuest struct {
	VMID        int
	Name        string
	Description string
	Status      string
	Template    bool
	Config      map[string]string
}

// fakeProxmox is an in memory stand in for the Proxmox VE API.
//
// It answers the calls the backend uses and records every request, so a test
// can check what the backend asked the server to do. The authentication is
// checked like the server checks it: a call needs a ticket or an API token,
// and a write needs the CSRF token of the ticket.
type fakeProxmox struct {
	mu sync.Mutex

	// requests holds every request the fake answered.
	requests []fakeRequest
	// tickets counts the requests for a ticket.
	tickets int

	// guests are the guests of the node.
	guests map[int]*fakeGuest
	// nextID is the identifier the next clone gets.
	nextID int
	// nodeStatus is the status the node reports.
	nodeStatus string

	// agentAvailable tells whether the guest agent answers.
	agentAvailable bool
	// execExitCode is the exit code the guest agent reports.
	execExitCode int
	// execOut and execErr are the output the guest agent reports.
	execOut string
	execErr string
	// execPolls is the number of polls before a command is reported as
	// finished.
	execPolls int
	// execPIDs counts the commands the guest agent started.
	execPIDs int
	// execPollsByPID counts the polls of every command.
	execPollsByPID map[int]int
	// rejectCalls is the number of calls the fake answers with a
	// rejection, so a test can check that the client replaces its ticket.
	rejectCalls int
}

// newFakeProxmox returns a fake with a template and a free identifier.
func newFakeProxmox() *fakeProxmox {
	return &fakeProxmox{
		guests: map[int]*fakeGuest{
			200: {VMID: 200, Name: "Preset-Alpine", Status: "stopped", Template: true, Config: map[string]string{}},
		},
		nextID:         9001,
		nodeStatus:     "online",
		agentAvailable: true,
		execPolls:      1,
		execPollsByPID: map[int]int{},
	}
}

// ServeHTTP implements http.Handler.
func (f *fakeProxmox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	segments := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, apiPrefix), "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		http.NotFound(w, r)
		return
	}
	if segments[0] == "access" && r.Method == http.MethodPost {
		f.handleTicket(w)
		return
	}
	if !f.authorized(r) {
		f.writeError(w, http.StatusUnauthorized, "authentication failure")
		return
	}
	switch {
	case segments[0] == "version":
		f.writeData(w, map[string]any{"version": "9.2.21", "release": "9.2", "repoid": "test"})
	case segments[0] == "cluster" && len(segments) == 2 && segments[1] == "nextid":
		f.mu.Lock()
		next := f.nextID
		f.mu.Unlock()
		f.writeData(w, strconv.Itoa(next))
	case segments[0] == "nodes" && len(segments) == 1:
		f.writeData(w, f.nodes())
	case segments[0] == "nodes" && len(segments) == 3 && segments[2] == "qemu":
		f.writeData(w, f.list())
	case segments[0] == "nodes" && len(segments) == 5 && segments[2] == "tasks":
		f.writeData(w, map[string]any{"upid": segments[3], "node": segments[1], "status": "stopped", "exitstatus": "OK"})
	case segments[0] == "nodes" && len(segments) == 4 && segments[2] == "qemu":
		f.handleDelete(w, r, segments[3])
	case segments[0] == "nodes" && len(segments) == 5 && segments[2] == "qemu":
		f.handleGuest(w, r, segments[3], segments[4])
	case segments[0] == "nodes" && len(segments) == 6 && segments[2] == "qemu" && segments[4] == "status":
		f.handleStatus(w, r, guestID(segments[3]), segments[5])
	case segments[0] == "nodes" && len(segments) == 6 && segments[2] == "qemu" && segments[4] == "agent":
		f.handleAgent(w, r, guestID(segments[3]))
	default:
		http.NotFound(w, r)
	}
}

// guestID parses the identifier of a guest.
func guestID(value string) int {
	vmid, _ := strconv.Atoi(value)
	return vmid
}

// handleDelete answers the call that removes a guest.
func (f *fakeProxmox) handleDelete(w http.ResponseWriter, r *http.Request, rawVMID string) {
	vmid := guestID(rawVMID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.guests[vmid]; !ok {
		f.writeError(w, http.StatusNotFound, fmt.Sprintf("VM %d does not exist", vmid))
		return
	}
	delete(f.guests, vmid)
	f.writeData(w, f.taskID("qmdestroy", vmid))
}

// nodes returns the nodes of the cluster.
func (f *fakeProxmox) nodes() []map[string]any {
	return []map[string]any{
		{
			"node":   "pve",
			"type":   "node",
			"id":     "node/pve",
			"status": f.nodeStatus,
			"uptime": 1000,
			"maxcpu": 4,
		},
	}
}

// list returns the guests of the node.
func (f *fakeProxmox) list() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	guests := make([]map[string]any, 0, len(f.guests))
	for _, guest := range f.guests {
		template := 0
		if guest.Template {
			template = 1
		}
		guests = append(guests, map[string]any{
			"vmid":     guest.VMID,
			"name":     guest.Name,
			"status":   guest.Status,
			"template": template,
			"cpu":      1,
		})
	}
	return guests
}

// taskID returns the identifier of a task.
func (f *fakeProxmox) taskID(kind string, vmid int) string {
	return fmt.Sprintf("UPID:pve:%08X:00000000:65A1B2C3:%s:%d:root@pam:", vmid, kind, vmid)
}

// writeData answers a call with the data member of the answer.
func (f *fakeProxmox) writeData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// writeError answers a call with a failure.
func (f *fakeProxmox) writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "message": message})
}

// find returns the first request that matches the method and the end of the
// path, or nil when there is none.
func (f *fakeProxmox) find(method, suffix string) *fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.requests {
		request := f.requests[index]
		if request.Method == method && strings.HasSuffix(request.Path, suffix) {
			return &request
		}
	}
	return nil
}

// count returns the number of requests that match the method and the end of
// the path.
func (f *fakeProxmox) count(method, suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, request := range f.requests {
		if request.Method == method && strings.HasSuffix(request.Path, suffix) {
			total++
		}
	}
	return total
}

// isLocalMAC reports whether an address is locally administered and unicast.
//
// The second bit of the first octet marks an address that was handed out by
// the software, and the first bit marks a multicast address.
func isLocalMAC(address string) bool {
	parts := strings.Split(address, ":")
	if len(parts) != 6 {
		return false
	}
	value, err := strconv.ParseUint(parts[0], 16, 8)
	if err != nil {
		return false
	}
	return value&0x02 != 0 && value&0x01 == 0
}

// countContaining returns the number of requests whose path holds the given
// text.
func (f *fakeProxmox) countContaining(method, text string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, request := range f.requests {
		if request.Method == method && strings.Contains(request.Path, text) {
			total++
		}
	}
	return total
}

// newTestRuntime returns a backend that talks to the fake server.
func newTestRuntime(t *testing.T, fake *fakeProxmox, configure func(*Config)) *Runtime {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	cfg := Config{
		Endpoint:      server.URL,
		Username:      "root@pam",
		Password:      "secret",
		Node:          "pve",
		Storage:       "local-lvm",
		TemplateVMID:  200,
		WorkspaceRoot: t.TempDir(),
		HTTPClient:    server.Client(),
	}
	if configure != nil {
		configure(&cfg)
	}
	backend, err := New(cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	return backend
}

// testSpec returns an instance specification for the tests.
func testSpec() runtime.InstanceSpec {
	return runtime.InstanceSpec{
		ID:   "ins_01JTEST",
		Name: "test instance",
		Recipe: runtime.Recipe{
			TemplateID: "debian",
			Image:      "200",
			Runtime:    runtime.KindVM,
			Resources:  runtime.Resources{CPU: 2, MemoryMB: 1024},
		},
		Labels: map[string]string{"job": "job_01JTEST", "user": "usr_01JTEST"},
	}
}

// TestCreateClonesTemplateAndConfiguresGuest checks that an instance becomes
// a full clone of the template with the resources of the recipe.
func TestCreateClonesTemplateAndConfiguresGuest(t *testing.T) {
	fake := newFakeProxmox()
	backend := newTestRuntime(t, fake, nil)
	instance, err := backend.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if instance.Runtime != "9001" {
		t.Errorf("runtime id = %q, want %q", instance.Runtime, "9001")
	}
	if instance.State != runtime.StateReady {
		t.Errorf("state = %q, want %q", instance.State, runtime.StateReady)
	}
	if instance.Backend != "proxmox" {
		t.Errorf("backend = %q, want %q", instance.Backend, "proxmox")
	}
	clone := fake.find(http.MethodPost, "/qemu/200/clone")
	if clone == nil {
		t.Fatal("the backend did not clone the template")
	}
	if clone.Form.Get("newid") != "9001" {
		t.Errorf("newid = %q, want %q", clone.Form.Get("newid"), "9001")
	}
	if clone.Form.Get("full") != "1" {
		t.Errorf("full = %q, want %q", clone.Form.Get("full"), "1")
	}
	if clone.Form.Get("name") != "da-test-instance" {
		t.Errorf("name = %q, want %q", clone.Form.Get("name"), "da-test-instance")
	}
	if clone.Form.Get("target") != "pve" {
		t.Errorf("target = %q, want %q", clone.Form.Get("target"), "pve")
	}
	if clone.Form.Get("storage") != "local-lvm" {
		t.Errorf("storage = %q, want %q", clone.Form.Get("storage"), "local-lvm")
	}
	if !strings.Contains(clone.Form.Get("description"), ownershipMarker) {
		t.Errorf("description = %q, want the ownership marker", clone.Form.Get("description"))
	}
	config := fake.find(http.MethodPut, "/qemu/9001/config")
	if config == nil {
		t.Fatal("the backend did not configure the clone")
	}
	if config.Form.Get("cores") != "2" {
		t.Errorf("cores = %q, want %q", config.Form.Get("cores"), "2")
	}
	if config.Form.Get("memory") != "1024" {
		t.Errorf("memory = %q, want %q", config.Form.Get("memory"), "1024")
	}
	if config.Form.Get("agent") != "1" {
		t.Errorf("agent = %q, want %q", config.Form.Get("agent"), "1")
	}
	network := config.Form.Get("net0")
	if !strings.HasPrefix(network, "virtio=") {
		t.Errorf("net0 = %q, want a virtio device", network)
	}
	if !strings.Contains(network, ",bridge=vmbr0") {
		t.Errorf("net0 = %q, want the configured bridge", network)
	}
	address := strings.TrimPrefix(strings.SplitN(network, ",", 2)[0], "virtio=")
	if !isLocalMAC(address) {
		t.Errorf("the mac address %q is not locally administered", address)
	}
	if fake.find(http.MethodPost, "/qemu/9001/status/start") == nil {
		t.Error("the backend did not start the clone")
	}
	if fake.find(http.MethodPut, "/qemu/200/config") != nil {
		t.Error("the backend wrote the configuration of the template")
	}
}

// TestDestroyRefusesForeignGuest checks that a guest an operator created by
// hand is never deleted.
func TestDestroyRefusesForeignGuest(t *testing.T) {
	fake := newFakeProxmox()
	fake.guests[9002] = &fakeGuest{VMID: 9002, Name: "operator-vm", Status: "running", Config: map[string]string{}}
	backend := newTestRuntime(t, fake, nil)
	err := backend.Destroy(context.Background(), "9002")
	if err == nil {
		t.Fatal("the backend deleted a guest it does not own")
	}
	if !strings.Contains(err.Error(), "ownership marker") {
		t.Errorf("error %q does not mention the ownership marker", err)
	}
	if fake.find(http.MethodDelete, "/qemu/9002") != nil {
		t.Error("the backend sent a delete call for a guest it does not own")
	}
}

// TestDestroyRemovesOwnedGuest checks that a guest of the backend is deleted
// together with its disks.
func TestDestroyRemovesOwnedGuest(t *testing.T) {
	fake := newFakeProxmox()
	fake.guests[9003] = &fakeGuest{
		VMID:        9003,
		Name:        "da-test-instance",
		Description: "double-agent instance=ins_01JTEST",
		Status:      "running",
		Config:      map[string]string{},
	}
	backend := newTestRuntime(t, fake, nil)
	if err := backend.Destroy(context.Background(), "9003"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	remove := fake.find(http.MethodDelete, "/qemu/9003")
	if remove == nil {
		t.Fatal("the backend did not delete the guest")
	}
	if remove.Form.Get("destroy-unreferenced-disks") != "1" {
		t.Errorf("destroy-unreferenced-disks = %q, want %q", remove.Form.Get("destroy-unreferenced-disks"), "1")
	}
	if _, ok := fake.guests[9003]; ok {
		t.Error("the guest survived the delete call")
	}
}

// TestCreateRefusesDisallowedTemplate checks that only the guests of the
// policy of the operator are cloned.
func TestCreateRefusesDisallowedTemplate(t *testing.T) {
	fake := newFakeProxmox()
	backend := newTestRuntime(t, fake, func(cfg *Config) {
		cfg.AllowedTemplates = []int{201}
	})
	spec := testSpec()
	spec.Recipe.Image = "200"
	_, err := backend.Create(context.Background(), spec)
	if err == nil {
		t.Fatal("the backend cloned a guest that is not allowed")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error %q does not mention the policy", err)
	}
	if fake.find(http.MethodPost, "/clone") != nil {
		t.Error("the backend sent a clone call for a guest that is not allowed")
	}
}

// TestPrepareChecksTheTemplate checks that only a template of the node is
// accepted as the source of a clone.
func TestPrepareChecksTheTemplate(t *testing.T) {
	fake := newFakeProxmox()
	backend := newTestRuntime(t, fake, nil)
	ctx := context.Background()
	if err := backend.Prepare(ctx, testSpec().Recipe); err != nil {
		t.Fatalf("prepare of a template: %v", err)
	}
	recipe := testSpec().Recipe
	recipe.Image = "404"
	if err := backend.Prepare(ctx, recipe); err == nil {
		t.Fatal("prepare of a missing template did not fail")
	}
}

// TestExecReturnsTheResultOfTheCommand checks that the answer of the guest
// agent becomes the result of the exec call.
func TestExecReturnsTheResultOfTheCommand(t *testing.T) {
	fake := newFakeProxmox()
	fake.execOut = "hello world\n"
	fake.execErr = "a warning\n"
	fake.execExitCode = 3
	backend := newTestRuntime(t, fake, nil)
	ctx := context.Background()
	instance, err := backend.Create(ctx, testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	handle, err := backend.Exec(ctx, instance.Runtime, runtime.ExecRequest{
		Argv: []string{"echo", "hello world"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	result := <-handle.Done
	if result.ExitCode != 3 {
		t.Errorf("exit code = %d, want %d", result.ExitCode, 3)
	}
	if string(result.Stdout) != "hello world\n" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "hello world\n")
	}
	if string(result.Stderr) != "a warning\n" {
		t.Errorf("stderr = %q, want %q", result.Stderr, "a warning\n")
	}
	exec := fake.find(http.MethodPost, "/agent/exec")
	if exec == nil {
		t.Fatal("the backend did not run the command")
	}
	command, ok := exec.Body["command"].([]any)
	if !ok || len(command) != 2 || command[0] != "echo" || command[1] != "hello world" {
		t.Errorf("command = %v, want the argument vector of the request", exec.Body["command"])
	}
}

// TestExecReportsAMissingGuestAgent checks that a template without the guest
// agent produces an error that names the missing piece.
func TestExecReportsAMissingGuestAgent(t *testing.T) {
	fake := newFakeProxmox()
	fake.agentAvailable = false
	backend := newTestRuntime(t, fake, nil)
	ctx := context.Background()
	instance, err := backend.Create(ctx, testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = backend.Exec(ctx, instance.Runtime, runtime.ExecRequest{Argv: []string{"true"}})
	if err == nil {
		t.Fatal("the backend ran a command without the guest agent")
	}
	if !errors.Is(err, ErrGuestAgentUnavailable) {
		t.Errorf("error %q does not report the missing guest agent", err)
	}
	if !strings.Contains(err.Error(), "qemu-guest-agent") {
		t.Errorf("error %q does not say what has to be installed", err)
	}
}

// TestGuestAgentErrorExplainsTheMissingAgent checks that the message of the
// server, which is the one of a template without the guest agent, is turned
// into an error that names the missing piece.
func TestGuestAgentErrorExplainsTheMissingAgent(t *testing.T) {
	failure := &APIError{
		StatusCode: http.StatusInternalServerError,
		Message:    "QEMU guest agent is not running\n",
		Method:     http.MethodPost,
		Path:       "/nodes/pve/qemu/9001/agent/exec",
	}
	err := guestAgentError(9001, failure)
	if !errors.Is(err, ErrGuestAgentUnavailable) {
		t.Errorf("error %q does not report the missing guest agent", err)
	}
	if !strings.Contains(err.Error(), "qemu-guest-agent") {
		t.Errorf("error %q does not say what has to be installed", err)
	}
	other := &APIError{StatusCode: http.StatusNotFound, Message: "VM 9001 does not exist"}
	if guestAgentError(9001, other) != error(other) {
		t.Error("a failure that has nothing to do with the guest agent was changed")
	}
}

// TestInspectMapsTheStateOfTheGuest checks that the state of the server
// becomes the state of the contract.
func TestInspectMapsTheStateOfTheGuest(t *testing.T) {
	fake := newFakeProxmox()
	fake.guests[9004] = &fakeGuest{VMID: 9004, Name: "da-test", Status: "running", Config: map[string]string{}}
	backend := newTestRuntime(t, fake, nil)
	ctx := context.Background()
	state, err := backend.Inspect(ctx, "9004")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if state.State != runtime.StateReady {
		t.Errorf("state = %q, want %q", state.State, runtime.StateReady)
	}
	fake.guests[9004].Status = "stopped"
	state, err = backend.Inspect(ctx, "9004")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if state.State != runtime.StateDestroyed {
		t.Errorf("state = %q, want %q", state.State, runtime.StateDestroyed)
	}
}

// TestHealthFailsWhenTheServerIsUnreachable checks that a backend without a
// server reports that it is unavailable.
func TestHealthFailsWhenTheServerIsUnreachable(t *testing.T) {
	server := httptest.NewServer(newFakeProxmox())
	endpoint := server.URL
	server.Close()
	backend, err := New(Config{
		Endpoint:      endpoint,
		Username:      "root@pam",
		Password:      "secret",
		Node:          "pve",
		TemplateVMID:  200,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	err = backend.Health(context.Background())
	if err == nil {
		t.Fatal("health of an unreachable server did not fail")
	}
	if !errors.Is(err, runtime.ErrUnavailable) {
		t.Errorf("error %q does not report the backend as unavailable", err)
	}
}

// TestHealthReportsAnOfflineNode checks that a node that is offline makes
// the backend unavailable.
func TestHealthReportsAnOfflineNode(t *testing.T) {
	fake := newFakeProxmox()
	fake.nodeStatus = "offline"
	backend := newTestRuntime(t, fake, nil)
	err := backend.Health(context.Background())
	if err == nil {
		t.Fatal("health of an offline node did not fail")
	}
	if !errors.Is(err, runtime.ErrUnavailable) {
		t.Errorf("error %q does not report the backend as unavailable", err)
	}
}

// TestNewValidatesTheConfiguration checks that a backend without the values
// it needs is refused.
func TestNewValidatesTheConfiguration(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{name: "no endpoint", cfg: Config{Node: "pve", TemplateVMID: 200}},
		{name: "no node", cfg: Config{Endpoint: "https://pve:8006", TemplateVMID: 200}},
		{name: "no template", cfg: Config{Endpoint: "https://pve:8006", Node: "pve"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.cfg); err == nil {
				t.Error("the backend was built from an incomplete configuration")
			}
		})
	}
}

// record notes a request and returns its body.
func (f *fakeProxmox) record(r *http.Request) map[string]any {
	// The form of a request is only parsed when it is asked for.
	_ = r.ParseForm()
	request := fakeRequest{Method: r.Method, Path: r.URL.Path, Form: r.Form, Header: r.Header.Clone()}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		request.Body = body
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	return request.Body
}

// authorized reports whether a call carries the credentials the fake expects.
func (f *fakeProxmox) authorized(r *http.Request) bool {
	f.mu.Lock()
	if f.rejectCalls > 0 {
		f.rejectCalls--
		f.mu.Unlock()
		return false
	}
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "" {
		return true
	}
	if !strings.HasPrefix(r.Header.Get("Cookie"), "PVEAuthCookie=") {
		return false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	// A write needs the CSRF token of the ticket, exactly like the server
	// demands it.
	return r.Header.Get("CSRFPreventionToken") != ""
}

// handleTicket answers the request for a ticket.
func (f *fakeProxmox) handleTicket(w http.ResponseWriter) {
	f.mu.Lock()
	f.tickets++
	f.mu.Unlock()
	f.writeData(w, map[string]any{
		"ticket":              "PVE:root@pam:fake::signature",
		"CSRFPreventionToken": "fake-csrf-token",
		"username":            "root@pam",
	})
}

// handleGuest answers a call that works on a single guest.
func (f *fakeProxmox) handleGuest(w http.ResponseWriter, r *http.Request, rawVMID, action string) {
	vmid, err := strconv.Atoi(rawVMID)
	if err != nil {
		f.writeError(w, http.StatusBadRequest, "the guest id is not a number")
		return
	}
	switch action {
	case "config":
		f.handleConfig(w, r, vmid)
	case "clone":
		f.handleClone(w, r, vmid)
	default:
		f.handleStatus(w, r, vmid, action)
	}
}

// handleConfig answers the calls that read or write the configuration.
func (f *fakeProxmox) handleConfig(w http.ResponseWriter, r *http.Request, vmid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	guest, ok := f.guests[vmid]
	if !ok {
		f.writeError(w, http.StatusNotFound, fmt.Sprintf("VM %d does not exist", vmid))
		return
	}
	if r.Method == http.MethodGet {
		config := map[string]any{
			"vmid":        guest.VMID,
			"name":        guest.Name,
			"description": guest.Description,
		}
		for key, value := range guest.Config {
			config[key] = value
		}
		f.writeData(w, config)
		return
	}
	for key, values := range r.Form {
		if len(values) > 0 {
			guest.Config[key] = values[0]
		}
	}
	if name := r.Form.Get("name"); name != "" {
		guest.Name = name
	}
	if description := r.Form.Get("description"); description != "" {
		guest.Description = description
	}
	f.writeData(w, nil)
}

// handleClone answers the call that copies a guest.
func (f *fakeProxmox) handleClone(w http.ResponseWriter, r *http.Request, source int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.guests[source]; !ok {
		f.writeError(w, http.StatusNotFound, fmt.Sprintf("VM %d does not exist", source))
		return
	}
	newid, err := strconv.Atoi(r.Form.Get("newid"))
	if err != nil || newid <= 0 {
		f.writeError(w, http.StatusBadRequest, "the new guest id is missing")
		return
	}
	guest := &fakeGuest{
		VMID:        newid,
		Name:        r.Form.Get("name"),
		Description: r.Form.Get("description"),
		Status:      "stopped",
		Config:      map[string]string{},
	}
	f.guests[newid] = guest
	f.nextID = newid + 1
	f.writeData(w, f.taskID("qmclone", newid))
}

// handleStatus answers the calls of the run state of a guest.
func (f *fakeProxmox) handleStatus(w http.ResponseWriter, r *http.Request, vmid int, action string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	guest, ok := f.guests[vmid]
	if !ok {
		f.writeError(w, http.StatusNotFound, fmt.Sprintf("VM %d does not exist", vmid))
		return
	}
	switch action {
	case "current":
		f.writeData(w, map[string]any{
			"vmid":      guest.VMID,
			"name":      guest.Name,
			"status":    guest.Status,
			"qmpstatus": guest.Status,
			"cpus":      1,
		})
	case "start":
		guest.Status = "running"
		f.writeData(w, f.taskID("qmstart", vmid))
	case "stop", "shutdown":
		guest.Status = "stopped"
		f.writeData(w, f.taskID("qmstop", vmid))
	case "reset":
		f.writeData(w, f.taskID("qmreset", vmid))
	default:
		f.writeError(w, http.StatusNotFound, "no such endpoint")
	}
}

// handleAgent answers the calls of the guest agent.
func (f *fakeProxmox) handleAgent(w http.ResponseWriter, r *http.Request, vmid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.agentAvailable {
		f.writeError(w, http.StatusInternalServerError, "QEMU guest agent is not running")
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/exec"):
		f.execPIDs++
		f.writeData(w, map[string]any{"pid": 1000 + f.execPIDs})
	case strings.HasSuffix(r.URL.Path, "/exec-status"):
		pid, _ := strconv.Atoi(r.URL.Query().Get("pid"))
		f.execPollsByPID[pid]++
		if f.execPollsByPID[pid] <= f.execPolls {
			f.writeData(w, map[string]any{"exited": 0})
			return
		}
		f.writeData(w, map[string]any{
			"exited":   1,
			"exitcode": f.execExitCode,
			"out-data": f.execOut,
			"err-data": f.execErr,
		})
	default:
		f.writeError(w, http.StatusNotFound, "no such endpoint")
	}
}
