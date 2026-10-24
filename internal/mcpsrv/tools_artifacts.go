package mcpsrv

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// ArtifactListInput narrows an artifact listing.
type ArtifactListInput struct {
	// Limit caps the number of artifacts. The node returns at most a hundred.
	Limit int `json:"limit,omitempty" jsonschema:"The greatest number of artifacts to return. Leave it empty for the default of fifty."`
}

// ArtifactListOutput carries the artifacts of the account.
type ArtifactListOutput struct {
	// Artifacts are the artifacts, newest first.
	Artifacts []ArtifactView `json:"artifacts"`
}

// ArtifactExportInput asks for the export of the files a workload produced.
type ArtifactExportInput struct {
	// InstanceID identifies the instance the files come from.
	InstanceID string `json:"instance_id" jsonschema:"The identifier of the instance, for example \"ins_01J...\"."`
	// Paths are the paths of the files inside the workspace of the
	// instance.
	Paths []string `json:"paths,omitempty" jsonschema:"The paths of the files to export, for example [\"report.txt\"]. The paths are relative to the root of the workspace."`
	// Path is a single file to export. It is accepted as a shorthand for
	// a list of one path.
	Path string `json:"path,omitempty" jsonschema:"The path of one file to export. Leave it empty when the paths are given."`
}

// ArtifactExportOutput carries the exported artifacts.
type ArtifactExportOutput struct {
	// Artifacts are the exported files, in the order they were requested.
	Artifacts []ArtifactView `json:"artifacts"`
}

// ArtifactDownloadInput selects the artifact to download.
type ArtifactDownloadInput struct {
	// ArtifactID identifies the artifact.
	ArtifactID string `json:"artifact_id" jsonschema:"The identifier of the artifact, for example \"art_01J...\"."`
}

// ArtifactDownloadOutput describes the download of an artifact.
//
// The content of the file is never embedded in the answer: a large file would
// blow up the context of the client. The answer carries a short-lived,
// single-use link that serves it instead.
type ArtifactDownloadOutput struct {
	// ArtifactID is the artifact that was requested.
	ArtifactID string `json:"artifact_id"`
	// Name is the display name of the file.
	Name string `json:"name"`
	// MIME is the content type of the file.
	MIME string `json:"mime"`
	// Size is the length of the file, in bytes.
	Size int64 `json:"size"`
	// Digest is the SHA-256 of the content, hex encoded.
	Digest string `json:"digest"`
	// State is the lifecycle state of the artifact.
	State string `json:"state"`
	// DownloadURL is the endpoint that serves the content. The link
	// carries a single-use token, so it stops working once it was used.
	DownloadURL string `json:"download_url"`
	// TokenExpiresAt is the deadline of the token of the link.
	TokenExpiresAt *string `json:"token_expires_at,omitempty"`
	// ExpiresAt is the retention deadline of the artifact.
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// addArtifactTools registers the tools of the artifacts.
func (s *Server) addArtifactTools(server *mcp.Server, actor policy.Actor) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "artifact.list",
		Description: "List the artifacts of the account, newest first. An artifact is a " +
			"file a workload exported; the content is never embedded in the answer.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ArtifactListInput) (*mcp.CallToolResult, ArtifactListOutput, error) {
		if failure := requireScope(actor, policy.ScopeArtifactsRead, correlationIDFrom(ctx)); failure != nil {
			return nil, ArtifactListOutput{}, failure
		}
		out, err := s.listArtifacts(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "artifact.export",
		Description: "Export the files a workload produced inside an instance. The node " +
			"copies the files out of the instance, hashes them and records them as " +
			"artifacts. The copy lands outside of the workspace of the guest, so the " +
			"workload cannot read it back.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ArtifactExportInput) (*mcp.CallToolResult, ArtifactExportOutput, error) {
		if failure := requireScope(actor, policy.ScopeArtifactsWrite, correlationIDFrom(ctx)); failure != nil {
			return nil, ArtifactExportOutput{}, failure
		}
		out, err := s.exportArtifacts(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "artifact.download",
		Description: "Describe the download of an artifact: its name, its size, its digest " +
			"and a short-lived link that serves it. The link carries a token that may be " +
			"redeemed once, so it stops working as soon as it was used.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ArtifactDownloadInput) (*mcp.CallToolResult, ArtifactDownloadOutput, error) {
		if failure := requireScope(actor, policy.ScopeArtifactsRead, correlationIDFrom(ctx)); failure != nil {
			return nil, ArtifactDownloadOutput{}, failure
		}
		out, err := s.downloadArtifact(ctx, actor, input)
		return nil, out, err
	})
}

