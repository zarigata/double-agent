package mcpsrv

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/runtime/local"
	"github.com/zarigata/double-agent/internal/store"
	"github.com/zarigata/double-agent/internal/templates"
)

// testEnv is a node of the control plane that lives in a temporary directory.
type testEnv struct {
	dir      string
	store    *store.Store
	orch     *orchestrator.Orchestrator
	server   *Server
	registry *runtime.Registry
	user     store.User
	// artifacts is the controlled file store of the node.
	artifacts *artifacts.Store
}

// newTestEnv builds a node with a local backend and one account.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	// A temporary directory may sit behind a symbolic link, and the local
	// backend resolves the root of its workspaces. The node therefore uses
	// the resolved spelling as well.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	catalogue := templates.NewCatalogue()
	for _, tmpl := range templates.Builtin() {
		if err := catalogue.Publish(tmpl); err != nil {
			t.Fatalf("publish the template %s: %v", tmpl.Spec.ID, err)
		}
	}

	registry := runtime.NewRegistry()
	backend, err := local.New(local.Config{
		Enabled:       true,
		WorkspaceRoot: filepath.Join(dir, "instances"),
	})
	if err != nil {
		t.Fatalf("create the local backend: %v", err)
	}
	registry.Register(backend)
	registry.Default = "local"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	artifactStore, err := artifacts.New(artifacts.Options{
		Root:    filepath.Join(dir, "artifacts"),
		MaxSize: 1 << 20,
	})
	if err != nil {
		t.Fatalf("create the artifact store: %v", err)
	}
	orch, err := orchestrator.New(orchestrator.Options{
		Store:     st,
		Catalogue: catalogue,
		Runtimes:  registry,
		Logger:    logger,
		DataDir:   dir,
		Artifacts: artifactStore,
		Limits: orchestrator.Limits{
			MaxConcurrentJobs:   4,
			MaxInstances:        8,
			MaxInstancesPerUser: 4,
			DefaultTTL:          time.Hour,
			MaxTTL:              4 * time.Hour,
		},
	})
	if err != nil {
		t.Fatalf("create the orchestrator: %v", err)
	}

	server, err := NewServer(orch, Options{
		Logger:    logger,
		Store:     st,
		Registry:  registry,
		Version:   "test",
		Artifacts: artifactStore,
	})
	if err != nil {
		t.Fatalf("create the mcp server: %v", err)
	}

	user, err := st.CreateUser(context.Background(), store.User{
		ID:       ids.New(ids.PrefixUser),
		Username: "tester",
		Role:     store.RoleUser,
	})
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}

	return &testEnv{
		dir:       dir,
		store:     st,
		orch:      orch,
		server:    server,
		registry:  registry,
		user:      user,
		artifacts: artifactStore,
	}
}

// issueKey stores a client credential and returns its secret.
func (e *testEnv) issueKey(t *testing.T, scopes ...string) (string, store.APIKey) {
	t.Helper()
	secret, _, prefix, hash, err := auth.IssueKey()
	if err != nil {
		t.Fatalf("issue the key: %v", err)
	}
	key, err := e.store.CreateAPIKey(context.Background(), store.APIKey{
		ID:      ids.New(ids.PrefixAPIKey),
		UserID:  e.user.ID,
		Name:    "test",
		Prefix:  prefix,
		KeyHash: hash,
		Scopes:  scopes,
	})
	if err != nil {
		t.Fatalf("store the key: %v", err)
	}
	return secret, key
}

// actorFor resolves the actor of a secret through the authentication of the
// transport, exactly as a request would.
func (e *testEnv) actorFor(t *testing.T, secret string) policy.Actor {
	t.Helper()
	request := httptestRequest(http.MethodPost, secret)
	actor, failure := e.server.authenticate(request)
	if failure != nil {
		t.Fatalf("authenticate the credential: %v", failure)
	}
	return actor
}

// connect pairs an MCP client with the server of an actor.
func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect the server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "certified-client", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect the client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

// callTool calls a tool and fails the test when the transport itself fails.
func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

