package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// artifactView is the public shape of an artifact. The path of the file on
// the host is never part of it.
type artifactView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	MIME        string     `json:"mime"`
	Size        int64      `json:"size"`
	Digest      string     `json:"digest"`
	State       string     `json:"state"`
	JobID       string     `json:"job_id,omitempty"`
	InstanceID  string     `json:"instance_id,omitempty"`
	OwnerID     string     `json:"owner_id"`
	DownloadURL string     `json:"download_url"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// handleArtifactList answers with the artifacts of the caller.
func (s *Server) handleArtifactList(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListArtifacts(r.Context(), scopedUserID(actorFrom(r)), limitQuery(r))
	if err != nil {
		writeError(w, r, CodeInternal, "the artifacts could not be loaded", nil)
		return
	}
	views := make([]artifactView, 0, len(records))
	for _, record := range records {
		views = append(views, viewArtifact(record))
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"artifacts": views})
}

// handleArtifactGet answers with the metadata of an artifact.
func (s *Server) handleArtifactGet(w http.ResponseWriter, r *http.Request) {
	record, ok := s.loadArtifact(w, r)
	if !ok {
		return
	}
	writeJSON(w, r, http.StatusOK, viewArtifact(record))
}

// handleArtifactDownload sends the content of an artifact.
//
// The download is authenticated and ownership checked on every request: a
// link is never a capability. A request that carries a token of a download
// grant does not need a session, because the token is the capability: it
// may be redeemed once and it expires shortly.
func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request) {
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		s.serveArtifactToken(w, r, token)
		return
	}
	actor := actorFrom(r)
	if actor.ID == "" {
		writeError(w, r, CodeAuthenticationFailed, "the request is not authenticated", nil)
		return
	}
	decision := policy.Evaluate(actor, policy.ScopeArtifactsRead)
	if !decision.Allowed {
		s.audit(r, actor, "artifact.download", store.DecisionDeny, decision.Reason)
		writeError(w, r, CodePermissionDenied, "the request is not allowed for this actor",
			map[string]any{"scope": string(policy.ScopeArtifactsRead)})
		return
	}
	record, ok := s.loadArtifact(w, r)
	if !ok {
		return
	}
	s.serveArtifact(w, r, record)
}

// serveArtifactToken serves the content of an artifact with a download
// token.
//
// The token is redeemed before the content is served: a token that was
// already used, that expired or that opens another artifact is refused
// with the not_found code, so a leaked link reveals nothing.
func (s *Server) serveArtifactToken(w http.ResponseWriter, r *http.Request, token string) {
	if s.downloads == nil {
		writeError(w, r, CodeCapabilityUnavailable, "the node has no download manager", nil)
		return
	}
	id := pathValue(r, "id")
	grant, err := s.downloads.Redeem(token, id)
	if err != nil {
		// The cause stays in the log: it names the state of the token,
		// never a path of the host.
		s.log.Warn("the download link was refused", "error", err, "request_id", requestID(r))
		writeError(w, r, CodeNotFound, "the download link is not available", nil)
		return
	}
	record, err := s.store.ArtifactByID(r.Context(), grant.ArtifactID)
	if err != nil {
		writeError(w, r, CodeNotFound, "the artifact does not exist", nil)
		return
	}
	if record.State != store.ArtifactAvailable {
		writeError(w, r, CodeArtifactExpired, "the artifact is no longer available",
			map[string]any{"artifact_id": record.ID})
		return
	}
	s.serveArtifact(w, r, record)
}

// serveArtifact writes the content of an artifact to the client.
func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request, record store.Artifact) {
	file, err := os.Open(record.Path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, r, CodeArtifactExpired, "the artifact is no longer available",
				map[string]any{"artifact_id": record.ID})
			return
		}
		writeError(w, r, CodeInternal, "the artifact could not be read", nil)
		return
	}
	defer file.Close()
	mime := record.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	header := w.Header()
	header.Set("Content-Type", mime)
	header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", record.Name))
	header.Set("Content-Length", fmt.Sprintf("%d", record.Size))
	header.Set("X-Content-Digest", record.Digest)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, file)
}

// handleArtifactDelete expires an artifact.
//
// The file is removed from the store and the record is marked as deleted,
// so the audit trail still shows that the artifact existed.
func (s *Server) handleArtifactDelete(w http.ResponseWriter, r *http.Request) {
	record, ok := s.loadArtifact(w, r)
	if !ok {
		return
	}
	if s.artifacts != nil {
		if err := s.artifacts.Remove(record.UserID, filepath.Base(record.Path)); err != nil && !errors.Is(err, artifacts.ErrNotFound) {
			writeError(w, r, CodeInternal, "the artifact could not be removed", nil)
			return
		}
	}
	if err := s.store.SetArtifactState(r.Context(), record.ID, store.ArtifactDeleted); err != nil {
		writeError(w, r, CodeInternal, "the artifact could not be expired", nil)
		return
	}
	s.audit(r, actorFrom(r), "artifact.delete", store.DecisionAllow, record.ID)
	writeJSON(w, r, http.StatusOK, viewArtifact(record))
}

// exportRequest is the body of the export endpoint.
type exportRequest struct {
	// InstanceID identifies the instance the files come from.
	InstanceID string `json:"instance_id"`
	// Paths are the paths of the files inside the workspace of the
	// instance.
	Paths []string `json:"paths"`
	// Path is a single file to export.
	Path string `json:"path"`
}

// handleArtifactExport exports the files a workload produced.
func (s *Server) handleArtifactExport(w http.ResponseWriter, r *http.Request) {
	var request exportRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	instanceID := strings.TrimSpace(request.InstanceID)
	if instanceID == "" {
		writeError(w, r, CodeInvalidRequest, "the instance identifier is required", nil)
		return
	}
	paths := make([]string, 0, len(request.Paths)+1)
	for _, item := range request.Paths {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	if trimmed := strings.TrimSpace(request.Path); trimmed != "" {
		paths = append(paths, trimmed)
	}
	if len(paths) == 0 {
		writeError(w, r, CodeInvalidRequest, "the path of the file to export is required", nil)
		return
	}
	result, err := s.orch.Export(r.Context(), orchestrator.ExportRequest{
		Actor:            actorFrom(r),
		InstanceID:       instanceID,
		Paths:            paths,
		CorrelationID:    requestID(r),
		MaxArtifactBytes: s.cfg.Artifacts.MaxSizeBytes,
		MaxTotalBytes:    s.cfg.Artifacts.MaxSizeBytes,
	})
	if err != nil {
		classify(err).write(w, r)
		return
	}
	views := make([]artifactView, 0, len(result.Artifacts))
	for _, record := range result.Artifacts {
		views = append(views, viewArtifact(record))
	}
	s.audit(r, actorFrom(r), "artifact.export", store.DecisionAllow, instanceID)
	writeJSON(w, r, http.StatusCreated, map[string]any{"artifacts": views})
}

// handleArtifactUpload stores a file in the artifact store.
//
// The body is either a multipart form with a "file" part or the raw content
// of the file. The name of the file is taken from the "name" query parameter
// when the body is raw.
func (s *Server) handleArtifactUpload(w http.ResponseWriter, r *http.Request) {
	if s.artifacts == nil {
		writeError(w, r, CodeCapabilityUnavailable, "the artifact store is not available", nil)
		return
	}
	limit := s.cfg.Artifacts.MaxUploadBytes
	if limit <= 0 {
		limit = defaultUploadLimit
	}
	name, reader, closer, err := s.uploadSource(w, r, limit)
	if err != nil {
		writeDecodeError(w, r, err)
		return
	}
	if closer != nil {
		defer closer.Close()
	}
	info, err := s.artifacts.Put(actorFrom(r).ID, name, reader)
	if err != nil {
		if errors.Is(err, artifacts.ErrTooLarge) {
			writeError(w, r, CodeInvalidRequest, "the file is larger than the accepted size",
				map[string]any{"limit_bytes": limit})
			return
		}
		if errors.Is(err, artifacts.ErrInvalidName) {
			writeError(w, r, CodeInvalidRequest, "the file name is not acceptable", nil)
			return
		}
		writeError(w, r, CodeInternal, "the file could not be stored", nil)
		return
	}
	record, err := s.store.CreateArtifact(r.Context(), store.Artifact{
		ID:        ids.New(ids.PrefixArtifact),
		UserID:    actorFrom(r).ID,
		Name:      info.Name,
		Path:      info.Path,
		MIME:      info.MIME,
		Size:      info.Size,
		Digest:    info.Digest,
		State:     store.ArtifactAvailable,
		ExpiresAt: artifacts.Expiry(s.cfg.Artifacts.Retention.Duration(), s.now()),
	})
	if err != nil {
		writeError(w, r, CodeInternal, "the artifact could not be recorded", nil)
		return
	}
	s.audit(r, actorFrom(r), "artifact.upload", store.DecisionAllow, record.ID)
	writeJSON(w, r, http.StatusCreated, map[string]any{"artifact": viewArtifact(record)})
}

// uploadSource reads the name and the content of an upload.
func (s *Server) uploadSource(w http.ResponseWriter, r *http.Request, limit int64) (string, io.Reader, io.Closer, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			return "", nil, nil, errorf(CodeInvalidRequest, "the multipart body could not be read", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			return "", nil, nil, errorf(CodeInvalidRequest, "the file part is missing", err)
		}
		name := header.Filename
		if requested := strings.TrimSpace(r.FormValue("name")); requested != "" {
			name = requested
		}
		return name, file, file, nil
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "upload.bin"
	}
	return name, http.MaxBytesReader(w, r.Body, limit), nil, nil
}

// loadArtifact loads an artifact and checks that the caller owns it.
func (s *Server) loadArtifact(w http.ResponseWriter, r *http.Request) (store.Artifact, bool) {
	id := pathValue(r, "id")
	record, err := s.store.ArtifactByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, CodeNotFound, "the artifact does not exist",
				map[string]any{"artifact_id": id})
			return store.Artifact{}, false
		}
		writeError(w, r, CodeInternal, "the artifact could not be loaded", nil)
		return store.Artifact{}, false
	}
	if !ownsResource(actorFrom(r), record.UserID) {
		writeError(w, r, CodePermissionDenied, "the artifact belongs to another user",
			map[string]any{"artifact_id": id})
		return store.Artifact{}, false
	}
	return record, true
}

// viewArtifact renders an artifact for a client.
func viewArtifact(record store.Artifact) artifactView {
	return artifactView{
		ID:          record.ID,
		Name:        record.Name,
		MIME:        record.MIME,
		Size:        record.Size,
		Digest:      record.Digest,
		State:       string(record.State),
		JobID:       record.JobID,
		InstanceID:  record.InstanceID,
		OwnerID:     record.UserID,
		DownloadURL: "/api/v1/artifacts/" + record.ID + "/download",
		CreatedAt:   record.CreatedAt,
		ExpiresAt:   record.ExpiresAt,
	}
}
