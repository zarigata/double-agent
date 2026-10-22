package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/store"
)

// beginUploadRequest is the body of the upload announcement.
type beginUploadRequest struct {
	// Filename is the name the file keeps in the artifact store.
	Filename string `json:"filename"`
	// ContentType is the content type of the file.
	ContentType string `json:"content_type"`
	// SizeBytes is the number of bytes the file holds.
	SizeBytes int64 `json:"size_bytes"`
}

// completeUploadRequest is the body of the upload completion.
type completeUploadRequest struct {
	// SHA256 is the digest of the content the client sent.
	SHA256 string `json:"sha256"`
}

// uploadView is the public shape of an upload.
//
// The path of the pending file is never part of it: the bytes of an upload
// are not readable, only the owner of the upload may complete it.
type uploadView struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	ContentType string  `json:"content_type,omitempty"`
	Size        int64   `json:"size"`
	Received    int64   `json:"received"`
	Digest      string  `json:"digest,omitempty"`
	State       string  `json:"state"`
	UploadURL   string  `json:"upload_url"`
	ArtifactID  string  `json:"artifact_id,omitempty"`
	CreatedAt   string  `json:"created_at"`
	ExpiresAt   *string `json:"expires_at,omitempty"`
}

// handleUploadBegin announces an upload to the node.
func (s *Server) handleUploadBegin(w http.ResponseWriter, r *http.Request) {
	if s.uploads == nil {
		writeError(w, r, CodeCapabilityUnavailable, "the node has no upload store", nil)
		return
	}
	var request beginUploadRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	name := strings.TrimSpace(request.Filename)
	if name == "" {
		writeError(w, r, CodeInvalidRequest, "the name of the file is required", nil)
		return
	}
	if request.SizeBytes < 0 {
		writeError(w, r, CodeInvalidRequest, "the size of the file is negative", nil)
		return
	}
	upload, err := s.uploads.Begin(actorFrom(r).ID, name, request.ContentType, request.SizeBytes)
	if err != nil {
		classify(err).write(w, r)
		return
	}
	s.audit(r, actorFrom(r), "upload.begin", store.DecisionAllow, upload.ID)
	writeJSON(w, r, http.StatusCreated, map[string]any{"upload": viewUpload(upload)})
}

// handleUploadContent receives the bytes of an upload.
//
// The body is the raw content of the file. The node refuses a body whose
// length differs from the announced one, so a truncated or an oversized
// transfer never reaches the artifact store.
func (s *Server) handleUploadContent(w http.ResponseWriter, r *http.Request) {
	if s.uploads == nil {
		writeError(w, r, CodeCapabilityUnavailable, "the node has no upload store", nil)
		return
	}
	id := pathValue(r, "id")
	upload, err := s.uploads.Status(actorFrom(r).ID, id)
	if err != nil {
		classify(err).write(w, r)
		return
	}
	if upload.State != artifacts.UploadPending {
		writeError(w, r, CodeConflict, "the upload is no longer pending",
			map[string]any{"upload_id": id, "state": string(upload.State)})
		return
	}
	if r.ContentLength >= 0 && r.ContentLength != upload.Size {
		writeError(w, r, CodeInvalidRequest, "the length of the body does not match the announced size",
			map[string]any{"upload_id": id, "announced_bytes": upload.Size, "body_bytes": r.ContentLength})
		return
	}
	// The reader stops one byte after the announced size, so a body that
	// is too long is refused instead of filling the disk.
	r.Body = http.MaxBytesReader(w, r.Body, upload.Size+1)
	upload, err = s.uploads.Write(actorFrom(r).ID, id, r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, CodeInvalidRequest, "the body is larger than the announced size",
				map[string]any{"upload_id": id, "announced_bytes": upload.Size})
			return
		}
		classify(err).write(w, r)
		return
	}
	s.audit(r, actorFrom(r), "upload.content", store.DecisionAllow, upload.ID)
	writeJSON(w, r, http.StatusOK, map[string]any{"upload": viewUpload(upload)})
}

// handleUploadComplete closes an upload and records the artifact.
func (s *Server) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	if s.uploads == nil {
		writeError(w, r, CodeCapabilityUnavailable, "the node has no upload store", nil)
		return
	}
	var request completeUploadRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	id := pathValue(r, "id")
	artifactID := ids.New(ids.PrefixArtifact)
	_, info, err := s.uploads.Complete(actorFrom(r).ID, id, request.SHA256, artifactID)
	if err != nil {
		classify(err).write(w, r)
		return
	}
	record, err := s.store.CreateArtifact(r.Context(), store.Artifact{
		ID:        artifactID,
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
	s.audit(r, actorFrom(r), "upload.complete", store.DecisionAllow, record.ID)
	writeJSON(w, r, http.StatusCreated, map[string]any{"artifact": viewArtifact(record)})
}

// handleUploadStatus answers with the state of an upload.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	if s.uploads == nil {
		writeError(w, r, CodeCapabilityUnavailable, "the node has no upload store", nil)
		return
	}
	upload, err := s.uploads.Status(actorFrom(r).ID, pathValue(r, "id"))
	if err != nil {
		classify(err).write(w, r)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"upload": viewUpload(upload)})
}

// viewUpload renders an upload for a client.
func viewUpload(upload artifacts.Upload) uploadView {
	view := uploadView{
		ID:          upload.ID,
		Name:        upload.Name,
		ContentType: upload.ContentType,
		Size:        upload.Size,
		Received:    upload.Received,
		Digest:      upload.Digest,
		State:       string(upload.State),
		UploadURL:   "/api/v1/uploads/" + upload.ID,
		ArtifactID:  upload.ArtifactID,
		CreatedAt:   upload.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if upload.ExpiresAt != nil {
		deadline := upload.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		view.ExpiresAt = &deadline
	}
	return view
}
