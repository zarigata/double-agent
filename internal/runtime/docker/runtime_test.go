package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/runtime"
)

// newTestRuntime returns a backend that talks to the fake daemon.
func newTestRuntime(t *testing.T, daemon *fakeDaemon, configure func(*Config)) *Runtime {
	t.Helper()
	server := httptest.NewServer(daemon)
	t.Cleanup(server.Close)
	cfg := Config{
		Host:          server.URL,
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
			TemplateID: "dev-linux",
			Image:      "alpine:3.19",
			Resources:  runtime.Resources{CPU: 2, MemoryMB: 512},
		},
		Labels: map[string]string{"job": "job_01JTEST", "user": "usr_01JTEST"},
	}
}

// TestPrepareEnforcesAllowedImages checks that only the images of the policy
// may be used.
func TestPrepareEnforcesAllowedImages(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, func(cfg *Config) {
		cfg.AllowedImages = []string{"alpine:3.19"}
	})
	ctx := context.Background()
	recipe := testSpec().Recipe
	if err := backend.Prepare(ctx, recipe); err != nil {
		t.Fatalf("prepare of an allowed image: %v", err)
	}
	recipe.Image = "alpine:3.20"
	err := backend.Prepare(ctx, recipe)
	if err == nil {
		t.Fatal("prepare of a forbidden image did not fail")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error %q does not mention the image policy", err)
	}
}

// TestPreparePullsMissingImage checks that a missing image is pulled when
// the backend is allowed to pull.
func TestPreparePullsMissingImage(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, func(cfg *Config) {
		cfg.PullImages = true
	})
	recipe := testSpec().Recipe
	recipe.Image = "alpine:3.20"
	if err := backend.Prepare(context.Background(), recipe); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, ok := daemon.lastCall(http.MethodPost, "/images/create"); !ok {
		t.Error("the daemon did not receive a pull call")
	}
}

// TestPrepareRefusesMissingImageWithoutPull checks that a missing image is
// an error when pulling is disabled.
func TestPrepareRefusesMissingImageWithoutPull(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, nil)
	recipe := testSpec().Recipe
	recipe.Image = "alpine:3.20"
	if err := backend.Prepare(context.Background(), recipe); err == nil {
		t.Fatal("prepare of a missing image did not fail")
	}
}

// TestCreateRejectsMountOutsideWorkspace checks that a recipe cannot bind a
// host path outside of the workspace root.
func TestCreateRejectsMountOutsideWorkspace(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, nil)
	spec := testSpec()
	spec.Recipe.Mounts = []runtime.Mount{{Source: "/etc", Target: "/host-etc"}}
	_, err := backend.Create(context.Background(), spec)
	if err == nil {
		t.Fatal("create with a mount outside the workspace did not fail")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("error %q does not mention the escape", err)
	}
	if _, ok := daemon.lastCall(http.MethodPost, "/containers/create"); ok {
		t.Error("the daemon received a create call for a rejected recipe")
	}
}

// TestCreateRejectsWorkspaceOutsideRoot checks that an instance workspace
// outside of the workspace root is rejected.
func TestCreateRejectsWorkspaceOutsideRoot(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, nil)
	spec := testSpec()
	spec.Workspace = t.TempDir()
	if _, err := backend.Create(context.Background(), spec); err == nil {
		t.Fatal("create with a workspace outside the root did not fail")
	}
}

// TestCreateSelectsNetworkPerEgressPolicy checks the network mode of every
// egress policy.
func TestCreateSelectsNetworkPerEgressPolicy(t *testing.T) {
	cases := []struct {
		egress string
		want   string
	}{
		{egress: "none", want: "none"},
		{egress: "restricted", want: restrictedNetwork},
		{egress: "allowed", want: "bridge"},
	}
	for _, test := range cases {
		daemon := newFakeDaemon()
		backend := newTestRuntime(t, daemon, nil)
		spec := testSpec()
		spec.Recipe.Network = runtime.NetworkPolicy{Egress: test.egress}
		if _, err := backend.Create(context.Background(), spec); err != nil {
			t.Fatalf("create with egress %q: %v", test.egress, err)
		}
		daemon.mu.Lock()
		mode := daemon.createHostConfig["NetworkMode"]
		daemon.mu.Unlock()
		if mode != test.want {
			t.Errorf("egress %q used network %v, want %q", test.egress, mode, test.want)
		}
	}
}

