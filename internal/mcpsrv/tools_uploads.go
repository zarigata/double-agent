package mcpsrv

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// FileUploadBeginInput describes the file a client wants to upload.
type FileUploadBeginInput struct {
	// Filename is the name the file keeps in the artifact store.
	Filename string `json:"filename" jsonschema:"The name of the file, for example \"report.txt\"."`
	// ContentType is the content type of the file.
	ContentType string `json:"content_type,omitempty" jsonschema:"The content type of the file, for example \"text/plain\". Leave it empty to let the node guess it."`
	// SizeBytes is the number of bytes the file holds.
	SizeBytes int64 `json:"size_bytes" jsonschema:"The exact number of bytes of the file. The node refuses a body of a different size."`
}

// FileUploadBeginOutput carries the handle of a pending upload.
type FileUploadBeginOutput struct {
	// UploadID identifies the upload. It is the opaque handle of the
	// transfer.
	UploadID string `json:"upload_id"`
	// UploadURL is the endpoint that accepts the bytes. The client sends
	// the raw content of the file with a PUT request.
	UploadURL string `json:"upload_url"`
	// ExpiresAt is the deadline of the upload.
	ExpiresAt *string `json:"expires_at,omitempty"`
	// MaxBytes is the largest file the node accepts.
	MaxBytes int64 `json:"max_bytes"`
}

// FileUploadCompleteInput closes a pending upload.
type FileUploadCompleteInput struct {
	// UploadID identifies the upload.
	UploadID string `json:"upload_id" jsonschema:"The identifier of the upload, for example \"upl_01J...\"."`
	// SHA256 is the digest of the content the client sent.
	SHA256 string `json:"sha256" jsonschema:"The sha256 of the file, hex encoded. The node refuses the upload when the bytes do not match it."`
}

// FileUploadCompleteOutput carries the artifact the upload produced.
type FileUploadCompleteOutput struct {
	// Artifact is the file that joined the artifact store.
	Artifact ArtifactView `json:"artifact"`
}

// addUploadTools registers the tools of the uploads.
func (s *Server) addUploadTools(server *mcp.Server, actor policy.Actor) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "file.upload.begin",
		Description: "Announce a file to upload. The node reserves a place for the bytes " +
			"and answers with an opaque handle and the endpoint that accepts them. Send the " +
			"raw content of the file to that endpoint with a PUT request, then call " +
			"file.upload.complete with the digest of the file.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input FileUploadBeginInput) (*mcp.CallToolResult, FileUploadBeginOutput, error) {
		if failure := requireScope(actor, policy.ScopeArtifactsWrite, correlationIDFrom(ctx)); failure != nil {
			return nil, FileUploadBeginOutput{}, failure
		}
		out, err := s.beginUpload(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "file.upload.complete",
		Description: "Close an upload. The node hashes the bytes it received, compares them " +
			"with the digest of the call and moves the file into the artifact store. A file " +
			"that does not match its digest is refused and never reaches the store.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input FileUploadCompleteInput) (*mcp.CallToolResult, FileUploadCompleteOutput, error) {
		if failure := requireScope(actor, policy.ScopeArtifactsWrite, correlationIDFrom(ctx)); failure != nil {
			return nil, FileUploadCompleteOutput{}, failure
		}
		out, err := s.completeUpload(ctx, actor, input)
		return nil, out, err
	})
}

// beginUpload announces an upload to the node.
func (s *Server) beginUpload(ctx context.Context, actor policy.Actor, input FileUploadBeginInput) (FileUploadBeginOutput, error) {
	correlationID := correlationIDFrom(ctx)
	name := strings.TrimSpace(input.Filename)
	if name == "" {
		return FileUploadBeginOutput{}, invalidRequest("the name of the file is required", correlationID)
	}
	if input.SizeBytes < 0 {
		return FileUploadBeginOutput{}, invalidRequest("the size of the file is negative", correlationID)
	}
	if s.uploads == nil {
		return FileUploadBeginOutput{}, &ToolError{
			Code:          CodeCapabilityUnavailable,
			Message:       "the node has no upload store",
			CorrelationID: correlationID,
		}
	}
	upload, err := s.uploads.Begin(actor.ID, name, input.ContentType, input.SizeBytes)
	if err != nil {
		return FileUploadBeginOutput{}, classify(err, correlationID)
	}
	out := FileUploadBeginOutput{
		UploadID:  upload.ID,
		UploadURL: "/api/v1/uploads/" + upload.ID,
		MaxBytes:  s.uploads.MaxBytes(),
	}
	if upload.ExpiresAt != nil {
		deadline := upload.ExpiresAt.Format(time.RFC3339)
		out.ExpiresAt = &deadline
	}
	return out, nil
}

// completeUpload closes an upload and records the artifact it produced.
func (s *Server) completeUpload(ctx context.Context, actor policy.Actor, input FileUploadCompleteInput) (FileUploadCompleteOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.UploadID)
	if id == "" {
		return FileUploadCompleteOutput{}, invalidRequest("the upload identifier is required", correlationID)
	}
	if s.uploads == nil {
		return FileUploadCompleteOutput{}, &ToolError{
			Code:          CodeCapabilityUnavailable,
			Message:       "the node has no upload store",
			CorrelationID: correlationID,
		}
	}
	artifactID := ids.New(ids.PrefixArtifact)
	_, info, err := s.uploads.Complete(actor.ID, id, input.SHA256, artifactID)
	if err != nil {
		return FileUploadCompleteOutput{}, classify(err, correlationID)
	}
	record, err := s.store.CreateArtifact(ctx, store.Artifact{
		ID:        artifactID,
		UserID:    actor.ID,
		Name:      info.Name,
		Path:      info.Path,
		MIME:      info.MIME,
		Size:      info.Size,
		Digest:    info.Digest,
		State:     store.ArtifactAvailable,
		ExpiresAt: artifacts.Expiry(s.retention, s.now()),
	})
	if err != nil {
		return FileUploadCompleteOutput{}, classify(err, correlationID)
	}
	return FileUploadCompleteOutput{Artifact: viewArtifact(record)}, nil
}
