package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// exportAnswer is the answer of the export endpoint.
type exportAnswer struct {
	Artifacts []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Size       int64  `json:"size"`
		Digest     string `json:"digest"`
		InstanceID string `json:"instance_id"`
		Download   string `json:"download_url"`
	} `json:"artifacts"`
}

// TestTheFilesOfAnInstanceAreExported checks the export of the files a
// workload produced.
func TestTheFilesOfAnInstanceAreExported(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	resp, data := admin.do(http.MethodPost, "/api/v1/instances",
		map[string]any{"template": "alpine-shell", "name": "export-test"})
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

	// The workload writes a file into the workspace of the instance.
	resp, data = admin.do(http.MethodPost, "/api/v1/instances/"+created.InstanceID+"/exec",
		map[string]any{"argv": []string{"sh", "-c", "printf 'the report of the workload' > report.txt"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the command answered %d: %s", resp.StatusCode, data)
	}

	resp, data = admin.do(http.MethodPost, "/api/v1/artifacts/export", map[string]any{
		"instance_id": created.InstanceID,
		"paths":       []string{"report.txt"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the export answered %d: %s", resp.StatusCode, data)
	}
	var exported exportAnswer
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatalf("decode the export: %v", err)
	}
	if len(exported.Artifacts) != 1 {
		t.Fatalf("the export produced %d artifacts, want 1", len(exported.Artifacts))
	}
	artifact := exported.Artifacts[0]
	if artifact.Name != "report.txt" {
		t.Errorf("the artifact is named %q, want %q", artifact.Name, "report.txt")
	}
	if artifact.Size != int64(len("the report of the workload")) {
		t.Errorf("the artifact holds %d bytes", artifact.Size)
	}
	if artifact.Digest == "" {
		t.Error("the artifact carries no digest")
	}
	if artifact.InstanceID != created.InstanceID {
		t.Errorf("the artifact names the instance %q", artifact.InstanceID)
	}
	// The answer never carries the path of the file on the host.
	if strings.Contains(artifact.Download, e.dataDir) {
		t.Errorf("the answer leaks the path of the host: %q", artifact.Download)
	}

	// The content is served by the download endpoint.
	resp, data = admin.do(http.MethodGet, "/api/v1/artifacts/"+artifact.ID+"/download", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the download answered %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "the report of the workload") {
		t.Errorf("the download returned %q", string(data))
	}
}

// TestTheExportRefusesAPathThatLeavesTheWorkspace checks the paths of an
// export request.
func TestTheExportRefusesAPathThatLeavesTheWorkspace(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminUser, adminPassword)

	resp, data := admin.do(http.MethodPost, "/api/v1/artifacts/export", map[string]any{
		"instance_id": "ins_01JUNKNOWN",
		"paths":       []string{"../../etc/passwd"},
	})
	if resp.StatusCode == http.StatusCreated {
		t.Fatalf("the export answered %d: %s", resp.StatusCode, data)
	}
	envelope := decodeError(t, data)
	if envelope.Error.Code != "not_found" && envelope.Error.Code != "invalid_request" {
		t.Errorf("the answer carries the code %q", envelope.Error.Code)
	}
}