// TestCreateUsesDefaultNetwork checks that the configured default applies
// when the recipe does not ask for a policy.
func TestCreateUsesDefaultNetwork(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, func(cfg *Config) {
		cfg.DefaultNetwork = "restricted"
	})
	if _, err := backend.Create(context.Background(), testSpec()); err != nil {
		t.Fatalf("create: %v", err)
	}
	daemon.mu.Lock()
	mode := daemon.createHostConfig["NetworkMode"]
	daemon.mu.Unlock()
	if mode != restrictedNetwork {
		t.Errorf("network = %v, want %q", mode, restrictedNetwork)
	}
}

// TestCreateDropsCapabilities checks the isolation settings of a container.
func TestCreateDropsCapabilities(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, nil)
	spec := testSpec()
	if _, err := backend.Create(context.Background(), spec); err != nil {
		t.Fatalf("create: %v", err)
	}
	daemon.mu.Lock()
	host := daemon.createHostConfig
	daemon.mu.Unlock()
	drop, _ := host["CapDrop"].([]any)
	if len(drop) != 1 || drop[0] != "ALL" {
		t.Errorf("CapDrop = %v, want [ALL]", host["CapDrop"])
	}
	add, _ := host["CapAdd"].([]any)
	if len(add) != len(minimalCapabilities) {
		t.Fatalf("CapAdd = %v, want %v", host["CapAdd"], minimalCapabilities)
	}
	for _, capability := range minimalCapabilities {
		found := false
		for _, granted := range add {
			if granted == capability {
				found = true
			}
		}
		if !found {
			t.Errorf("CapAdd = %v, want it to contain %q", host["CapAdd"], capability)
		}
	}
	if host["Privileged"] != false {
		t.Errorf("Privileged = %v, want false", host["Privileged"])
	}
	if host["AutoRemove"] != false {
		t.Errorf("AutoRemove = %v, want false", host["AutoRemove"])
	}
	security, _ := host["SecurityOpt"].([]any)
	if len(security) == 0 || security[0] != "no-new-privileges:true" {
		t.Errorf("SecurityOpt = %v, want the no-new-privileges option", host["SecurityOpt"])
	}
	if host["Memory"].(float64) != 512*1024*1024 {
		t.Errorf("Memory = %v, want 512 MiB", host["Memory"])
	}
	if host["MemorySwap"].(float64) != 512*1024*1024 {
		t.Errorf("MemorySwap = %v, want the memory limit", host["MemorySwap"])
	}
	if host["NanoCpus"].(float64) != 2e9 {
		t.Errorf("NanoCpus = %v, want 2000000000", host["NanoCpus"])
	}
	if host["PidsLimit"].(float64) != float64(defaultPidsLimit) {
		t.Errorf("PidsLimit = %v, want %d", host["PidsLimit"], defaultPidsLimit)
	}
}

