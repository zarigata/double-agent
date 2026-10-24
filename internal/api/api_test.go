package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/api"
	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/config"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/runtime/local"
	"github.com/zarigata/double-agent/internal/store"
	"github.com/zarigata/double-agent/internal/templates"
)

const (
	// adminUser and adminPassword are the credentials of the administrator
	// of the test node.
	adminUser     = "admin"
	adminPassword = "administrator-pass-1"
	// memberUser and memberPassword are the credentials of a regular
	// account of the test node.
	memberUser     = "member"
	memberPassword = "member-password-1"
)

// env is a running control plane with its HTTP surface.
type env struct {
	t      *testing.T
	server *httptest.Server
	store  *store.Store
	// dataDir is the directory of the node.
	dataDir string
}

// session is a signed in browser.
type session struct {
	env    *env
	client *http.Client
	// csrf is the CSRF token of the session.
	csrf string
}

// newEnv starts a control plane on a temporary directory.
//
// The local host process backend stands in for a container runtime, so the
// whole flow works without a container engine.
func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	// The local backend resolves the symbolic links of its workspace root
	// and then refuses a workspace outside of it. The orchestrator has to
	// use the resolved path as well, otherwise the two disagree about the
	// location of the workspace of an instance.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve the data directory: %v", err)
	}

	cfg := config.Default()
	cfg.DataDir = resolved
	cfg.Listen.TLS.Enabled = false
	cfg.Runtime.Default = "local"
	cfg.Runtime.Local.Enabled = true
	cfg.Runtime.Local.WorkspaceRoot = filepath.Join(resolved, "instances")
	cfg.Artifacts.Root = filepath.Join(resolved, "artifacts")
	cfg.Artifacts.MaxUploadBytes = 1 << 20
	cfg.Limits.MaxInstances = 10
	cfg.Limits.MaxInstancesPerUser = 10

	st, err := store.Open(filepath.Join(resolved, "state", "double-agent.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	catalogue := templates.NewCatalogue()
	for _, tmpl := range templates.Builtin() {
		if err := catalogue.Publish(tmpl); err != nil {
			t.Fatalf("publish a template: %v", err)
		}
	}

	registry := runtime.NewRegistry()
	backend, err := local.New(local.Config{
		Enabled:       true,
		WorkspaceRoot: cfg.Runtime.Local.WorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("create the local backend: %v", err)
	}
	registry.Register(backend)
	registry.Default = "local"

	orch, err := orchestrator.New(orchestrator.Options{
		Store:     st,
		Catalogue: catalogue,
		Runtimes:  registry,
		DataDir:   resolved,
		Limits: orchestrator.Limits{
			MaxConcurrentJobs:   4,
			MaxInstances:        10,
			MaxInstancesPerUser: 10,
			DefaultTTL:          time.Hour,
			MaxTTL:              4 * time.Hour,
		},
	})
	if err != nil {
		t.Fatalf("create the orchestrator: %v", err)
	}

	artifactStore, err := artifacts.New(artifacts.Options{
		Root:    cfg.Artifacts.Root,
		MaxSize: cfg.Artifacts.MaxSizeBytes,
	})
	if err != nil {
		t.Fatalf("create the artifact store: %v", err)
	}

	handler := api.New(api.Options{
		Store:        st,
		Orchestrator: orch,
		Artifacts:    artifactStore,
		Config:       cfg,
		Registry:     registry,
		Catalogue:    catalogue,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	createUser(t, st, adminUser, adminPassword, store.RoleAdmin)
	createUser(t, st, memberUser, memberPassword, store.RoleUser)

	return &env{t: t, server: server, store: st, dataDir: resolved}
}

// createUser adds an account to the store of the test node.
func createUser(t *testing.T, st *store.Store, username, password string, role store.Role) store.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash the password: %v", err)
	}
	user, err := st.CreateUser(context.Background(), store.User{
		ID:           ids.New(ids.PrefixUser),
		Username:     username,
		PasswordHash: hash,
		Role:         role,
	})
	if err != nil {
		t.Fatalf("create the user %q: %v", username, err)
	}
	return user
}

// anonymous returns a client that carries no session.
func (e *env) anonymous() *session {
	return &session{env: e, client: e.newClient()}
}

// newClient returns a client with its own cookie jar.
func (e *env) newClient() *http.Client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatalf("create the cookie jar: %v", err)
	}
	return &http.Client{Jar: jar}
}