// decodeOutput reads the structured answer of a tool.
func decodeOutput(t *testing.T, result *mcp.CallToolResult, out any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("the tool failed: %s", toolErrorText(t, result))
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("encode the answer: %v", err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
}

// toolErrorText returns the message of a failed tool call.
func toolErrorText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		return "the tool returned no content"
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return "the tool returned no text"
	}
	return text.Text
}

// toolErrorCode returns the stable code of a failed tool call.
func toolErrorCode(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	var payload struct {
		Code Code `json:"code"`
	}
	if err := json.Unmarshal([]byte(toolErrorText(t, result)), &payload); err != nil {
		t.Fatalf("decode the failure: %v", err)
	}
	return string(payload.Code)
}

// scopeStrings renders a list of scopes for the store.
func scopeStrings(scopes []policy.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, string(scope))
	}
	return out
}

// listToolNames lists the tools of a server.
func listToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list the tools: %v", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// TestToolList checks the contract of the surface.
func TestToolList(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	names := listToolNames(t, session)
	expected := append([]string{}, ToolNames...)
	sort.Strings(expected)
	if len(names) != len(expected) {
		t.Fatalf("the surface exposes %d tools, want %d: %v", len(names), len(expected), names)
	}
	for i, name := range names {
		if name != expected[i] {
			t.Fatalf("the surface exposes %q at position %d, want %q", name, i, expected[i])
		}
	}

	// Every tool is described and carries a schema: a client that lists the
	// tools has to know how to call them.
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list the tools: %v", err)
	}
	for _, tool := range result.Tools {
		if tool.Description == "" {
			t.Errorf("the tool %s has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("the tool %s has no input schema", tool.Name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("the tool %s has no output schema", tool.Name)
		}
	}
}

// TestNoAdministrativeTool checks that a client never administers the node.
func TestNoAdministrativeTool(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.AdminScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	for _, name := range listToolNames(t, session) {
		for _, prefix := range []string{"user.", "admin.", "key.", "session."} {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("the surface exposes the administrative tool %q", name)
			}
		}
	}
}

// TestCapabilitiesGet checks the shape of the discovery document.
func TestCapabilitiesGet(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	var out CapabilitiesOutput
	decodeOutput(t, callTool(t, session, "capabilities.get", map[string]any{}), &out)

	if out.Version != "test" {
		t.Errorf("the answer reports the version %q, want %q", out.Version, "test")
	}
	if len(out.Runtimes) != 1 {
		t.Fatalf("the answer lists %d backends, want 1", len(out.Runtimes))
	}
	if out.Runtimes[0].Name != "local" || !out.Runtimes[0].Healthy || !out.Runtimes[0].Default {
		t.Errorf("the answer describes the backend %+v", out.Runtimes[0])
	}
	if len(out.Templates) == 0 {
		t.Error("the answer lists no template")
	}
	if len(out.Scopes) == 0 {
		t.Error("the answer lists no scope")
	}
	if len(out.Tools) != len(ToolNames) {
		t.Errorf("the answer lists %d tools, want %d", len(out.Tools), len(ToolNames))
	}
	if len(out.Resources) == 0 {
		t.Error("the answer lists no resource")
	}
}

// TestTemplateTools checks the catalogue of the node.
func TestTemplateTools(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	var list TemplateListOutput
	decodeOutput(t, callTool(t, session, "template.list", map[string]any{}), &list)
	if len(list.Templates) == 0 {
		t.Fatal("the catalogue is empty")
	}

	var one TemplateGetOutput
	decodeOutput(t, callTool(t, session, "template.get", map[string]any{
		"template": "alpine-shell",
	}), &one)
	if one.Template.ID != "alpine-shell" {
		t.Errorf("the answer describes the template %q", one.Template.ID)
	}
	if one.Template.Digest == "" {
		t.Error("the answer carries no digest")
	}

	// A template that does not exist is a tool failure, not a transport
	// failure.
	missing := callTool(t, session, "template.get", map[string]any{"template": "nope"})
	if !missing.IsError {
		t.Fatal("the call for an unknown template succeeded")
	}
	if code := toolErrorCode(t, missing); code != string(CodeNotFound) {
		t.Errorf("the failure carries the code %q, want %q", code, CodeNotFound)
	}
}

