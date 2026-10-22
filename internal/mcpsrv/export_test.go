package mcpsrv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
)

// fakeRuntime is a backend that hands out a file instead of running a
// workload.
//
// The copy lands outside of the workspace of the guest, exactly like the
// docker backend does it, so the test proves that the exported file is not
// readable from the instance.
type fakeRuntime struct {
	// root is the directory that holds the exported files.
	root string
	// content is the content of the exported file.
	content []byte
	// name is the name of the exported file.
	name string
	// copied records the paths the control plane asked for.
	copied []string
}

// Kind reports the runtime family of the backend.
func (f *fakeRuntime) Kind() runtime.Kind { return runtime.KindContainer }

// Name reports the identifier of the backend.
func (f *fakeRuntime) Name() string { return "fake" }

// Prepare makes the backend ready.
func (f *fakeRuntime) Prepare(ctx context.Context, recipe runtime.Recipe) error { return nil }

// Create provisions an instance.
func (f *fakeRuntime) Create(ctx context.Context, spec runtime.InstanceSpec) (runtime.Instance, error) {
	return runtime.Instance{
		ID:      spec.ID,
		Backend: f.Name(),
		Runtime: runtime.ID("fake-" + string(spec.ID)),
		State:   runtime.StateReady,
	}, nil
}

// Start transitions an instance into a running state.
func (f *fakeRuntime) Start(ctx context.Context, id runtime.ID) error { return nil }

// Exec runs a command inside an instance.
func (f *fakeRuntime) Exec(ctx context.Context, id runtime.ID, req runtime.ExecRequest) (runtime.ExecHandle, error) {
	return runtime.ExecHandle{}, runtime.ErrNotSupported
}

// Console reports that the backend has no console.
func (f *fakeRuntime) Console(ctx context.Context, id runtime.ID) (runtime.ConsoleEndpoint, error) {
	return runtime.ConsoleEndpoint{}, runtime.ErrNotSupported
}

// CopyIn uploads a file into the workspace of an instance.
func (f *fakeRuntime) CopyIn(ctx context.Context, id runtime.ID, src runtime.ArtifactRef, dst runtime.GuestPath) error {
	return runtime.ErrNotSupported
}

// CopyOut writes the file of the workload outside of its workspace.
func (f *fakeRuntime) CopyOut(ctx context.Context, id runtime.ID, src runtime.GuestPath) (runtime.ArtifactRef, error) {
	f.copied = append(f.copied, string(src))
	name := f.name
	if name == "" {
		name = filepath.Base(string(src))
	}
	directory := filepath.Join(f.root, "exports", string(id))
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return runtime.ArtifactRef{}, err
	}
	destination := filepath.Join(directory, name)
	if err := os.WriteFile(destination, f.content, 0o640); err != nil {
		return runtime.ArtifactRef{}, err
	}
	digest := sha256.Sum256(f.content)
	return runtime.ArtifactRef{
		Path:   destination,
		Name:   name,
		MIME:   "text/plain",
		Digest: hex.EncodeToString(digest[:]),
		Size:   int64(len(f.content)),
	}, nil
}

// Stop halts an instance.
func (f *fakeRuntime) Stop(ctx context.Context, id runtime.ID, grace time.Duration) error { return nil }

// Destroy removes an instance.
func (f *fakeRuntime) Destroy(ctx context.Context, id runtime.ID) error { return nil }

// Inspect reports the state of an instance.
func (f *fakeRuntime) Inspect(ctx context.Context, id runtime.ID) (runtime.ObservedState, error) {
	return runtime.ObservedState{State: runtime.StateReady}, nil
}

// Health reports that the backend is usable.
func (f *fakeRuntime) Health(ctx context.Context) error { return nil }