// login signs an account in and keeps the session cookie and the CSRF token.
func (e *env) login(username, password string) *session {
	e.t.Helper()
	client := e.newClient()
	resp, data := e.requestWith(client, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": username, "password": password}, nil)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("the login answered %d: %s", resp.StatusCode, data)
	}
	var answer struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		e.t.Fatalf("decode the session: %v", err)
	}
	return &session{env: e, client: client, csrf: answer.CSRFToken}
}

// request performs an anonymous request against the test node.
func (e *env) request(method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	return e.requestWith(e.newClient(), method, path, body, headers)
}

// requestWith performs a request with a given client.
//
// The body is sent as JSON. The headers are added to the request, so a test
// can add a bearer token or a CSRF token.
func (e *env) requestWith(client *http.Client, method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("encode the request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatalf("build the request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("perform the request: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read the answer: %v", err)
	}
	return resp, data
}

// do performs a request of the session.
func (s *session) do(method, path string, body any) (*http.Response, []byte) {
	s.env.t.Helper()
	return s.requestWith(s.client, method, path, body, map[string]string{
		"X-CSRF-Token": s.csrf,
	})
}

// requestWith performs a request of the session with extra headers.
func (s *session) requestWith(client *http.Client, method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	s.env.t.Helper()
	return s.env.requestWith(client, method, path, body, headers)
}

// errorEnvelope is the shape every failed request answers with.
type errorEnvelope struct {
	Error struct {
		Code          string         `json:"code"`
		Message       string         `json:"message"`
		Retryable     bool           `json:"retryable"`
		Retry         bool           `json:"retry"`
		CorrelationID string         `json:"correlation_id"`
		Details       map[string]any `json:"details"`
	} `json:"error"`
}

// decodeError decodes the error envelope of a failed request.
func decodeError(t *testing.T, data []byte) errorEnvelope {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode the error envelope %q: %v", string(data), err)
	}
	return envelope
}

func TestLoginSetsCookieAndMeReturnsTheUser(t *testing.T) {
	e := newEnv(t)
	client := e.newClient()
	resp, data := e.requestWith(client, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": adminUser, "password": adminPassword}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the login answered %d: %s", resp.StatusCode, data)
	}
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		t.Fatal("the login did not set a cookie")
	}
	cookie := cookies[0]
	if !strings.Contains(cookie, auth.SessionCookieName+"=") {
		t.Errorf("the cookie does not carry the session: %q", cookie)
	}
	if !strings.Contains(cookie, "HttpOnly") {
		t.Errorf("the session cookie is not HttpOnly: %q", cookie)
	}
	if !strings.Contains(cookie, "SameSite=Lax") {
		t.Errorf("the session cookie does not restrict the same site: %q", cookie)
	}

	var session struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatalf("decode the login answer: %v", err)
	}
	if session.User.Username != adminUser {
		t.Errorf("the login returned the user %q", session.User.Username)
	}
	if session.User.Role != string(store.RoleAdmin) {
		t.Errorf("the login returned the role %q", session.User.Role)
	}
	if session.User.ID == "" {
		t.Error("the login did not return the identifier of the user")
	}
	if session.CSRFToken == "" {
		t.Error("the login did not return a CSRF token")
	}

	resp, data = e.requestWith(client, http.MethodGet, "/api/v1/auth/me", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the session answered %d: %s", resp.StatusCode, data)
	}
	var me struct {
		User struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(data, &me); err != nil {
		t.Fatalf("decode the session answer: %v", err)
	}
	if me.User.Username != adminUser || me.User.Role != string(store.RoleAdmin) {
		t.Errorf("the session returned %+v", me.User)
	}
}

func TestLoginWithAWrongPasswordIsRefused(t *testing.T) {
	e := newEnv(t)
	resp, data := e.request(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": adminUser, "password": "not-the-right-password"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the login answered %d, want 401: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "authentication_failed" {
		t.Errorf("the answer carries the code %q, want authentication_failed", envelope.Error.Code)
	}
	if envelope.Error.Message == "" {
		t.Error("the answer carries no message")
	}
}