// TestDocsSearch checks the documentation search.
func TestDocsSearch(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	var out DocsSearchOutput
	decodeOutput(t, callTool(t, session, "docs.search", map[string]any{
		"query": "secret",
	}), &out)
	if len(out.Results) == 0 {
		t.Fatal("the search found nothing")
	}
	found := false
	for _, result := range out.Results {
		if strings.Contains(strings.ToLower(result.Excerpt+result.Title), "secret") {
			found = true
		}
	}
	if !found {
		t.Errorf("the search did not find the section about the secrets: %+v", out.Results)
	}
}

// waitForJob waits until a job reaches a final state.
func waitForJob(t *testing.T, session *mcp.ClientSession, jobID string) JobView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var out JobGetOutput
		decodeOutput(t, callTool(t, session, "job.get", map[string]any{"job_id": jobID}), &out)
		if out.Job.State == string(store.JobCompleted) || out.Job.State == string(store.JobFailed) {
			return out.Job
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the job %s did not finish in time", jobID)
	return JobView{}
}

// TestInstanceLifecycle runs an instance from end to end.
//
// The node is a temporary directory with a SQLite store and the local
// backend, so the call goes through the whole control plane.
func TestInstanceLifecycle(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	// The payload is the one of docs/MCP_API_AND_DISCOVERY.md.
	var created InstanceCreateOutput
	decodeOutput(t, callTool(t, session, "instance.create", map[string]any{
		"template": "alpine-shell",
		"resources": map[string]any{
			"cpu":       1,
			"memory_mb": 512,
			"gpu":       map[string]any{"vendor": "any", "count": 1, "required": false},
		},
		"ttl_seconds":     3600,
		"idempotency_key": "client-task-47",
	}), &created)

	if created.JobID == "" {
		t.Fatal("the answer carries no job")
	}
	if created.InstanceID == "" {
		t.Fatal("the answer carries no instance")
	}
	if created.State != string(store.JobPreparing) {
		t.Errorf("the answer reports the state %q, want %q", created.State, store.JobPreparing)
	}
	if created.Links["status"] != "double-agent://jobs/"+created.JobID {
		t.Errorf("the answer links to %q", created.Links["status"])
	}

	// The same idempotency key returns the first job instead of starting a
	// second workload.
	var replayed InstanceCreateOutput
	decodeOutput(t, callTool(t, session, "instance.create", map[string]any{
		"template":        "alpine-shell",
		"idempotency_key": "client-task-47",
	}), &replayed)
	if replayed.JobID != created.JobID {
		t.Errorf("the replay created the job %q, want %q", replayed.JobID, created.JobID)
	}
	if !replayed.Replayed {
		t.Error("the replay is not reported as a replay")
	}

	job := waitForJob(t, session, created.JobID)
	if job.State != string(store.JobCompleted) {
		t.Fatalf("the job ended in the state %q: %s", job.State, job.ErrorMessage)
	}

	var instance InstanceGetOutput
	decodeOutput(t, callTool(t, session, "instance.get", map[string]any{
		"instance_id": created.InstanceID,
	}), &instance)
	if instance.Instance.State != string(store.InstanceReady) {
		t.Fatalf("the instance is in the state %q, want %q", instance.Instance.State, store.InstanceReady)
	}
	if instance.Instance.TemplateID != "alpine-shell" {
		t.Errorf("the instance runs the template %q", instance.Instance.TemplateID)
	}

	// The command is an argument vector and never a shell string.
	var executed InstanceExecOutput
	decodeOutput(t, callTool(t, session, "instance.exec", map[string]any{
		"instance_id":     created.InstanceID,
		"argv":            []string{"/bin/sh", "-c", "echo double-agent"},
		"timeout_seconds": 60,
		"environment":     map[string]any{"CI": "true"},
		"secret_refs":     []string{"secret/npm-read-token"},
	}), &executed)
	if executed.ExitCode != 0 {
		t.Fatalf("the command exited with %d: %s", executed.ExitCode, executed.Stderr)
	}
	if !strings.Contains(executed.Stdout, "double-agent") {
		t.Errorf("the command wrote %q", executed.Stdout)
	}
	if len(executed.SecretRefs) != 1 || executed.SecretRefs[0] != "secret/npm-read-token" {
		t.Errorf("the answer echoes the references %v", executed.SecretRefs)
	}

	// The lifetime of the instance may be pushed further away.
	var extended InstanceExtendTTLOutput
	decodeOutput(t, callTool(t, session, "instance.extend_ttl", map[string]any{
		"instance_id": created.InstanceID,
		"ttl_seconds": 7200,
	}), &extended)
	if !extended.ExpiresAt.After(instance.Instance.ExpiresAt) {
		t.Errorf("the instance expires at %s, want later than %s", extended.ExpiresAt, instance.Instance.ExpiresAt)
	}

	// The instance is stopped and then destroyed.
	var stopped InstanceStopOutput
	decodeOutput(t, callTool(t, session, "instance.stop", map[string]any{
		"instance_id":   created.InstanceID,
		"grace_seconds": 1,
	}), &stopped)
	if stopped.InstanceID != created.InstanceID {
		t.Errorf("the answer names the instance %q", stopped.InstanceID)
	}

	var destroyed InstanceDestroyOutput
	decodeOutput(t, callTool(t, session, "instance.destroy", map[string]any{
		"instance_id": created.InstanceID,
	}), &destroyed)
	if destroyed.State != string(store.InstanceDestroyed) {
		t.Errorf("the answer reports the state %q, want %q", destroyed.State, store.InstanceDestroyed)
	}
}

