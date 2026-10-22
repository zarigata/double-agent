package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordedRequest is a call the fake daemon received.
type recordedRequest struct {
	// Method is the HTTP method of the call.
	Method string
	// Path is the request path without the API version prefix.
	Path string
	// Query is the raw query string of the call.
	Query string
	// Body is the request body.
	Body []byte
}

// fakeDaemon is a stand-in for the Docker Engine API.
//
// It answers with the shapes the real daemon uses, including the protocol
// upgrade of an exec session, so the client is exercised the way it is in
// production.
type fakeDaemon struct {
	mu sync.Mutex
	// requests holds every call the daemon received.
	requests []recordedRequest
	// createBody is the decoded body of the last container create call.
	createBody ContainerCreateRequest
	// createHostConfig is the host configuration of the last create call.
	createHostConfig map[string]any
	// execRequest is the body of the last exec create call.
	execRequest ExecCreateRequest
	// archive is the tar archive of the last upload.
	archive []byte
	// storedFile is the file the daemon holds for a download.
	storedFile []byte
	// storedName is the name of the file the daemon holds.
	storedName string
	// execStdout and execStderr are the output of the exec session.
	execStdout []byte
	execStderr []byte
	// execExitCode is the exit code the exec inspect reports.
	execExitCode int
	// rawExecStream makes the exec start answer with a plain response
	// instead of a protocol upgrade.
	rawExecStream bool
	// images are the images the daemon knows.
	images map[string]bool
	// networks are the networks the daemon knows.
	networks map[string]bool
	// inspectState is the state the container inspect reports.
	inspectState ContainerState
}

// newFakeDaemon returns a daemon that knows one image and one container.
func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{
		images:   map[string]bool{"alpine:3.19": true},
		networks: map[string]bool{},
		inspectState: ContainerState{
			Status:    "running",
			Running:   true,
			StartedAt: "2024-05-01T10:00:00.000000000Z",
		},
	}
}