// listArtifacts lists the artifacts of the account.
func (s *Server) listArtifacts(ctx context.Context, actor policy.Actor, input ArtifactListInput) (ArtifactListOutput, error) {
	correlationID := correlationIDFrom(ctx)
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	records, err := s.store.ListArtifacts(ctx, actor.ID, limit)
	if err != nil {
		return ArtifactListOutput{}, classify(err, correlationID)
	}
	views := make([]ArtifactView, 0, len(records))
	for _, record := range records {
		if !ownsResource(actor, record.UserID) {
			continue
		}
		views = append(views, viewArtifact(record))
	}
	return ArtifactListOutput{Artifacts: views}, nil
}

// exportArtifacts exports the files a workload produced.
func (s *Server) exportArtifacts(ctx context.Context, actor policy.Actor, input ArtifactExportInput) (ArtifactExportOutput, error) {
	correlationID := correlationIDFrom(ctx)
	instanceID := strings.TrimSpace(input.InstanceID)
	if instanceID == "" {
		return ArtifactExportOutput{}, invalidRequest("the instance identifier is required", correlationID)
	}
	paths := make([]string, 0, len(input.Paths)+1)
	for _, item := range input.Paths {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	if trimmed := strings.TrimSpace(input.Path); trimmed != "" {
		paths = append(paths, trimmed)
	}
	if len(paths) == 0 {
		return ArtifactExportOutput{}, invalidRequest("the path of the file to export is required", correlationID)
	}
	if s.orch == nil {
		return ArtifactExportOutput{}, &ToolError{
			Code:          CodeCapabilityUnavailable,
			Message:       "the node has no control plane",
			CorrelationID: correlationID,
		}
	}
	result, err := s.orch.Export(ctx, orchestrator.ExportRequest{
		Actor:         actor,
		InstanceID:    instanceID,
		Paths:         paths,
		CorrelationID: correlationID,
	})
	if err != nil {
		return ArtifactExportOutput{}, classify(err, correlationID)
	}
	views := make([]ArtifactView, 0, len(result.Artifacts))
	for _, record := range result.Artifacts {
		views = append(views, viewArtifact(record))
	}
	return ArtifactExportOutput{Artifacts: views}, nil
}

// downloadArtifact describes the download of an artifact.
//
// The answer carries a link with a single-use token: the token may be
// redeemed once and it expires shortly. The path of the file on the host
// never leaves the node.
func (s *Server) downloadArtifact(ctx context.Context, actor policy.Actor, input ArtifactDownloadInput) (ArtifactDownloadOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.ArtifactID)
	if id == "" {
		return ArtifactDownloadOutput{}, invalidRequest("the artifact identifier is required", correlationID)
	}
	record, err := s.store.ArtifactByID(ctx, id)
	if err != nil {
		return ArtifactDownloadOutput{}, classify(err, correlationID)
	}
	if !ownsResource(actor, record.UserID) {
		return ArtifactDownloadOutput{}, permissionDenied(
			"the artifact belongs to another account",
			correlationID,
			map[string]any{"artifact_id": id},
		)
	}
	if record.State != store.ArtifactAvailable {
		return ArtifactDownloadOutput{}, &ToolError{
			Code:          CodeArtifactExpired,
			Message:       "the artifact is no longer available",
			CorrelationID: correlationID,
			Details:       map[string]any{"artifact_id": id, "state": string(record.State)},
		}
	}
	if s.downloads == nil {
		return ArtifactDownloadOutput{}, &ToolError{
			Code:          CodeCapabilityUnavailable,
			Message:       "the node has no download manager",
			CorrelationID: correlationID,
		}
	}
	token, err := s.downloads.Issue(record.ID, actor.ID)
	if err != nil {
		return ArtifactDownloadOutput{}, classify(err, correlationID)
	}
	view := viewArtifact(record)
	out := ArtifactDownloadOutput{
		ArtifactID:  view.ID,
		Name:        view.Name,
		MIME:        view.MIME,
		Size:        view.Size,
		Digest:      view.Digest,
		State:       view.State,
		DownloadURL: view.DownloadURL + "?token=" + token.Token,
	}
	deadline := token.ExpiresAt.Format(time.RFC3339)
	out.TokenExpiresAt = &deadline
	if view.ExpiresAt != nil {
		expiry := view.ExpiresAt.Format(time.RFC3339)
		out.ExpiresAt = &expiry
	}
	return out, nil
}
