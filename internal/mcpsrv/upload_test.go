package mcpsrv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zarigata/double-agent/internal/api"
	"github.com/zarigata/double-agent/internal/config"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// TestFileUploadBeginsAndCompletes walks through the whole upload: the
// client announces the file, sends the bytes and closes the upload with the
// digest of the file.
func TestFileUploadBeginsAndCompletes(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	content := []byte("the source of the workload")
	digest := sha256.Sum256(content)

	var begun FileUploadBeginOutput
	decodeOutput(t, callTool(t, session, "file.upload.begin", map[string]any{
		"filename":     "main.go",
		"content_type": "text/plain",
		"size_bytes":   len(content),
	}), &begun)

	if begun.UploadID == "" {
		t.Fatal("the announcement returned no handle")
	}
	if !strings.HasSuffix(begun.UploadURL, begun.UploadID) {
		t.Errorf("the endpoint %q does not carry the handle %q", begun.UploadURL, begun.UploadID)
	}
	if begun.MaxBytes <= 0 {
		t.Errorf("the announcement reports the limit %d", begun.MaxBytes)
	}

	// The client sends the bytes to the endpoint of the announcement. The
	// node writes them into the pending file of the upload.
	if _, err := env.server.uploads.Write(env.user.ID, begun.UploadID, bytes.NewReader(content)); err != nil {
		t.Fatalf("write the bytes of the upload: %v", err)
	}

	var completed FileUploadCompleteOutput
	decodeOutput(t, callTool(t, session, "file.upload.complete", map[string]any{
		"upload_id": begun.UploadID,
		"sha256":    hex.EncodeToString(digest[:]),
	}), &completed)

	artifact := completed.Artifact
	if artifact.ID == "" {
		t.Fatal("the completion returned no artifact")
	}
	if artifact.Name != "main.go" {
		t.Errorf("the artifact is named %q, want %q", artifact.Name, "main.go")
	}
	if artifact.Size != int64(len(content)) {
		t.Errorf("the artifact holds %d bytes, want %d", artifact.Size, len(content))
	}
	if artifact.Digest != hex.EncodeToString(digest[:]) {
		t.Errorf("the artifact carries the digest %q", artifact.Digest)
	}

	// The file joined the artifact store of the account.
	record, err := env.store.ArtifactByID(context.Background(), artifact.ID)
	if err != nil {
		t.Fatalf("load the artifact: %v", err)
	}
	if record.UserID != env.user.ID {
		t.Errorf("the artifact belongs to %q", record.UserID)
	}
	if record.Digest != hex.EncodeToString(digest[:]) {
		t.Errorf("the record carries the digest %q", record.Digest)
	}
}

// TestTheUploadFlowCrossesTheTwoSurfaces proves that the upload a client
// announces through the MCP surface may be sent to the REST surface and
// closed through the MCP surface again.
func TestTheUploadFlowCrossesTheTwoSurfaces(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	// The REST surface of the same node, over the same artifact store.
	cfg := config.Default()
	cfg.DataDir = env.dir
	cfg.Artifacts.Root = env.artifacts.Root()
	server := httptest.NewServer(api.New(api.Options{
		Store:        env.store,
		Orchestrator: env.orch,
		Artifacts:    env.artifacts,
		Config:       cfg,
		Registry:     env.registry,
		Catalogue:    env.orch.Catalogue(),
	}))
	t.Cleanup(server.Close)

	content := []byte("package main")
	digest := sha256.Sum256(content)

	var begun FileUploadBeginOutput
	decodeOutput(t, callTool(t, session, "file.upload.begin", map[string]any{
		"filename":   "main.go",
		"size_bytes": len(content),
	}), &begun)

	// The client sends the bytes to the endpoint of the announcement with
	// its credential.
	req, err := http.NewRequest(http.MethodPut, server.URL+begun.UploadURL, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("perform the request: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the transfer answered %d: %s", resp.StatusCode, data)
	}

	// The client closes the upload through the MCP surface.
	var completed FileUploadCompleteOutput
	decodeOutput(t, callTool(t, session, "file.upload.complete", map[string]any{
		"upload_id": begun.UploadID,
		"sha256":    hex.EncodeToString(digest[:]),
	}), &completed)
	if completed.Artifact.Name != "main.go" {
		t.Errorf("the artifact is named %q, want %q", completed.Artifact.Name, "main.go")
	}
	if completed.Artifact.Size != int64(len(content)) {
		t.Errorf("the artifact holds %d bytes, want %d", completed.Artifact.Size, len(content))
	}
}

