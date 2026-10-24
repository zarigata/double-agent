package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/artifacts"
)

// uploadAnswer is the answer of the upload endpoints.
type uploadAnswer struct {
	Upload struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		Received  int64  `json:"received"`
		Digest    string `json:"digest"`
		State     string `json:"state"`
		UploadURL string `json:"upload_url"`
	} `json:"upload"`
	Artifact struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	} `json:"artifact"`
}

// beginUpload announces a file to the node.
func beginUpload(t *testing.T, s *session, name string, size int64) uploadAnswer {
	t.Helper()
	resp, data := s.do(http.MethodPost, "/api/v1/uploads", map[string]any{
		"filename":     name,
		"content_type": "text/plain",
		"size_bytes":   size,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the announcement answered %d: %s", resp.StatusCode, data)
	}
	var answer uploadAnswer
	if err := json.Unmarshal(data, &answer); err != nil {
		t.Fatalf("decode the announcement: %v", err)
	}
	return answer
}

// putUpload sends the bytes of an upload.
func putUpload(t *testing.T, s *session, id string, content []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, s.env.server.URL+"/api/v1/uploads/"+id, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-CSRF-Token", s.csrf)
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("perform the request: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return resp, data
}

// TestAFileIsUploadedThroughTheResumableEndpoint walks through the upload
// of a file: the announcement, the bytes and the completion.
func TestAFileIsUploadedThroughTheResumableEndpoint(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	content := []byte("a stored note")
	digest := sha256.Sum256(content)

	answer := beginUpload(t, admin, "note.txt", int64(len(content)))
	if answer.Upload.ID == "" {
		t.Fatal("the announcement returned no handle")
	}
	if answer.Upload.UploadURL != "/api/v1/uploads/"+answer.Upload.ID {
		t.Errorf("the endpoint %q does not carry the handle", answer.Upload.UploadURL)
	}
	if answer.Upload.State != "pending" {
		t.Errorf("the announcement carries the state %q", answer.Upload.State)
	}

	resp, data := putUpload(t, admin, answer.Upload.ID, content)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the transfer answered %d: %s", resp.StatusCode, data)
	}
	var transferred uploadAnswer
	if err := json.Unmarshal(data, &transferred); err != nil {
		t.Fatalf("decode the transfer answer: %v", err)
	}
	if transferred.Upload.Received != int64(len(content)) {
		t.Errorf("the node received %d bytes, want %d", transferred.Upload.Received, len(content))
	}
	if transferred.Upload.Digest != hex.EncodeToString(digest[:]) {
		t.Errorf("the node hashed the content to %q", transferred.Upload.Digest)
	}

	// The status of the upload is readable by its owner.
	resp, data = admin.do(http.MethodGet, "/api/v1/uploads/"+answer.Upload.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the status answered %d: %s", resp.StatusCode, data)
	}

	// The completion records the artifact.
	resp, data = admin.do(http.MethodPost, "/api/v1/uploads/"+answer.Upload.ID+"/complete",
		map[string]any{"sha256": hex.EncodeToString(digest[:])})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the completion answered %d: %s", resp.StatusCode, data)
	}
	var completed uploadAnswer
	if err := json.Unmarshal(data, &completed); err != nil {
		t.Fatalf("decode the completion: %v", err)
	}
	if completed.Artifact.ID == "" {
		t.Fatal("the completion returned no artifact")
	}
	if completed.Artifact.Name != "note.txt" {
		t.Errorf("the artifact is named %q", completed.Artifact.Name)
	}
	if completed.Artifact.Size != int64(len(content)) {
		t.Errorf("the artifact holds %d bytes", completed.Artifact.Size)
	}

	// The content is served by the download endpoint.
	resp, data = admin.do(http.MethodGet, "/api/v1/artifacts/"+completed.Artifact.ID+"/download", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the download answered %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "a stored note") {
		t.Errorf("the download returned %q", string(data))
	}
}

// TestAnUploadRefusesAFileThatIsTooLarge checks the limit of the node.
func TestAnUploadRefusesAFileThatIsTooLarge(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	// The test node accepts a megabyte per upload.
	resp, data := admin.do(http.MethodPost, "/api/v1/uploads", map[string]any{
		"filename":   "huge.bin",
		"size_bytes": (1 << 20) + 1,
	})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the announcement answered %d, want 429: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "quota_exceeded" {
		t.Errorf("the answer carries the code %q, want quota_exceeded", envelope.Error.Code)
	}
}

