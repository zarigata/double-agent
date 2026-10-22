package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/runtime"
)

// newTestRuntime returns a host process backend with a temporary workspace.
func newTestRuntime(t *testing.T, enabled bool) *Runtime {
	t.Helper()
	backend, err := New(Config{Enabled: enabled, WorkspaceRoot: t.TempDir()})
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
			Resources:  runtime.Resources{CPU: 1, MemoryMB: 256},
		},
	}
}

// TestLifecycle runs the whole lifecycle of an instance.
func TestLifecycle(t *testing.T) {
	backend := newTestRuntime(t, true)
	ctx := context.Background()
	instance, err := backend.Create(ctx, testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if instance.Backend != "local" {
		t.Errorf("backend = %q, want local", instance.Backend)
	}
	if err := backend.Start(ctx, instance.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	observed, err := backend.Inspect(ctx, instance.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if observed.State != runtime.StateReady {
		t.Errorf("state = %q, want %q", observed.State, runtime.StateReady)
	}
	handle, err := backend.Exec(ctx, instance.ID, runtime.ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo hi"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	result := <-handle.Done
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if strings.TrimSpace(string(result.Stdout)) != "hi" {
		t.Errorf("stdout = %q, want hi", result.Stdout)
	}
	if err := backend.Stop(ctx, instance.ID, 100*time.Millisecond); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := backend.Destroy(ctx, instance.ID); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	workspace := filepath.Join(backend.workspaceRoot, "ins_01JTEST")
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Errorf("the instance workspace still exists: %v", err)
	}
}

// TestExecDoesNotUseAShell checks that the argument vector is passed to the
// process as it is.
func TestExecDoesNotUseAShell(t *testing.T) {
	backend := newTestRuntime(t, true)
	ctx := context.Background()
	instance, err := backend.Create(ctx, testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := backend.Start(ctx, instance.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	handle, err := backend.Exec(ctx, instance.ID, runtime.ExecRequest{
		Argv: []string{"/bin/echo", "$HOME"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	result := <-handle.Done
	if strings.TrimSpace(string(result.Stdout)) != "$HOME" {
		t.Errorf("stdout = %q, want the literal $HOME", result.Stdout)
	}
	_ = backend.Destroy(ctx, instance.ID)
}

// TestExecRunsInTheWorkspace checks the working directory of a command.
func TestExecRunsInTheWorkspace(t *testing.T) {
	backend := newTestRuntime(t, true)
	ctx := context.Background()
	instance, err := backend.Create(ctx, testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := backend.Start(ctx, instance.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	handle, err := backend.Exec(ctx, instance.ID, runtime.ExecRequest{
		Argv: []string{"/bin/pwd"},
		Env:  map[string]string{"GREETING": "hello"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	result := <-handle.Done
	workspace := filepath.Join(backend.workspaceRoot, "ins_01JTEST")
	if strings.TrimSpace(string(result.Stdout)) != workspace {
		t.Errorf("working directory = %q, want %q", result.Stdout, workspace)
	}
	handle, err = backend.Exec(ctx, instance.ID, runtime.ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo $GREETING"},
		Env:  map[string]string{"GREETING": "hello"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	result = <-handle.Done
	if strings.TrimSpace(string(result.Stdout)) != "hello" {
		t.Errorf("stdout = %q, want hello", result.Stdout)
	}
	_ = backend.Destroy(ctx, instance.ID)
}

// TestExecTimeout checks that a command that outlives its deadline is
// stopped.
func TestExecTimeout(t *testing.T) {
	backend := newTestRuntime(t, true)
	ctx := context.Background()
	instance, err := backend.Create(ctx, testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := backend.Start(ctx, instance.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	started := time.Now()
	handle, err := backend.Exec(ctx, instance.ID, runtime.ExecRequest{
		Argv:    []string{"/bin/sh", "-c", "sleep 30"},
		Timeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	result := <-handle.Done
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the command ran for %s, want it to be stopped", elapsed)
	}
	if result.ExitCode == 0 {
		t.Errorf("exit code = %d, want a failure", result.ExitCode)
	}
	if !strings.Contains(string(result.Stderr), "timed out") {
		t.Errorf("stderr = %q, want a timeout report", result.Stderr)
	}
	_ = backend.Destroy(ctx, instance.ID)
}

// TestExecTruncatesOutput checks that the output allowance of a request is
// enforced.
func TestExecTruncatesOutput(t *testing.T) {
	backend := newTestRuntime(t, true)
	ctx := context.Background()
	instance, err := backend.Create(ctx, testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := backend.Start(ctx, instance.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	handle, err := backend.Exec(ctx, instance.ID, runtime.ExecRequest{
		Argv:           []string{"/bin/sh", "-c", "head -c 4096 /dev/zero | tr '\\0' 'x'"},
		MaxOutputBytes: 64,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	result := <-handle.Done
	if !result.Truncated {
		t.Error("the result is not marked as truncated")
	}
	if len(result.Stdout) != 64 {
		t.Errorf("stdout holds %d bytes, want 64", len(result.Stdout))
	}
	_ = backend.Destroy(ctx, instance.ID)
}

// TestCopyInAndCopyOut checks the file transfer of an instance.
func TestCopyInAndCopyOut(t *testing.T) {
	backend := newTestRuntime(t, true)
	ctx := context.Background()
	instance, err := backend.Create(ctx, testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := backend.Start(ctx, instance.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	source := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(source, []byte("upload body"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := backend.CopyIn(ctx, instance.ID, runtime.ArtifactRef{Path: source, Name: "upload.txt"}, "upload.txt"); err != nil {
		t.Fatalf("copy in: %v", err)
	}
	copied := filepath.Join(backend.workspaceRoot, "ins_01JTEST", "upload.txt")
	content, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if string(content) != "upload body" {
		t.Errorf("copied content = %q, want %q", content, "upload body")
	}
	artifact, err := backend.CopyOut(ctx, instance.ID, "upload.txt")
	if err != nil {
		t.Fatalf("copy out: %v", err)
	}
	if artifact.Name != "upload.txt" {
		t.Errorf("artifact name = %q, want upload.txt", artifact.Name)
	}
	if artifact.Size != int64(len("upload body")) {
		t.Errorf("artifact size = %d, want %d", artifact.Size, len("upload body"))
	}
	if artifact.Digest == "" {
		t.Error("the artifact has no digest")
	}
	exported, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(exported) != "upload body" {
		t.Errorf("artifact content = %q", exported)
	}
	_ = backend.Destroy(ctx, instance.ID)
}

// TestCopyRejectsPathEscape checks that a path cannot leave the workspace.
func TestCopyRejectsPathEscape(t *testing.T) {
	backend := newTestRuntime(t, true)
	ctx := context.Background()
	instance, err := backend.Create(ctx, testSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := backend.CopyOut(ctx, instance.ID, "../../etc/passwd"); err == nil {
		t.Fatal("copy out of the workspace did not fail")
	}
	if err := backend.CopyIn(ctx, instance.ID, runtime.ArtifactRef{Path: "/etc/hostname"}, "/etc/hostname"); err == nil {
		t.Fatal("copy in outside of the workspace did not fail")
	}
	_ = backend.Destroy(ctx, instance.ID)
}

// TestDisabledBackendRefusesWork checks that a backend that was not enabled
// refuses every operation.
func TestDisabledBackendRefusesWork(t *testing.T) {
	backend := newTestRuntime(t, false)
	ctx := context.Background()
	err := backend.Health(ctx)
	if err == nil {
		t.Fatal("health of a disabled backend did not fail")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error %q does not report the backend as disabled", err)
	}
	if _, err := backend.Create(ctx, testSpec()); err == nil {
		t.Fatal("create on a disabled backend did not fail")
	}
	if err := backend.Prepare(ctx, testSpec().Recipe); err == nil {
		t.Fatal("prepare on a disabled backend did not fail")
	}
}

// TestUnknownInstanceIsReported checks the error of an unknown instance.
func TestUnknownInstanceIsReported(t *testing.T) {
	backend := newTestRuntime(t, true)
	ctx := context.Background()
	if _, err := backend.Exec(ctx, "ins_missing", runtime.ExecRequest{Argv: []string{"/bin/echo", "hi"}}); err == nil {
		t.Fatal("exec on an unknown instance did not fail")
	}
	if _, err := backend.Inspect(ctx, "ins_missing"); err == nil {
		t.Fatal("inspect of an unknown instance did not fail")
	}
}