// TestFileUploadRefusesADigestMismatch checks that a file that does not
// match its digest never reaches the artifact store.
func TestFileUploadRefusesADigestMismatch(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	content := []byte("the source of the workload")
	wrong := sha256.Sum256([]byte("another file"))

	var begun FileUploadBeginOutput
	decodeOutput(t, callTool(t, session, "file.upload.begin", map[string]any{
		"filename":   "main.go",
		"size_bytes": len(content),
	}), &begun)

	if _, err := env.server.uploads.Write(env.user.ID, begun.UploadID, bytes.NewReader(content)); err != nil {
		t.Fatalf("write the bytes of the upload: %v", err)
	}

	result := callTool(t, session, "file.upload.complete", map[string]any{
		"upload_id": begun.UploadID,
		"sha256":    hex.EncodeToString(wrong[:]),
	})
	if !result.IsError {
		t.Fatal("the completion of a file with a wrong digest succeeded")
	}
	if code := toolErrorCode(t, result); code != string(CodeDigestMismatch) {
		t.Errorf("the failure carries the code %q, want %q", code, CodeDigestMismatch)
	}

	// The file never reached the store.
	artifacts, err := env.store.ListArtifacts(context.Background(), env.user.ID, 10)
	if err != nil {
		t.Fatalf("list the artifacts: %v", err)
	}
	if len(artifacts) != 0 {
		t.Errorf("the refused upload produced %d artifacts", len(artifacts))
	}
}

// TestFileUploadRefusesATooLargeFile checks the limit of the node.
func TestFileUploadRefusesATooLargeFile(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	result := callTool(t, session, "file.upload.begin", map[string]any{
		"filename":   "huge.bin",
		"size_bytes": 1 << 40,
	})
	if !result.IsError {
		t.Fatal("the announcement of a huge file succeeded")
	}
	if code := toolErrorCode(t, result); code != string(CodeQuotaExceeded) {
		t.Errorf("the failure carries the code %q, want %q", code, CodeQuotaExceeded)
	}
}

// TestFileUploadIsPrivateToItsOwner checks that the upload of one account
// cannot be completed by another one.
func TestFileUploadIsPrivateToItsOwner(t *testing.T) {
	env := newTestEnv(t)
	secret, _ := env.issueKey(t, scopeStrings(policy.DefaultUserScopes)...)
	session := connect(t, env.server.MCPServer(env.actorFor(t, secret)))

	content := []byte("the source of the workload")
	digest := sha256.Sum256(content)

	var begun FileUploadBeginOutput
	decodeOutput(t, callTool(t, session, "file.upload.begin", map[string]any{
		"filename":   "main.go",
		"size_bytes": len(content),
	}), &begun)
	if _, err := env.server.uploads.Write(env.user.ID, begun.UploadID, bytes.NewReader(content)); err != nil {
		t.Fatalf("write the bytes of the upload: %v", err)
	}

	// Another account of the node tries to complete the upload.
	other, err := env.store.CreateUser(context.Background(), store.User{
		ID:       ids.New(ids.PrefixUser),
		Username: "intruder",
		Role:     store.RoleUser,
	})
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}
	if _, _, err := env.server.uploads.Complete(other.ID, begun.UploadID, hex.EncodeToString(digest[:]), "art_01"); err == nil {
		t.Fatal("another account completed the upload")
	}
}