// TestCreateBindsOnlyTheWorkspace checks the binds of a container.
func TestCreateBindsOnlyTheWorkspace(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, nil)
	root := backend.workspaceRoot
	spec := testSpec()
	spec.Recipe.Mounts = []runtime.Mount{{Source: filepath.Join(root, "data"), Target: "/data", ReadOnly: true}}
	if _, err := backend.Create(context.Background(), spec); err != nil {
		t.Fatalf("create: %v", err)
	}
	daemon.mu.Lock()
	binds, _ := daemon.createHostConfig["Binds"].([]any)
	labels := daemon.createBody.Labels
	daemon.mu.Unlock()
	if len(binds) != 2 {
		t.Fatalf("binds = %v, want the workspace and one mount", binds)
	}
	workspace := filepath.Join(root, "ins_01JTEST")
	if binds[0] != workspace+":/workspace:rw" {
		t.Errorf("first bind = %v, want the instance workspace", binds[0])
	}
	if binds[1] != filepath.Join(root, "data")+":/data:ro" {
		t.Errorf("second bind = %v, want the read-only data mount", binds[1])
	}
	for _, bind := range binds {
		if strings.Contains(bind.(string), "docker.sock") {
			t.Errorf("bind %v exposes a runtime socket", bind)
		}
	}
	if labels[LabelInstance] != "ins_01JTEST" {
		t.Errorf("label %s = %q, want the instance identifier", LabelInstance, labels[LabelInstance])
	}
	if labels[LabelJob] != "job_01JTEST" {
		t.Errorf("label %s = %q, want the job identifier", LabelJob, labels[LabelJob])
	}
	if labels[LabelUser] != "usr_01JTEST" {
		t.Errorf("label %s = %q, want the user identifier", LabelUser, labels[LabelUser])
	}
}