// TestInstanceCreateWithoutScope checks that a key that may not create an
// instance is refused before the control plane is reached.
func TestInstanceCreateWithoutScope(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t,
		string(policy.ScopeCapabilitiesRead),
		string(policy.ScopeInstancesRead),
	)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	result := callTool(t, session, "instance.create", map[string]any{
		"template": "alpine-shell",
	})
	if !result.IsError {
		t.Fatal("the call of a key without the scope was accepted")
	}
	if code := toolErrorCode(t, result); code != string(CodePermissionDenied) {
		t.Errorf("the failure carries the code %q, want %q", code, CodePermissionDenied)
	}
}

// TestAdminScopeIsDropped checks the rule that an MCP key never inherits the
// power of a browser session.
func TestAdminScopeIsDropped(t *testing.T) {
	env := newTestEnv(t)
	secret, key := env.issueKey(t,
		string(policy.ScopeAdmin),
		string(policy.ScopeUsersManage),
		string(policy.ScopeKeysManage),
		string(policy.ScopeInstancesCreate),
		string(policy.ScopeCapabilitiesRead),
	)
	actor := env.actorFor(t, secret)

	if actor.IsAdmin {
		t.Error("the actor of an MCP key is an administrator")
	}
	if actor.Scopes.Has(policy.ScopeAdmin) {
		t.Error("the admin scope survived the authentication")
	}
	if actor.Scopes.Has(policy.ScopeUsersManage) || actor.Scopes.Has(policy.ScopeKeysManage) {
		t.Error("an administrative scope survived the authentication")
	}
	if !actor.Scopes.Has(policy.ScopeInstancesCreate) {
		t.Error("the ordinary scope of the key was dropped")
	}
	if actor.Scopes.IsAdmin() {
		t.Error("the scope set of the actor is an administrator")
	}
	if actor.Type != store.ActorAPIKey {
		t.Errorf("the actor is of the type %q, want %q", actor.Type, store.ActorAPIKey)
	}
	if actor.ID != env.user.ID {
		t.Errorf("the actor is %q, want %q", actor.ID, env.user.ID)
	}
	if actor.Username != env.user.Username {
		t.Errorf("the actor is named %q, want %q", actor.Username, env.user.Username)
	}

	// The key keeps working for the work it is allowed to do.
	stored, err := env.store.APIKeyByID(context.Background(), key.ID)
	if err != nil {
		t.Fatalf("read the key: %v", err)
	}
	if !slices.Contains(stored.Scopes, string(policy.ScopeAdmin)) {
		t.Error("the stored key lost its scope: the rule applies to the session, not to the record")
	}

	session := connect(t, env.server.MCPServer(actor))
	var out CapabilitiesOutput
	decodeOutput(t, callTool(t, session, "capabilities.get", map[string]any{}), &out)
	for _, scope := range out.Scopes {
		if scope == string(policy.ScopeAdmin) {
			t.Error("the answer lists the admin scope")
		}
	}
}