func TestARequestWithoutCredentialsIsRefused(t *testing.T) {
	e := newEnv(t)
	resp, data := e.request(http.MethodGet, "/api/v1/instances", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the request answered %d, want 401: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "authentication_failed" {
		t.Errorf("the answer carries the code %q, want authentication_failed", envelope.Error.Code)
	}
	if envelope.Error.CorrelationID == "" {
		t.Error("the answer carries no correlation identifier")
	}
	if envelope.Error.CorrelationID != resp.Header.Get("X-Request-Id") {
		t.Errorf("the correlation identifier %q does not match the header %q",
			envelope.Error.CorrelationID, resp.Header.Get("X-Request-Id"))
	}
}

func TestTheErrorEnvelopeHasTheDocumentedShape(t *testing.T) {
	e := newEnv(t)
	resp, data := e.request(http.MethodGet, "/api/v1/admin/users", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the request answered %d, want 401: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code == "" {
		t.Error("the envelope carries no code")
	}
	if envelope.Error.Message == "" {
		t.Error("the envelope carries no message")
	}
	if envelope.Error.Retryable {
		t.Error("an authentication failure is not retryable")
	}
	if envelope.Error.CorrelationID == "" {
		t.Error("the envelope carries no correlation identifier")
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("the answer carries the content type %q", contentType)
	}
}

func TestCreateInstanceAnswersWithAJob(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	// The instance is created in the background, so the answer carries the
	// job that tracks it.
	resp, data := admin.do(http.MethodPost, "/api/v1/instances",
		map[string]any{"template": "alpine-shell", "name": "test-instance"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the creation answered %d, want 202: %s", resp.StatusCode, data)
	}
	var created struct {
		JobID      string            `json:"job_id"`
		InstanceID string            `json:"instance_id"`
		State      string            `json:"state"`
		Links      map[string]string `json:"links"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("decode the creation answer: %v", err)
	}
	if created.JobID == "" {
		t.Error("the answer carries no job identifier")
	}
	if created.InstanceID == "" {
		t.Error("the answer carries no instance identifier")
	}
	if created.State == "" {
		t.Error("the answer carries no state")
	}
	if want := "double-agent://jobs/" + created.JobID; created.Links["status"] != want {
		t.Errorf("the answer links to %q, want %q", created.Links["status"], want)
	}

	// The job is listed for the owner.
	resp, data = admin.do(http.MethodGet, "/api/v1/jobs", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the job list answered %d: %s", resp.StatusCode, data)
	}
	var jobs struct {
		Jobs []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatalf("decode the job list: %v", err)
	}
	found := false
	for _, job := range jobs.Jobs {
		if job.ID == created.JobID {
			found = true
		}
	}
	if !found {
		t.Errorf("the job %q is not part of the listing", created.JobID)
	}

	// The instance reaches the ready state and is released again.
	e.waitForInstance(t, admin, created.InstanceID, "ready")
	resp, data = admin.do(http.MethodDelete, "/api/v1/instances/"+created.InstanceID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the destruction answered %d: %s", resp.StatusCode, data)
	}
}

func TestACookieAuthenticatedChangeNeedsTheCSRFToken(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	resp, data := e.requestWith(admin.client, http.MethodPost, "/api/v1/instances",
		map[string]any{"template": "alpine-shell"}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the request without a CSRF token answered %d, want 403: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "permission_denied" {
		t.Errorf("the answer carries the code %q, want permission_denied", envelope.Error.Code)
	}

	resp, data = admin.do(http.MethodPost, "/api/v1/instances",
		map[string]any{"template": "alpine-shell", "name": "csrf-test"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the request with a CSRF token answered %d, want 202: %s", resp.StatusCode, data)
	}
}

func TestABearerTokenNeedsNoCSRFToken(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)
	secret := e.issueKey(t, admin, []string{"instances:create", "instances:read", "jobs:read"})

	resp, data := e.requestWith(e.newClient(), http.MethodPost, "/api/v1/instances",
		map[string]any{"template": "alpine-shell", "name": "bearer-test"},
		map[string]string{"Authorization": "Bearer " + secret})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the request with a bearer token answered %d, want 202: %s", resp.StatusCode, data)
	}
	var created struct {
		JobID      string `json:"job_id"`
		InstanceID string `json:"instance_id"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("decode the creation answer: %v", err)
	}
	e.waitForInstance(t, admin, created.InstanceID, "ready")
}

func TestANonAdministratorCannotListTheUsers(t *testing.T) {
	e := newEnv(t)
	member := e.login(memberUser, memberPassword)
	resp, data := member.do(http.MethodGet, "/api/v1/admin/users", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the request answered %d, want 403: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "permission_denied" {
		t.Errorf("the answer carries the code %q, want permission_denied", envelope.Error.Code)
	}
}

func TestTheHealthEndpointIsPublic(t *testing.T) {
	e := newEnv(t)
	resp, data := e.request(http.MethodGet, "/api/v1/healthz", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the health endpoint answered %d: %s", resp.StatusCode, data)
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &health); err != nil {
		t.Fatalf("decode the health answer: %v", err)
	}
	if health.Status != "ok" {
		t.Errorf("the health endpoint answered %q", health.Status)
	}
}

func TestTheCapabilitiesDescribeTheNode(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)
	resp, data := admin.do(http.MethodGet, "/api/v1/capabilities", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the capabilities answered %d: %s", resp.StatusCode, data)
	}
	var capabilities struct {
		Runtimes []struct {
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			Healthy bool   `json:"healthy"`
		} `json:"runtimes"`
		Templates []map[string]any `json:"templates"`
		Limits    map[string]any   `json:"limits"`
		Network   map[string]any   `json:"network"`
	}
	if err := json.Unmarshal(data, &capabilities); err != nil {
		t.Fatalf("decode the capabilities: %v", err)
	}
	if len(capabilities.Runtimes) == 0 {
		t.Error("the answer lists no runtime backend")
	}
	if !capabilities.Runtimes[0].Healthy {
		t.Errorf("the local backend is not healthy: %+v", capabilities.Runtimes[0])
	}
	if len(capabilities.Templates) == 0 {
		t.Error("the answer lists no template")
	}
	if capabilities.Limits == nil || capabilities.Network == nil {
		t.Error("the answer carries no limits or no network policy")
	}
}

func TestTheTemplateCatalogueIsServed(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)
	resp, data := admin.do(http.MethodGet, "/api/v1/templates", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the template list answered %d: %s", resp.StatusCode, data)
	}
	var catalogue struct {
		Templates []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(data, &catalogue); err != nil {
		t.Fatalf("decode the template list: %v", err)
	}
	if len(catalogue.Templates) == 0 {
		t.Fatal("the catalogue is empty")
	}
	resp, data = admin.do(http.MethodGet, "/api/v1/templates/alpine-shell", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the template answered %d: %s", resp.StatusCode, data)
	}
	resp, data = admin.do(http.MethodGet, "/api/v1/templates/not-a-template", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown template answered %d, want 404: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "not_found" {
		t.Errorf("the answer carries the code %q, want not_found", envelope.Error.Code)
	}
}

func TestACommandRunsInsideAnInstance(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)
	resp, data := admin.do(http.MethodPost, "/api/v1/instances",
		map[string]any{"template": "alpine-shell", "name": "exec-test"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the creation answered %d: %s", resp.StatusCode, data)
	}
	var created struct {
		InstanceID string `json:"instance_id"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("decode the creation answer: %v", err)
	}
	e.waitForInstance(t, admin, created.InstanceID, "ready")

	resp, data = admin.do(http.MethodPost, "/api/v1/instances/"+created.InstanceID+"/exec",
		map[string]any{"argv": []string{"echo", "hello double agent"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the command answered %d: %s", resp.StatusCode, data)
	}
	var result struct {
		ExitCode   int    `json:"exit_code"`
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode the command answer: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("the command exited with %d: %s", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "hello double agent") {
		t.Errorf("the command wrote %q", result.Stdout)
	}
}

func TestAnArtifactIsUploadedAndDownloaded(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatalf("build the multipart body: %v", err)
	}
	if _, err := part.Write([]byte("a stored note")); err != nil {
		t.Fatalf("write the multipart body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close the multipart body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/api/v1/artifacts", body)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-CSRF-Token", admin.csrf)
	resp, err := admin.client.Do(req)
	if err != nil {
		t.Fatalf("perform the request: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the upload answered %d: %s", resp.StatusCode, data)
	}
	var answer struct {
		Artifact struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		t.Fatalf("decode the upload answer: %v", err)
	}
	if answer.Artifact.ID == "" {
		t.Fatal("the upload returned no artifact")
	}
	if answer.Artifact.Size != int64(len("a stored note")) {
		t.Errorf("the artifact holds %d bytes", answer.Artifact.Size)
	}

	resp, data = admin.do(http.MethodGet, "/api/v1/artifacts/"+answer.Artifact.ID+"/download", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the download answered %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "a stored note") {
		t.Errorf("the download returned %q", string(data))
	}
	if disposition := resp.Header.Get("Content-Disposition"); !strings.Contains(disposition, "note.txt") {
		t.Errorf("the download carries the disposition %q", disposition)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Errorf("the download carries the content type %q", contentType)
	}
}

func TestTheWebInterfaceIsServed(t *testing.T) {
	e := newEnv(t)
	resp, data := e.request(http.MethodGet, "/", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the interface answered %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "<title>Double Agent") {
		t.Error("the interface does not look like the control plane")
	}
	// An unknown path belongs to the single page application.
	resp, data = e.request(http.MethodGet, "/instances/ins_123", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the fallback answered %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "<title>Double Agent") {
		t.Error("the fallback does not serve the entry point")
	}
	// An unknown API path is an error and not the interface.
	resp, data = e.request(http.MethodGet, "/api/v1/nope", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown endpoint answered %d, want 404: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "not_found" {
		t.Errorf("the answer carries the code %q, want not_found", envelope.Error.Code)
	}
}

func TestACrossOriginRequestFromAnUnknownOriginIsRefused(t *testing.T) {
	e := newEnv(t)
	resp, data := e.request(http.MethodGet, "/api/v1/healthz", nil,
		map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the request answered %d, want 403: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "permission_denied" {
		t.Errorf("the answer carries the code %q, want permission_denied", envelope.Error.Code)
	}
}

func TestAnInstanceOfAnotherUserIsHidden(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)
	resp, data := admin.do(http.MethodPost, "/api/v1/instances",
		map[string]any{"template": "alpine-shell", "name": "owned-instance"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the creation answered %d: %s", resp.StatusCode, data)
	}
	var created struct {
		InstanceID string `json:"instance_id"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("decode the creation answer: %v", err)
	}
	e.waitForInstance(t, admin, created.InstanceID, "ready")

	// Another account of the same node must not see the instance.
	member := e.login(memberUser, memberPassword)
	resp, data = member.do(http.MethodGet, "/api/v1/instances/"+created.InstanceID, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the request answered %d, want 403: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "permission_denied" {
		t.Errorf("the answer carries the code %q, want permission_denied", envelope.Error.Code)
	}
	// The listing of the member holds no instance of the administrator.
	resp, data = member.do(http.MethodGet, "/api/v1/instances", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the instance list answered %d: %s", resp.StatusCode, data)
	}
	var listing struct {
		Instances []struct {
			ID string `json:"id"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		t.Fatalf("decode the instance list: %v", err)
	}
	for _, instance := range listing.Instances {
		if instance.ID == created.InstanceID {
			t.Errorf("the instance %q is part of the listing of another account", created.InstanceID)
		}
	}
}

func TestAWrongMethodIsRefused(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)
	resp, data := admin.do(http.MethodPut, "/api/v1/instances", map[string]any{"template": "alpine-shell"})
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("the request answered %d, want 405: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "invalid_request" {
		t.Errorf("the answer carries the code %q, want invalid_request", envelope.Error.Code)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("the answer allows %q", allow)
	}
}

func TestTheCertificateAuthorityIsServed(t *testing.T) {
	e := newEnv(t)
	// The daemon publishes the certificate authority in the PKI directory
	// of the node.
	pki := filepath.Join(e.dataDir, "pki")
	if err := os.MkdirAll(pki, 0o750); err != nil {
		t.Fatalf("create the pki directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pki, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatalf("write the certificate: %v", err)
	}
	resp, data := e.request(http.MethodGet, "/ca.crt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the certificate answered %d: %s", resp.StatusCode, data)
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "application/x-x509-ca-cert" {
		t.Errorf("the certificate carries the content type %q", contentType)
	}
}

// issueKey creates a client credential and returns its secret.
func (e *env) issueKey(t *testing.T, admin *session, scopes []string) string {
	t.Helper()
	resp, data := admin.do(http.MethodPost, "/api/v1/admin/keys",
		map[string]any{"name": "test", "scopes": scopes})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the key creation answered %d: %s", resp.StatusCode, data)
	}
	var answer struct {
		Key struct {
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
			Secret string `json:"secret"`
		} `json:"key"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		t.Fatalf("decode the key answer: %v", err)
	}
	if answer.Key.Secret == "" {
		t.Fatal("the key creation returned no secret")
	}
	if answer.Key.Prefix == "" {
		t.Error("the key creation returned no prefix")
	}
	return answer.Key.Secret
}

// waitForInstance waits until an instance reaches a state.
func (e *env) waitForInstance(t *testing.T, admin *session, id, state string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		resp, data := admin.do(http.MethodGet, "/api/v1/instances/"+id, nil)
		last = string(data)
		if resp.StatusCode == http.StatusOK {
			var instance struct {
				State string `json:"state"`
			}
			if err := json.Unmarshal(data, &instance); err != nil {
				t.Fatalf("decode the instance: %v", err)
			}
			if instance.State == state {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the instance %q did not reach the state %q: %s", id, state, last)
}