// TestExecTruncatesOutput checks that the output allowance of a request is
// enforced.
func TestExecTruncatesOutput(t *testing.T) {
	daemon := newFakeDaemon()
	daemon.execStdout = []byte(strings.Repeat("x", 4096))
	backend := newTestRuntime(t, daemon, nil)
	spec := testSpec()
	instance, err := backend.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	handle, err := backend.Exec(context.Background(), instance.ID, runtime.ExecRequest{
		Argv:           []string{"/bin/sh", "-c", "echo hi"},
		MaxOutputBytes: 100,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	result := <-handle.Done
	if !result.Truncated {
		t.Error("the result is not marked as truncated")
	}
	if len(result.Stdout) != 100 {
		t.Errorf("stdout holds %d bytes, want 100", len(result.Stdout))
	}
}

// TestExecKeepsSecretOutOfTheResult checks that the environment of a session
// never reaches the result of an exec.
func TestExecKeepsSecretOutOfTheResult(t *testing.T) {
	daemon := newFakeDaemon()
	daemon.execStderr = []byte("failed\n")
	backend := newTestRuntime(t, daemon, nil)
	instance, err := backend.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	handle, err := backend.Exec(context.Background(), instance.ID, runtime.ExecRequest{
		Argv:       []string{"/bin/sh", "-c", "echo hi"},
		Env:        map[string]string{"TOKEN": "super-secret-value"},
		SecretRefs: []string{"secret/github-read-token"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	result := <-handle.Done
	if strings.Contains(string(result.Stdout)+string(result.Stderr), "super-secret-value") {
		t.Error("the secret value reached the result of the exec")
	}
	if strings.Contains(string(result.Stdout)+string(result.Stderr), "secret/github-read-token") {
		t.Error("the secret reference reached the result of the exec")
	}
	daemon.mu.Lock()
	env := daemon.execRequest.Env
	daemon.mu.Unlock()
	if len(env) != 1 || env[0] != "TOKEN=super-secret-value" {
		t.Errorf("session environment = %v, want the secret value", env)
	}
}

// TestCopyInAndCopyOut checks the file transfer of an instance.
func TestCopyInAndCopyOut(t *testing.T) {
	daemon := newFakeDaemon()
	daemon.storedName = "notes.txt"
	daemon.storedFile = []byte("hello from the instance")
	backend := newTestRuntime(t, daemon, nil)
	instance, err := backend.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	source := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(source, []byte("upload body"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := backend.CopyIn(context.Background(), instance.ID, runtime.ArtifactRef{Path: source, Name: "upload.txt"}, "upload.txt"); err != nil {
		t.Fatalf("copy in: %v", err)
	}
	daemon.mu.Lock()
	archive := daemon.archive
	daemon.mu.Unlock()
	if len(archive) == 0 {
		t.Fatal("the daemon did not receive an archive")
	}
	if !strings.Contains(string(archive), "upload body") {
		t.Error("the archive does not hold the uploaded content")
	}
	artifact, err := backend.CopyOut(context.Background(), instance.ID, "notes.txt")
	if err != nil {
		t.Fatalf("copy out: %v", err)
	}
	if artifact.Name != "notes.txt" {
		t.Errorf("artifact name = %q, want notes.txt", artifact.Name)
	}
	if artifact.Size != int64(len("hello from the instance")) {
		t.Errorf("artifact size = %d, want %d", artifact.Size, len("hello from the instance"))
	}
	if artifact.Digest == "" {
		t.Error("the artifact has no digest")
	}
	content, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(content) != "hello from the instance" {
		t.Errorf("artifact content = %q", content)
	}
}

// TestCopyRejectsPathEscape checks that a path cannot leave the workspace.
func TestCopyRejectsPathEscape(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, nil)
	instance, err := backend.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := backend.CopyOut(context.Background(), instance.ID, "../../etc/passwd"); err == nil {
		t.Fatal("copy out of the workspace did not fail")
	}
	if err := backend.CopyIn(context.Background(), instance.ID, runtime.ArtifactRef{Path: "/etc/hostname"}, "/workspace/../escape"); err == nil {
		t.Fatal("copy in outside of the workspace did not fail")
	}
}

// TestDestroyRemovesWorkspace checks that the storage of an instance is
// removed with it.
func TestDestroyRemovesWorkspace(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, nil)
	instance, err := backend.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	workspace := filepath.Join(backend.workspaceRoot, "ins_01JTEST")
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("the instance workspace was not created: %v", err)
	}
	if err := backend.Destroy(context.Background(), instance.ID); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Errorf("the instance workspace still exists: %v", err)
	}
	if _, ok := daemon.lastCall(http.MethodDelete, "/containers/container-1"); !ok {
		t.Error("the daemon did not receive a remove call")
	}
}

// TestInspectMapsContainerState checks the mapping of the daemon states.
func TestInspectMapsContainerState(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, nil)
	instance, err := backend.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	observed, err := backend.Inspect(context.Background(), instance.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if observed.State != runtime.StateReady {
		t.Errorf("state = %q, want %q", observed.State, runtime.StateReady)
	}
	daemon.mu.Lock()
	daemon.inspectState = ContainerState{Status: "exited", ExitCode: 2}
	daemon.mu.Unlock()
	observed, err = backend.Inspect(context.Background(), instance.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if observed.State != runtime.StateFailed {
		t.Errorf("state = %q, want %q", observed.State, runtime.StateFailed)
	}
	if observed.ExitCode == nil || *observed.ExitCode != 2 {
		t.Errorf("exit code = %v, want 2", observed.ExitCode)
	}
}

// TestHealthReportsUnreachableDaemon checks the health report of a backend
// without a daemon.
func TestHealthReportsUnreachableDaemon(t *testing.T) {
	backend := newTestRuntime(t, newFakeDaemon(), nil)
	if err := backend.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	broken := newTestRuntime(t, newFakeDaemon(), func(cfg *Config) {
		cfg.Host = "http://127.0.0.1:1"
	})
	err := broken.Health(context.Background())
	if err == nil {
		t.Fatal("health of an unreachable daemon did not fail")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("error %q does not mention the daemon", err)
	}
}

// TestStopAndConsole checks the remaining operations of the backend.
func TestStopAndConsole(t *testing.T) {
	daemon := newFakeDaemon()
	backend := newTestRuntime(t, daemon, nil)
	instance, err := backend.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := backend.Stop(context.Background(), instance.ID, 3*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	stop, ok := daemon.lastCall(http.MethodPost, "/stop")
	if !ok {
		t.Fatal("the daemon did not receive a stop call")
	}
	if !strings.Contains(stop.Query, "t=3") {
		t.Errorf("stop query = %q, want the grace period", stop.Query)
	}
	if _, err := backend.Console(context.Background(), instance.ID); err == nil {
		t.Fatal("console did not fail")
	} else if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error %q does not report an unsupported operation", err)
	}
}