// TestEveryToolAnswers calls every tool of the surface once.
//
// A tool that cannot do its work answers with a tool failure that carries a
// stable code. A transport failure is a bug of the surface.
func TestEveryToolAnswers(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	// A job that is still running, so that the cancellation has something to
	// cancel.
	pending, err := env.store.CreateJob(context.Background(), store.Job{
		ID:     ids.New(ids.PrefixJob),
		UserID: env.user.ID,
		Kind:   store.JobKindInstanceCreate,
		State:  store.JobQueued,
	})
	if err != nil {
		t.Fatalf("create the job: %v", err)
	}

	// An artifact of the account.
	path := filepath.Join(env.dir, "report.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write the artifact: %v", err)
	}
	artifact, err := env.store.CreateArtifact(context.Background(), store.Artifact{
		ID:     ids.New(ids.PrefixArtifact),
		UserID: env.user.ID,
		Name:   "report.txt",
		Path:   path,
		MIME:   "text/plain",
		Size:   5,
		Digest: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		State:  store.ArtifactAvailable,
	})
	if err != nil {
		t.Fatalf("record the artifact: %v", err)
	}

	// The creation is checked apart from the other calls: its instance is
	// destroyed at the end of the test, because a workload that outlives its
	// test would hold a process of the host.
	var created InstanceCreateOutput
	decodeOutput(t, callTool(t, session, "instance.create", map[string]any{
		"template": "alpine-shell",
	}), &created)
	t.Cleanup(func() {
		callTool(t, session, "instance.destroy", map[string]any{
			"instance_id": created.InstanceID,
		})
	})

	cases := []struct {
		name      string
		arguments map[string]any
	}{
		{"capabilities.get", map[string]any{}},
		{"template.list", map[string]any{}},
		{"template.get", map[string]any{"template": "alpine-shell"}},
		{"docs.search", map[string]any{"query": "instance"}},
		{"instance.get", map[string]any{"instance_id": "ins_missing"}},
		{"instance.exec", map[string]any{"instance_id": "ins_missing", "argv": []string{"true"}}},
		{"instance.stop", map[string]any{"instance_id": "ins_missing"}},
		{"instance.destroy", map[string]any{"instance_id": "ins_missing"}},
		{"instance.extend_ttl", map[string]any{"instance_id": "ins_missing", "ttl_seconds": 60}},
		{"artifact.list", map[string]any{}},
		{"artifact.download", map[string]any{"artifact_id": artifact.ID}},
		{"job.get", map[string]any{"job_id": pending.ID}},
		{"job.list", map[string]any{}},
		{"job.cancel", map[string]any{"job_id": pending.ID}},
		{"job.logs", map[string]any{"job_id": pending.ID}},
	}
	for _, testCase := range cases {
		result := callTool(t, session, testCase.name, testCase.arguments)
		if result.IsError {
			// A failure is acceptable when the tool cannot do its work, but
			// it has to carry a stable code.
			code := toolErrorCode(t, result)
			if code == "" {
				t.Errorf("the failure of %s carries no code", testCase.name)
			}
			continue
		}
		if result.StructuredContent == nil {
			t.Errorf("the answer of %s is empty", testCase.name)
		}
	}
}

// bearerTransport adds the credential of the client to every request.
type bearerTransport struct {
	token string
	// id is the correlation identifier the client supplies.
	id   string
	base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	if t.id != "" {
		clone.Header.Set("X-Request-Id", t.id)
	}
	return t.base.RoundTrip(clone)
}