// TestAnUploadRefusesABodyOfAnotherLength checks that the node refuses a
// body whose length differs from the announced one.
func TestAnUploadRefusesABodyOfAnotherLength(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	answer := beginUpload(t, admin, "note.txt", 12)
	resp, data := putUpload(t, admin, answer.Upload.ID, []byte("short"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("the transfer answered %d, want 400: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "invalid_request" {
		t.Errorf("the answer carries the code %q, want invalid_request", envelope.Error.Code)
	}
}

// TestAnUploadRefusesAFileWithAWrongDigest checks that a file that does not
// match its digest never reaches the artifact store.
func TestAnUploadRefusesAFileWithAWrongDigest(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	content := []byte("a stored note")
	answer := beginUpload(t, admin, "note.txt", int64(len(content)))
	resp, data := putUpload(t, admin, answer.Upload.ID, content)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the transfer answered %d: %s", resp.StatusCode, data)
	}

	wrong := sha256.Sum256([]byte("another file"))
	resp, data = admin.do(http.MethodPost, "/api/v1/uploads/"+answer.Upload.ID+"/complete",
		map[string]any{"sha256": hex.EncodeToString(wrong[:])})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("the completion answered %d, want 400: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "digest_mismatch" {
		t.Errorf("the answer carries the code %q, want digest_mismatch", envelope.Error.Code)
	}

	// The file never reached the store.
	resp, data = admin.do(http.MethodGet, "/api/v1/artifacts", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the listing answered %d: %s", resp.StatusCode, data)
	}
	if strings.Contains(string(data), "note.txt") {
		t.Errorf("the refused upload produced an artifact: %s", data)
	}
}

// TestAnUploadIsPrivateToItsOwner checks that one account never touches
// the upload of another one.
func TestAnUploadIsPrivateToItsOwner(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)
	member := e.login(memberUser, memberPassword)

	answer := beginUpload(t, admin, "secret.txt", 6)

	// The other account may not read the state of the upload.
	resp, data := member.do(http.MethodGet, "/api/v1/uploads/"+answer.Upload.ID, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the status answered %d, want 403: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "permission_denied" {
		t.Errorf("the answer carries the code %q, want permission_denied", envelope.Error.Code)
	}

	// The other account may not send the bytes of the upload.
	resp, data = putUpload(t, member, answer.Upload.ID, []byte("secret"))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the transfer answered %d, want 403: %s", resp.StatusCode, data)
	}

	// The other account may not complete the upload.
	digest := sha256.Sum256([]byte("secret"))
	resp, data = member.do(http.MethodPost, "/api/v1/uploads/"+answer.Upload.ID+"/complete",
		map[string]any{"sha256": hex.EncodeToString(digest[:])})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the completion answered %d, want 403: %s", resp.StatusCode, data)
	}
}

// TestADownloadTokenIsRedeemedOnce checks the link of a download.
func TestADownloadTokenIsRedeemedOnce(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	content := []byte("a stored note")
	digest := sha256.Sum256(content)
	answer := beginUpload(t, admin, "note.txt", int64(len(content)))
	if resp, data := putUpload(t, admin, answer.Upload.ID, content); resp.StatusCode != http.StatusOK {
		t.Fatalf("the transfer answered %d: %s", resp.StatusCode, data)
	}
	resp, data := admin.do(http.MethodPost, "/api/v1/uploads/"+answer.Upload.ID+"/complete",
		map[string]any{"sha256": hex.EncodeToString(digest[:])})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the completion answered %d: %s", resp.StatusCode, data)
	}
	var completed uploadAnswer
	if err := json.Unmarshal(data, &completed); err != nil {
		t.Fatalf("decode the completion: %v", err)
	}

	// The download manager of the node mints the token of the link. It
	// lives next to the artifact store, so the test mints the token the
	// same way the MCP surface does.
	downloads, err := artifacts.NewDownloads(artifacts.DownloadOptions{
		Root: filepath.Join(e.dataDir, "artifacts", "downloads"),
		TTL:  5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create the download manager: %v", err)
	}
	token, err := downloads.Issue(completed.Artifact.ID, "usr_test")
	if err != nil {
		t.Fatalf("issue the token: %v", err)
	}

	// The link opens the artifact without a session.
	resp, data = e.request(http.MethodGet,
		"/api/v1/artifacts/"+completed.Artifact.ID+"/download?token="+token.Token, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the link answered %d, want 200: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "a stored note") {
		t.Errorf("the link returned %q", string(data))
	}

	// The token is spent: the same link answers nothing the second time.
	resp, data = e.request(http.MethodGet,
		"/api/v1/artifacts/"+completed.Artifact.ID+"/download?token="+token.Token, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the spent link answered %d, want 404: %s", resp.StatusCode, data)
	}
}
