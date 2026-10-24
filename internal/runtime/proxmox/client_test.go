package proxmox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zarigata/double-agent/internal/runtime"
)

// newTestClient returns a client that talks to the fake server.
func newTestClient(t *testing.T, fake *fakeProxmox, configure func(*Config)) *Client {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	cfg := Config{
		Endpoint:   server.URL,
		Username:   "root@pam",
		Password:   "secret",
		Node:       "pve",
		Storage:    "local-lvm",
		HTTPClient: server.Client(),
	}
	if configure != nil {
		configure(&cfg)
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

// TestTicketIsFetchedOnceAndReused checks that the client asks for a ticket
// once and sends it with every call.
func TestTicketIsFetchedOnceAndReused(t *testing.T) {
	fake := newFakeProxmox()
	client := newTestClient(t, fake, nil)
	ctx := context.Background()
	if _, err := client.Version(ctx); err != nil {
		t.Fatalf("version: %v", err)
	}
	if _, err := client.NextID(ctx); err != nil {
		t.Fatalf("next id: %v", err)
	}
	values := url.Values{}
	values.Set("memory", "1024")
	if err := client.SetConfig(ctx, 200, values); err != nil {
		t.Fatalf("set config: %v", err)
	}
	if fake.tickets != 1 {
		t.Errorf("the client asked for %d tickets, want 1", fake.tickets)
	}
	version := fake.find(http.MethodGet, "/version")
	if version == nil {
		t.Fatal("the client did not ask for the version")
	}
	if version.Header.Get("Cookie") != "PVEAuthCookie=PVE:root@pam:fake::signature" {
		t.Errorf("cookie = %q, want the ticket of the session", version.Header.Get("Cookie"))
	}
	if version.Header.Get("CSRFPreventionToken") != "" {
		t.Errorf("a read carried the csrf token %q", version.Header.Get("CSRFPreventionToken"))
	}
	config := fake.find(http.MethodPut, "/config")
	if config == nil {
		t.Fatal("the client did not write the configuration")
	}
	if config.Header.Get("CSRFPreventionToken") != "fake-csrf-token" {
		t.Errorf("csrf token = %q, want the token of the ticket", config.Header.Get("CSRFPreventionToken"))
	}
}

// TestTokenAuthNeedsNoTicket checks that an api token is used as it is and
// that the client never asks for a ticket.
func TestTokenAuthNeedsNoTicket(t *testing.T) {
	fake := newFakeProxmox()
	client := newTestClient(t, fake, func(cfg *Config) {
		cfg.Username = ""
		cfg.Password = ""
		cfg.TokenID = "root@pam!doubleagent"
		cfg.TokenSecret = "the-secret"
	})
	ctx := context.Background()
	if _, err := client.Version(ctx); err != nil {
		t.Fatalf("version: %v", err)
	}
	if fake.tickets != 0 {
		t.Errorf("the client asked for %d tickets, want none", fake.tickets)
	}
	version := fake.find(http.MethodGet, "/version")
	if version == nil {
		t.Fatal("the client did not ask for the version")
	}
	if version.Header.Get("Authorization") != "PVEAPIToken=root@pam!doubleagent=the-secret" {
		t.Errorf("authorization = %q, want the api token", version.Header.Get("Authorization"))
	}
	if version.Header.Get("Cookie") != "" {
		t.Errorf("the call carried the cookie %q", version.Header.Get("Cookie"))
	}
}

// TestRejectedTicketIsReplaced checks that the client fetches a new ticket
// when the server rejects the cached one.
func TestRejectedTicketIsReplaced(t *testing.T) {
	fake := newFakeProxmox()
	fake.rejectCalls = 1
	client := newTestClient(t, fake, nil)
	if _, err := client.Version(context.Background()); err != nil {
		t.Fatalf("version: %v", err)
	}
	if fake.tickets != 2 {
		t.Errorf("the client asked for %d tickets, want 2", fake.tickets)
	}
}

// TestNextIDParsesTheIdentifier checks that the identifier of the server is
// read from the string it answers with.
func TestNextIDParsesTheIdentifier(t *testing.T) {
	fake := newFakeProxmox()
	client := newTestClient(t, fake, nil)
	vmid, err := client.NextID(context.Background())
	if err != nil {
		t.Fatalf("next id: %v", err)
	}
	if vmid != 9001 {
		t.Errorf("next id = %d, want %d", vmid, 9001)
	}
}

// TestCloneCarriesTheSourceInThePath checks that the guest that is copied is
// the guest the call names.
func TestCloneCarriesTheSourceInThePath(t *testing.T) {
	fake := newFakeProxmox()
	client := newTestClient(t, fake, nil)
	upid, err := client.Clone(context.Background(), CloneRequest{
		SourceVMID:  200,
		NewVMID:     9001,
		Name:        "da-test",
		Description: "double-agent instance=ins_01JTEST",
		Target:      "pve",
		Storage:     "local-lvm",
		Full:        true,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if !strings.HasPrefix(upid, "UPID:") {
		t.Errorf("the clone answered with %q, want the identifier of a task", upid)
	}
	clone := fake.find(http.MethodPost, "/qemu/200/clone")
	if clone == nil {
		t.Fatal("the client did not clone the template")
	}
	if clone.Form.Get("newid") != "9001" {
		t.Errorf("newid = %q, want %q", clone.Form.Get("newid"), "9001")
	}
	if clone.Form.Get("full") != "1" {
		t.Errorf("full = %q, want %q", clone.Form.Get("full"), "1")
	}
}

// TestSetConfigOnlyWritesTheGivenValues checks that a property the backend
// does not care about keeps its value.
func TestSetConfigOnlyWritesTheGivenValues(t *testing.T) {
	fake := newFakeProxmox()
	client := newTestClient(t, fake, nil)
	values := url.Values{}
	values.Set("memory", "2048")
	if err := client.SetConfig(context.Background(), 200, values); err != nil {
		t.Fatalf("set config: %v", err)
	}
	config := fake.find(http.MethodPut, "/qemu/200/config")
	if config == nil {
		t.Fatal("the client did not write the configuration")
	}
	if config.Form.Get("memory") != "2048" {
		t.Errorf("memory = %q, want %q", config.Form.Get("memory"), "2048")
	}
	if config.Form.Get("cores") != "" {
		t.Errorf("the call carried the cores %q, want none", config.Form.Get("cores"))
	}
}

// TestDestroyAsksForTheDisksToBeDestroyed checks that the disks of a guest
// are destroyed with it.
func TestDestroyAsksForTheDisksToBeDestroyed(t *testing.T) {
	fake := newFakeProxmox()
	client := newTestClient(t, fake, nil)
	if _, err := client.Destroy(context.Background(), 200); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	remove := fake.find(http.MethodDelete, "/qemu/200")
	if remove == nil {
		t.Fatal("the client did not delete the guest")
	}
	if remove.Form.Get("destroy-unreferenced-disks") != "1" {
		t.Errorf("destroy-unreferenced-disks = %q, want %q", remove.Form.Get("destroy-unreferenced-disks"), "1")
	}
}

// TestAgentExecAsksForAPlainBody checks that the body of the guest agent
// carries nothing the api would reject.
func TestAgentExecAsksForAPlainBody(t *testing.T) {
	fake := newFakeProxmox()
	client := newTestClient(t, fake, nil)
	pid, err := client.AgentExec(context.Background(), 9001, AgentExecRequest{
		Command:   []string{"echo", "hello"},
		InputData: "hello",
	})
	if err != nil {
		t.Fatalf("agent exec: %v", err)
	}
	if pid == 0 {
		t.Error("the guest agent answered with no process id")
	}
	exec := fake.find(http.MethodPost, "/agent/exec")
	if exec == nil {
		t.Fatal("the client did not run the command")
	}
	if _, ok := exec.Body["capture-output"]; ok {
		t.Error("the body carried capture-output, which the api rejects")
	}
	if exec.Body["input-data"] != "hello" {
		t.Errorf("input-data = %v, want the plain text of the request", exec.Body["input-data"])
	}
}

// TestTaskStatusIsPolled checks that the client waits for a task.
func TestTaskStatusIsPolled(t *testing.T) {
	fake := newFakeProxmox()
	client := newTestClient(t, fake, nil)
	upid := "UPID:pve:00001234:00000000:65A1B2C3:qmclone:9001:root@pam:"
	if err := client.WaitForTask(context.Background(), upid); err != nil {
		t.Fatalf("wait for task: %v", err)
	}
	if fake.countContaining(http.MethodGet, "/tasks/") == 0 {
		t.Error("the client did not ask for the state of the task")
	}
}

// TestIsNotFoundReportsAMissingGuest checks that a guest that does not exist
// is recognised.
func TestIsNotFoundReportsAMissingGuest(t *testing.T) {
	fake := newFakeProxmox()
	client := newTestClient(t, fake, nil)
	_, err := client.GetConfig(context.Background(), 404)
	if err == nil {
		t.Fatal("the configuration of a missing guest was read")
	}
	if !IsNotFound(err) {
		t.Errorf("error %q does not report a missing guest", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %q is not an api error", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", apiErr.StatusCode, http.StatusNotFound)
	}
	if !strings.Contains(apiErr.Message, "does not exist") {
		t.Errorf("message = %q, want the reason of the server", apiErr.Message)
	}
}

// TestParseEndpoint checks the rules for the address of the server.
func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		value string
		want  string
		fail  bool
	}{
		{value: "https://192.168.1.79:8006", want: "https://192.168.1.79:8006"},
		{value: "https://192.168.1.79:8006/", want: "https://192.168.1.79:8006"},
		{value: "192.168.1.79:8006", want: "https://192.168.1.79:8006"},
		{value: "http://pve:8006", want: "http://pve:8006"},
		{value: "", fail: true},
		{value: "ftp://pve:8006", fail: true},
	}
	for _, test := range cases {
		got, err := parseEndpoint(test.value)
		if test.fail {
			if err == nil {
				t.Errorf("the endpoint %q was accepted", test.value)
			}
			continue
		}
		if err != nil {
			t.Errorf("the endpoint %q was refused: %v", test.value, err)
			continue
		}
		if got != test.want {
			t.Errorf("the endpoint %q became %q, want %q", test.value, got, test.want)
		}
	}
}

// TestTaskNodeReadsTheNodeOfTheTask checks that the node of a task is read
// from its identifier.
func TestTaskNodeReadsTheNodeOfTheTask(t *testing.T) {
	upid := "UPID:pve:00001234:00000000:65A1B2C3:qmclone:9001:root@pam:"
	if node := taskNode(upid); node != "pve" {
		t.Errorf("node = %q, want %q", node, "pve")
	}
	if node := taskNode("not-a-task"); node != "" {
		t.Errorf("node = %q, want no node", node)
	}
}

// TestGuestNameIsADNSName checks that the name of a guest is accepted by the
// server.
func TestGuestNameIsADNSName(t *testing.T) {
	name := guestName("Test Instance_01")
	if name != "da-test-instance-01" {
		t.Errorf("name = %q, want %q", name, "da-test-instance-01")
	}
	if len(name) > 63 {
		t.Errorf("the name %q is longer than a dns name may be", name)
	}
	long := guestName(strings.Repeat("a", 100))
	if len(long) > 63 {
		t.Errorf("the name %q is longer than a dns name may be", long)
	}
}

// TestCoresForRoundsUp checks that a fraction of a core becomes a whole
// core.
func TestCoresForRoundsUp(t *testing.T) {
	if cores := coresFor(0.5); cores != 1 {
		t.Errorf("cores = %d, want 1", cores)
	}
	if cores := coresFor(2.25); cores != 3 {
		t.Errorf("cores = %d, want 3", cores)
	}
	if cores := coresFor(0); cores != 0 {
		t.Errorf("cores = %d, want 0", cores)
	}
}

// TestCommandLineKeepsTheArgumentVector checks that the environment and the
// working directory do not change the command of a recipe.
func TestCommandLineKeepsTheArgumentVector(t *testing.T) {
	argv := commandLine(runtime.ExecRequest{Argv: []string{"echo", "hello"}})
	if len(argv) != 2 || argv[0] != "echo" || argv[1] != "hello" {
		t.Errorf("argv = %v, want the argument vector of the request", argv)
	}
	argv = commandLine(runtime.ExecRequest{Argv: []string{"echo", "hello"}, Env: map[string]string{"A": "1"}})
	if len(argv) != 4 || argv[0] != "env" || argv[1] != "A=1" {
		t.Errorf("argv = %v, want the environment of the request", argv)
	}
	if argv[2] != "echo" || argv[3] != "hello" {
		t.Errorf("argv = %v, want the command of the request", argv)
	}
}