// TestStreamableTransport drives the surface through the HTTP transport.
//
// The test covers the whole path of a remote client: the bearer token is
// checked by the middleware, the transport is stateless and answers with
// JSON, and the tools of the node are the tools of the document.
func TestStreamableTransport(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)

	httpServer := httptest.NewServer(NewHandler(env.server))
	t.Cleanup(httpServer.Close)

	transport := &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{
			token: secret,
			base:  http.DefaultTransport,
		}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "remote-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect the client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	names := listToolNames(t, session)
	expected := append([]string{}, ToolNames...)
	sort.Strings(expected)
	if len(names) != len(expected) {
		t.Fatalf("the surface exposes %d tools, want %d: %v", len(names), len(expected), names)
	}
	for i, name := range names {
		if name != expected[i] {
			t.Fatalf("the surface exposes %q at position %d, want %q", name, i, expected[i])
		}
	}

	var out CapabilitiesOutput
	decodeOutput(t, callTool(t, session, "capabilities.get", map[string]any{}), &out)
	if out.Version != "test" {
		t.Errorf("the answer reports the version %q, want %q", out.Version, "test")
	}
}

// TestStreamableTransportRefusesAnonymousClient checks that a client without
// a credential never reaches the tools of the node.
func TestStreamableTransportRefusesAnonymousClient(t *testing.T) {
	env := newTestEnv(t)
	httpServer := httptest.NewServer(NewHandler(env.server))
	t.Cleanup(httpServer.Close)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   httpServer.URL + "/mcp",
		HTTPClient: http.DefaultClient,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "anonymous-client", Version: "1"}, nil)
	if _, err := client.Connect(context.Background(), transport, nil); err == nil {
		t.Fatal("the anonymous client was accepted")
	}
}

// TestResources checks the agent-readable resources of the node.
func TestResources(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	listed, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("list the resources: %v", err)
	}
	uris := make([]string, 0, len(listed.Resources))
	for _, resource := range listed.Resources {
		uris = append(uris, resource.URI)
	}
	for _, want := range ResourceNames {
		found := false
		for _, uri := range uris {
			if uri == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the surface does not publish the resource %q", want)
		}
	}

	// The quickstart explains how to use the surface.
	read, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "double-agent://docs/quickstart",
	})
	if err != nil {
		t.Fatalf("read the quickstart: %v", err)
	}
	if len(read.Contents) == 0 || !strings.Contains(read.Contents[0].Text, "instance.create") {
		t.Errorf("the quickstart carries %+v", read.Contents)
	}

	// The template of the node is readable through its resource template.
	template, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "double-agent://templates/alpine-shell",
	})
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	if len(template.Contents) == 0 || !strings.Contains(template.Contents[0].Text, "alpine-shell") {
		t.Errorf("the template carries %+v", template.Contents)
	}

	// A template that does not exist is not a resource.
	if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "double-agent://templates/nope",
	}); err == nil {
		t.Error("the read of an unknown template succeeded")
	}
}

// TestCorrelationIDFollowsTheCall checks that the identifier of the request
// travels with the failure of a tool call.
func TestCorrelationIDFollowsTheCall(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)

	httpServer := httptest.NewServer(NewHandler(env.server))
	t.Cleanup(httpServer.Close)

	const requestID = "req_caller_supplied"
	transport := &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{
			token: secret,
			id:    requestID,
			base:  http.DefaultTransport,
		}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "remote-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect the client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	result := callTool(t, session, "template.get", map[string]any{"template": "nope"})
	if !result.IsError {
		t.Fatal("the call for an unknown template succeeded")
	}
	var payload struct {
		Code          Code   `json:"code"`
		Message       string `json:"message"`
		CorrelationID string `json:"correlation_id"`
		Retryable     bool   `json:"retryable"`
	}
	if err := json.Unmarshal([]byte(toolErrorText(t, result)), &payload); err != nil {
		t.Fatalf("decode the failure: %v", err)
	}
	if payload.Code != CodeNotFound {
		t.Errorf("the failure carries the code %q, want %q", payload.Code, CodeNotFound)
	}
	if payload.Message == "" {
		t.Error("the failure carries no message")
	}
	if payload.CorrelationID != requestID {
		t.Errorf("the failure carries the correlation identifier %q, want %q", payload.CorrelationID, requestID)
	}
}