// ServeHTTP implements http.Handler.
func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	prefix := "/v" + APIVersion
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		Method: r.Method,
		Path:   strings.TrimPrefix(r.URL.Path, prefix),
		Query:  r.URL.RawQuery,
		Body:   body,
	})
	f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case r.Method == http.MethodGet && path == "/_ping":
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "OK")
	case r.Method == http.MethodGet && path == "/info":
		writeJSON(w, http.StatusOK, DaemonInfo{ID: "fake", ServerVersion: "27.0.0", NCPU: 4})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
		ref := strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json")
		f.mu.Lock()
		known := f.images[ref]
		f.mu.Unlock()
		if !known {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such image: " + ref})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"Id": "sha256:fake"})
	case r.Method == http.MethodPost && path == "/images/create":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"Downloaded newer image"}`+"\n")
	case r.Method == http.MethodPost && path == "/containers/create":
		f.handleCreate(w, body)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/stop"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/json") && strings.HasPrefix(path, "/containers/"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		if id != "container-1" {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + id})
			return
		}
		f.mu.Lock()
		state := f.inspectState
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, ContainerInspect{ID: "container-1", Name: "/double-agent-test", State: &state})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/logs"):
		f.handleLogs(w)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/exec"):
		f.handleExecCreate(w, body)
	case r.Method == http.MethodPost && path == "/exec/exec-1/start":
		f.handleExecStart(w)
	case r.Method == http.MethodGet && path == "/exec/exec-1/json":
		f.mu.Lock()
		code := f.execExitCode
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, ExecInspect{ID: "exec-1", ContainerID: "container-1", ExitCode: code})
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/archive"):
		f.mu.Lock()
		f.archive = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/archive"):
		f.handleArchive(w)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/networks/"):
		name := strings.TrimPrefix(path, "/networks/")
		f.mu.Lock()
		known := f.networks[name]
		f.mu.Unlock()
		if !known {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "network " + name + " not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"Name": name})
	case r.Method == http.MethodPost && path == "/networks/create":
		f.mu.Lock()
		f.networks[restrictedNetwork] = true
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]string{"Id": "network-1"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "fake daemon has no route for " + path})
	}
}

// handleCreate records the body of a container create call.
func (f *fakeDaemon) handleCreate(w http.ResponseWriter, body []byte) {
	var request ContainerCreateRequest
	_ = json.Unmarshal(body, &request)
	f.mu.Lock()
	f.createBody = request
	f.createHostConfig = nil
	if request.HostConfig != nil {
		encoded, _ := json.Marshal(request.HostConfig)
		_ = json.Unmarshal(encoded, &f.createHostConfig)
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"Id": "container-1", "Warnings": []string{}})
}

// handleExecCreate records the body of an exec create call.
func (f *fakeDaemon) handleExecCreate(w http.ResponseWriter, body []byte) {
	var request ExecCreateRequest
	_ = json.Unmarshal(body, &request)
	f.mu.Lock()
	f.execRequest = request
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"Id": "exec-1"})
}

// handleExecStart hijacks the connection and writes the framed output of the
// session, the way the daemon does.
func (f *fakeDaemon) handleExecStart(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "no hijacker"})
		return
	}
	conn, buffer, err := hijacker.Hijack()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "hijack failed"})
		return
	}
	defer conn.Close()
	f.mu.Lock()
	raw := f.rawExecStream
	stdout := f.execStdout
	stderr := f.execStderr
	f.mu.Unlock()
	if raw {
		// A daemon that ignores the upgrade header answers 200 and
		// streams the same bytes as the response body.
		_, _ = fmt.Fprint(buffer, "HTTP/1.1 200 OK\r\nContent-Type: "+mediaTypeRawStream+"\r\n\r\n")
	} else {
		_, _ = fmt.Fprint(buffer, "HTTP/1.1 101 UPGRADED\r\nContent-Type: "+mediaTypeMultiplexedStream+"\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	}
	_ = buffer.Flush()
	if len(stdout) > 0 {
		_ = writeStreamFrame(conn, streamStdout, stdout)
	}
	if len(stderr) > 0 {
		_ = writeStreamFrame(conn, streamStderr, stderr)
	}
}

// handleLogs writes the framed output of the container.
func (f *fakeDaemon) handleLogs(w http.ResponseWriter) {
	f.mu.Lock()
	stdout := f.execStdout
	stderr := f.execStderr
	f.mu.Unlock()
	w.Header().Set("Content-Type", mediaTypeMultiplexedStream)
	w.WriteHeader(http.StatusOK)
	_ = writeStreamFrame(w, streamStdout, stdout)
	_ = writeStreamFrame(w, streamStderr, stderr)
}

// handleArchive answers a download with the file the daemon holds.
func (f *fakeDaemon) handleArchive(w http.ResponseWriter) {
	f.mu.Lock()
	name := f.storedName
	content := f.storedFile
	f.mu.Unlock()
	if name == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such file"})
		return
	}
	archive, err := buildTar([]tarFile{{Name: name, Mode: 0o644, Data: content}})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(archive)
}

// writeFrame writes one framed chunk of an output stream.
func writeStreamFrame(w io.Writer, stream byte, payload []byte) error {
	header := make([]byte, frameHeaderSize)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// writeJSON answers with a JSON document.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// calls returns the recorded calls of the daemon.
func (f *fakeDaemon) calls() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

// lastCall returns the last recorded call that matches a method and a path
// suffix.
func (f *fakeDaemon) lastCall(method, suffix string) (recordedRequest, bool) {
	calls := f.calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Method == method && strings.HasSuffix(calls[i].Path, suffix) {
			return calls[i], true
		}
	}
	return recordedRequest{}, false
}

// newTestClient returns a client that talks to the fake daemon.
func newTestClient(t *testing.T, daemon *fakeDaemon) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(daemon)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, server
}

// TestContainerCreateSendsDockerContract checks that the body of a create
// call uses the field names of the Docker Engine API.
func TestContainerCreateSendsDockerContract(t *testing.T) {
	daemon := newFakeDaemon()
	client, _ := newTestClient(t, daemon)
	body := ContainerCreateRequest{
		Image:      "alpine:3.19",
		Cmd:        []string{"sleep", "1"},
		Env:        []string{"A=1"},
		Labels:     map[string]string{LabelInstance: "ins_1"},
		WorkingDir: "/workspace",
		HostConfig: &HostConfig{
			Binds:       []string{"/tmp/work:/workspace:rw"},
			Memory:      1 << 30,
			MemorySwap:  1 << 30,
			NanoCpus:    2e9,
			NetworkMode: "none",
			CapDrop:     []string{"ALL"},
			CapAdd:      []string{"CHOWN"},
			SecurityOpt: []string{"no-new-privileges:true"},
			PidsLimit:   int64Pointer(512),
		},
	}
	id, warnings, err := client.ContainerCreate(context.Background(), "double-agent-test", body)
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	if id != "container-1" {
		t.Fatalf("container id = %q, want container-1", id)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	call, ok := daemon.lastCall(http.MethodPost, "/containers/create")
	if !ok {
		t.Fatal("the daemon did not receive a create call")
	}
	if !strings.Contains(call.Query, "name=double-agent-test") {
		t.Errorf("create query = %q, want the container name", call.Query)
	}
	var wire map[string]any
	if err := json.Unmarshal(call.Body, &wire); err != nil {
		t.Fatalf("decode create body: %v", err)
	}
	for _, key := range []string{"Image", "Cmd", "Env", "Labels", "HostConfig", "WorkingDir", "Tty", "OpenStdin", "AttachStdout", "AttachStderr"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("create body has no %q field", key)
		}
	}
	host, ok := wire["HostConfig"].(map[string]any)
	if !ok {
		t.Fatal("create body has no host configuration")
	}
	for _, key := range []string{"Binds", "Memory", "MemorySwap", "NanoCpus", "NetworkMode", "CapDrop", "CapAdd", "SecurityOpt", "PidsLimit", "ReadonlyRootfs", "AutoRemove", "Privileged"} {
		if _, ok := host[key]; !ok {
			t.Errorf("host configuration has no %q field", key)
		}
	}
	if host["NanoCpus"].(float64) != 2e9 {
		t.Errorf("NanoCpus = %v, want 2000000000", host["NanoCpus"])
	}
	if host["PidsLimit"].(float64) != 512 {
		t.Errorf("PidsLimit = %v, want 512", host["PidsLimit"])
	}
}

// TestExecStartReadsFramedStream checks that the framed output of an exec
// session is split into stdout and stderr.
func TestExecStartReadsFramedStream(t *testing.T) {
	daemon := newFakeDaemon()
	daemon.execStdout = []byte("hello\n")
	daemon.execStderr = []byte("warning\n")
	daemon.execExitCode = 3
	client, _ := newTestClient(t, daemon)
	stream, mediaType, err := client.ExecStart(context.Background(), "exec-1", false)
	if err != nil {
		t.Fatalf("start exec: %v", err)
	}
	defer stream.Close()
	if mediaType != mediaTypeMultiplexedStream {
		t.Errorf("media type = %q, want %q", mediaType, mediaTypeMultiplexedStream)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := demuxStream(stream, stdout, stderr, true); err != nil {
		t.Fatalf("demux: %v", err)
	}
	if stdout.String() != "hello\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "hello\n")
	}
	if stderr.String() != "warning\n" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "warning\n")
	}
	info, err := client.ExecInspect(context.Background(), "exec-1")
	if err != nil {
		t.Fatalf("inspect exec: %v", err)
	}
	if info.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", info.ExitCode)
	}
}

// TestExecStartReadsPlainStream checks the fallback for a daemon that
// answers the exec start with a plain response instead of a protocol
// upgrade.
func TestExecStartReadsPlainStream(t *testing.T) {
	daemon := newFakeDaemon()
	daemon.rawExecStream = true
	daemon.execStdout = []byte("plain\n")
	client, _ := newTestClient(t, daemon)
	stream, mediaType, err := client.ExecStart(context.Background(), "exec-1", false)
	if err != nil {
		t.Fatalf("start exec: %v", err)
	}
	defer stream.Close()
	if mediaType != mediaTypeRawStream {
		t.Errorf("media type = %q, want %q", mediaType, mediaTypeRawStream)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := demuxStream(stream, stdout, stderr, true); err != nil {
		t.Fatalf("demux: %v", err)
	}
	if stdout.String() != "plain\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "plain\n")
	}
}

// TestExecCreateSendsContract checks the body of an exec create call.
func TestExecCreateSendsContract(t *testing.T) {
	daemon := newFakeDaemon()
	client, _ := newTestClient(t, daemon)
	id, err := client.ExecCreate(context.Background(), "container-1", ExecCreateRequest{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"/bin/sh", "-c", "echo hi"},
		Env:          []string{"TOKEN=secret"},
		WorkingDir:   "/workspace",
	})
	if err != nil {
		t.Fatalf("create exec: %v", err)
	}
	if id != "exec-1" {
		t.Fatalf("exec id = %q, want exec-1", id)
	}
	call, ok := daemon.lastCall(http.MethodPost, "/containers/container-1/exec")
	if !ok {
		t.Fatal("the daemon did not receive an exec create call")
	}
	var wire map[string]any
	if err := json.Unmarshal(call.Body, &wire); err != nil {
		t.Fatalf("decode exec body: %v", err)
	}
	if wire["WorkingDir"] != "/workspace" {
		t.Errorf("WorkingDir = %v, want /workspace", wire["WorkingDir"])
	}
	if wire["Privileged"] != false {
		t.Errorf("Privileged = %v, want false", wire["Privileged"])
	}
	if wire["Tty"] != false {
		t.Errorf("Tty = %v, want false", wire["Tty"])
	}
	env := wire["Env"].([]any)
	if len(env) != 1 || env[0] != "TOKEN=secret" {
		t.Errorf("Env = %v, want the session environment", env)
	}
}

// TestCopyToContainerUploadsTar checks that an upload is a tar archive that
// holds the file under its own name.
func TestCopyToContainerUploadsTar(t *testing.T) {
	daemon := newFakeDaemon()
	client, _ := newTestClient(t, daemon)
	archive, err := buildTar([]tarFile{{Name: "notes.txt", Mode: 0o644, Data: []byte("content")}})
	if err != nil {
		t.Fatalf("build tar: %v", err)
	}
	if err := client.CopyToContainer(context.Background(), "container-1", "/workspace", bytes.NewReader(archive)); err != nil {
		t.Fatalf("copy to container: %v", err)
	}
	call, ok := daemon.lastCall(http.MethodPut, "/archive")
	if !ok {
		t.Fatal("the daemon did not receive an upload")
	}
	if !strings.Contains(call.Query, "path=%2Fworkspace") {
		t.Errorf("upload query = %q, want the target path", call.Query)
	}
	reader := tar.NewReader(bytes.NewReader(call.Body))
	header, err := reader.Next()
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if header.Name != "notes.txt" {
		t.Errorf("archive entry = %q, want notes.txt", header.Name)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read archive content: %v", err)
	}
	if string(content) != "content" {
		t.Errorf("archive content = %q, want %q", content, "content")
	}
}

// TestCopyFromContainerDownloadsTar checks that a download is unpacked.
func TestCopyFromContainerDownloadsTar(t *testing.T) {
	daemon := newFakeDaemon()
	daemon.storedName = "report.txt"
	daemon.storedFile = []byte("report body")
	client, _ := newTestClient(t, daemon)
	stream, err := client.CopyFromContainer(context.Background(), "container-1", "/workspace/report.txt")
	if err != nil {
		t.Fatalf("copy from container: %v", err)
	}
	defer stream.Close()
	name, content, err := extractTar(stream, "report.txt", maxArtifactBytes)
	if err != nil {
		t.Fatalf("extract archive: %v", err)
	}
	if name != "report.txt" {
		t.Errorf("name = %q, want report.txt", name)
	}
	if string(content) != "report body" {
		t.Errorf("content = %q, want %q", content, "report body")
	}
}

// TestContainerLifecycleCalls checks the calls of a container lifecycle.
func TestContainerLifecycleCalls(t *testing.T) {
	daemon := newFakeDaemon()
	client, _ := newTestClient(t, daemon)
	ctx := context.Background()
	if err := client.ContainerStart(ctx, "container-1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := client.ContainerStop(ctx, "container-1", 5*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := client.ContainerRemove(ctx, "container-1", true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	stop, ok := daemon.lastCall(http.MethodPost, "/stop")
	if !ok {
		t.Fatal("the daemon did not receive a stop call")
	}
	if !strings.Contains(stop.Query, "t=5") {
		t.Errorf("stop query = %q, want the grace period", stop.Query)
	}
	remove, ok := daemon.lastCall(http.MethodDelete, "/containers/container-1")
	if !ok {
		t.Fatal("the daemon did not receive a remove call")
	}
	if !strings.Contains(remove.Query, "v=1") || !strings.Contains(remove.Query, "force=true") {
		t.Errorf("remove query = %q, want the volume and force flags", remove.Query)
	}
}

// TestPingAndInfo checks the health calls of the daemon.
func TestPingAndInfo(t *testing.T) {
	daemon := newFakeDaemon()
	client, _ := newTestClient(t, daemon)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	info, err := client.Info(context.Background())
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.ServerVersion != "27.0.0" {
		t.Errorf("server version = %q, want 27.0.0", info.ServerVersion)
	}
}

// TestNotFoundError checks that a missing resource is reported as such.
func TestNotFoundError(t *testing.T) {
	daemon := newFakeDaemon()
	client, _ := newTestClient(t, daemon)
	_, err := client.ContainerInspect(context.Background(), "missing")
	if err == nil {
		t.Fatal("inspect of a missing container did not fail")
	}
	if !IsNotFound(err) {
		t.Fatalf("error %v is not a not found error", err)
	}
}

// TestParseEndpoint checks the supported daemon endpoints.
func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		host    string
		network string
		address string
	}{
		{"unix:///var/run/docker.sock", "unix", "/var/run/docker.sock"},
		{"", "unix", "/var/run/docker.sock"},
		{"/var/run/docker.sock", "unix", "/var/run/docker.sock"},
		{"tcp://127.0.0.1:2375", "tcp", "127.0.0.1:2375"},
		{"http://127.0.0.1:2375", "tcp", "127.0.0.1:2375"},
		{"https://docker.example:2376", "tcp", "docker.example:2376"},
	}
	for _, test := range cases {
		endpoint, err := parseEndpoint(test.host)
		if err != nil {
			t.Fatalf("parse %q: %v", test.host, err)
		}
		if endpoint.network != test.network || endpoint.address != test.address {
			t.Errorf("parse %q = %s %s, want %s %s", test.host, endpoint.network, endpoint.address, test.network, test.address)
		}
	}
}

// int64Pointer returns a pointer to a value.
func int64Pointer(value int64) *int64 { return &value }