// fakeInstance registers a fake backend and the instance that uses it.
func fakeInstance(t *testing.T, env *testEnv, content []byte, owner string) (*fakeRuntime, string) {
	t.Helper()
	backend := &fakeRuntime{
		root:    filepath.Join(env.dir, "instances"),
		content: content,
		name:    "report.txt",
	}
	env.registry.Register(backend)
	instance, err := env.store.CreateInstance(context.Background(), store.Instance{
		ID:         ids.New(ids.PrefixInstance),
		JobID:      ids.New(ids.PrefixJob),
		UserID:     owner,
		TemplateID: "alpine-shell",
		Runtime:    backend.Name(),
		RuntimeID:  "fake-1",
		Name:       "exported",
		State:      store.InstanceReady,
		Workspace:  filepath.Join(env.dir, "instances", "fake-1"),
		ExpiresAt:  time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create the instance: %v", err)
	}
	return backend, instance.ID
}

// TestArtifactExportCopiesTheFileOutOfTheInstance checks the export of the
// files a workload produced.
func TestArtifactExportCopiesTheFileOutOfTheInstance(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	content := []byte("the report of the workload")
	backend, instanceID := fakeInstance(t, env, content, env.user.ID)

	var exported ArtifactExportOutput
	decodeOutput(t, callTool(t, session, "artifact.export", map[string]any{
		"instance_id": instanceID,
		"paths":       []string{"report.txt"},
	}), &exported)

	if len(exported.Artifacts) != 1 {
		t.Fatalf("the export produced %d artifacts, want 1", len(exported.Artifacts))
	}
	artifact := exported.Artifacts[0]
	if artifact.Name != "report.txt" {
		t.Errorf("the artifact is named %q, want %q", artifact.Name, "report.txt")
	}
	if artifact.Size != int64(len(content)) {
		t.Errorf("the artifact holds %d bytes, want %d", artifact.Size, len(content))
	}
	digest := sha256.Sum256(content)
	if artifact.Digest != hex.EncodeToString(digest[:]) {
		t.Errorf("the artifact carries the digest %q", artifact.Digest)
	}
	if artifact.InstanceID != instanceID {
		t.Errorf("the artifact names the instance %q, want %q", artifact.InstanceID, instanceID)
	}
	if len(backend.copied) != 1 || backend.copied[0] != "report.txt" {
		t.Errorf("the backend copied %v", backend.copied)
	}

	// The file joined the artifact store of the account and the copy that
	// the backend made stays outside of the workspace of the guest.
	record, err := env.store.ArtifactByID(context.Background(), artifact.ID)
	if err != nil {
		t.Fatalf("load the artifact: %v", err)
	}
	if !strings.HasPrefix(record.Path, env.artifacts.Root()) {
		t.Errorf("the artifact does not live in the artifact store")
	}
	if strings.Contains(record.Path, filepath.Join("instances", "fake-1")) {
		t.Errorf("the artifact %q lives inside the workspace of the guest", record.Path)
	}
	content2, err := os.ReadFile(record.Path)
	if err != nil {
		t.Fatalf("read the artifact: %v", err)
	}
	if string(content2) != string(content) {
		t.Errorf("the artifact holds %q", string(content2))
	}
}

// TestArtifactExportRefusesAnotherAccount checks the ownership of the
// instance that is exported.
func TestArtifactExportRefusesAnotherAccount(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	other, err := env.store.CreateUser(context.Background(), store.User{
		ID:       ids.New(ids.PrefixUser),
		Username: "intruder",
		Role:     store.RoleUser,
	})
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}
	_, instanceID := fakeInstance(t, env, []byte("the report of the workload"), other.ID)

	result := callTool(t, session, "artifact.export", map[string]any{
		"instance_id": instanceID,
		"paths":       []string{"report.txt"},
	})
	if !result.IsError {
		t.Fatal("the export of the instance of another account succeeded")
	}
	if code := toolErrorCode(t, result); code != string(CodePermissionDenied) {
		t.Errorf("the failure carries the code %q, want %q", code, CodePermissionDenied)
	}
}

// TestArtifactDownloadIssuesASingleUseToken checks the link the download
// tool answers with.
func TestArtifactDownloadIssuesASingleUseToken(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	content := []byte("the report of the workload")
	_, instanceID := fakeInstance(t, env, content, env.user.ID)

	var exported ArtifactExportOutput
	decodeOutput(t, callTool(t, session, "artifact.export", map[string]any{
		"instance_id": instanceID,
		"paths":       []string{"report.txt"},
	}), &exported)
	if len(exported.Artifacts) != 1 {
		t.Fatalf("the export produced %d artifacts, want 1", len(exported.Artifacts))
	}

	var download ArtifactDownloadOutput
	decodeOutput(t, callTool(t, session, "artifact.download", map[string]any{
		"artifact_id": exported.Artifacts[0].ID,
	}), &download)

	// The answer never carries the path of the file on the host.
	if strings.Contains(download.DownloadURL, env.artifacts.Root()) {
		t.Errorf("the link leaks the path of the host: %q", download.DownloadURL)
	}
	parsed, err := url.Parse(download.DownloadURL)
	if err != nil {
		t.Fatalf("parse the link: %v", err)
	}
	token := parsed.Query().Get("token")
	if token == "" {
		t.Fatalf("the link %q carries no token", download.DownloadURL)
	}

	// The token opens the artifact once.
	grant, err := env.server.downloads.Redeem(token, exported.Artifacts[0].ID)
	if err != nil {
		t.Fatalf("redeem the token: %v", err)
	}
	if grant.ArtifactID != exported.Artifacts[0].ID {
		t.Errorf("the token opens the artifact %q", grant.ArtifactID)
	}
	if _, err := env.server.downloads.Redeem(token, exported.Artifacts[0].ID); err == nil {
		t.Error("the token was redeemed twice")
	}
}

// TestArtifactDownloadRefusesAnExpiredToken checks that a link that is too
// old does not open the artifact anymore.
func TestArtifactDownloadRefusesAnExpiredToken(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	content := []byte("the report of the workload")
	_, instanceID := fakeInstance(t, env, content, env.user.ID)

	var exported ArtifactExportOutput
	decodeOutput(t, callTool(t, session, "artifact.export", map[string]any{
		"instance_id": instanceID,
		"paths":       []string{"report.txt"},
	}), &exported)

	// A manager whose tokens expire at once stands in for a link that
	// outlived its deadline.
	expired, err := artifacts.NewDownloads(artifacts.DownloadOptions{
		Root: filepath.Join(env.dir, "expired-downloads"),
		TTL:  time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("create the download manager: %v", err)
	}
	token, err := expired.Issue(exported.Artifacts[0].ID, env.user.ID)
	if err != nil {
		t.Fatalf("issue the token: %v", err)
	}
	if _, err := expired.Redeem(token.Token, exported.Artifacts[0].ID); err == nil {
		t.Fatal("an expired token opened the artifact")
	}
}
