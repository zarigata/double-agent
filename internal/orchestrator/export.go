package orchestrator

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
)

// ExportRequest is the input of an export request.
type ExportRequest struct {
	// Actor is the authenticated caller.
	Actor policy.Actor
	// InstanceID selects the instance the files come from.
	InstanceID string
	// Paths are the paths of the files inside the workspace of the
	// instance. They are relative to the root of the workspace.
	Paths []string
	// CorrelationID ties the audit records together.
	CorrelationID string
	// MaxArtifactBytes is the largest file accepted. Zero uses the limit of
	// the node.
	MaxArtifactBytes int64
	// MaxTotalBytes is the largest total accepted by one request. Zero
	// uses the limit of the node.
	MaxTotalBytes int64
}

// ExportResult is the outcome of an export request.
type ExportResult struct {
	// Artifacts are the exported files, in the order they were requested.
	Artifacts []store.Artifact
}

// Export copies the files a workload produced out of its instance and into
// the artifact store.
//
// The file leaves the instance through the backend of the runtime, which
// writes it outside of the workspace of the guest: the workload cannot read
// the copy back. The control plane then imports the file into its own
// store, hashes it and records it, so the client receives a digest it can
// verify.
//
// The size of the export is bounded twice: one file may not be larger than
// the limit of the node, and the files of one request may not add up to
// more than the limit either.
func (o *Orchestrator) Export(ctx context.Context, req ExportRequest) (ExportResult, error) {
	decision := policy.Evaluate(req.Actor, policy.ScopeArtifactsWrite)
	if !decision.Allowed {
		o.audit(ctx, req.Actor, "artifact.export", "deny", req.CorrelationID, decision.Reason)
		return ExportResult{}, fmt.Errorf("%w: %s", ErrPolicyDenied, decision.Reason)
	}
	instance, backend, err := o.resolveInstance(ctx, req.Actor, req.InstanceID, policy.ScopeArtifactsWrite)
	if err != nil {
		return ExportResult{}, err
	}
	paths, err := exportPaths(req.Paths)
	if err != nil {
		return ExportResult{}, err
	}
	if instance.State.Terminal() {
		return ExportResult{}, fmt.Errorf("%w: instance %s is %s", ErrInstanceNotFound, instance.ID, instance.State)
	}

	maxArtifact := req.MaxArtifactBytes
	if maxArtifact <= 0 {
		maxArtifact = o.limits.MaxArtifactBytes
	}
	maxTotal := req.MaxTotalBytes
	if maxTotal <= 0 {
		maxTotal = o.limits.MaxExportBytes
	}

	result := ExportResult{Artifacts: make([]store.Artifact, 0, len(paths))}
	var total int64
	for _, item := range paths {
		ref, err := backend.CopyOut(ctx, runtime.ID(instance.RuntimeID), runtime.GuestPath(item))
		if err != nil {
			return ExportResult{}, fmt.Errorf("orchestrator: export %s: %w", item, err)
		}
		if ref.Size > maxArtifact {
			return ExportResult{}, fmt.Errorf("%w: the file %s holds %d bytes, the limit is %d",
				ErrQuotaExceeded, item, ref.Size, maxArtifact)
		}
		total += ref.Size
		if total > maxTotal {
			return ExportResult{}, fmt.Errorf("%w: the export holds more than the %d byte limit",
				ErrQuotaExceeded, maxTotal)
		}
		record, err := o.recordArtifact(ctx, req.Actor, instance, ref)
		if err != nil {
			return ExportResult{}, err
		}
		result.Artifacts = append(result.Artifacts, record)
	}
	o.audit(ctx, req.Actor, "artifact.export", "allow", req.CorrelationID, "")
	return result, nil
}

// recordArtifact imports an exported file into the artifact store and
// records it in the metadata database.
func (o *Orchestrator) recordArtifact(ctx context.Context, actor policy.Actor, instance store.Instance, ref runtime.ArtifactRef) (store.Artifact, error) {
	name := path.Base(strings.TrimSpace(ref.Name))
	if name == "" || name == "." || name == "/" {
		return store.Artifact{}, fmt.Errorf("%w: the exported file has no name", ErrInvalidRequest)
	}
	info, err := o.artifacts.Import(actor.ID, name, ref.Path)
	if err != nil {
		return store.Artifact{}, fmt.Errorf("orchestrator: import the export: %w", err)
	}
	record, err := o.store.CreateArtifact(ctx, store.Artifact{
		ID:         ids.New(ids.PrefixArtifact),
		UserID:     actor.ID,
		JobID:      instance.JobID,
		InstanceID: instance.ID,
		Name:       info.Name,
		Path:       info.Path,
		MIME:       info.MIME,
		Size:       info.Size,
		Digest:     info.Digest,
		State:      store.ArtifactAvailable,
		ExpiresAt:  artifacts.Expiry(o.retention, o.now()),
	})
	if err != nil {
		return store.Artifact{}, fmt.Errorf("orchestrator: record the artifact: %w", err)
	}
	return record, nil
}

// exportPaths checks the paths of an export request.
//
// A path is relative to the root of the workspace of the instance: a path
// that climbs out of it is refused before the backend is asked for it.
func exportPaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, raw := range paths {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "/") || strings.Contains(item, "\\") {
			return nil, fmt.Errorf("%w: the path %q is not relative to the workspace", ErrInvalidRequest, item)
		}
		cleaned := path.Clean(item)
		if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return nil, fmt.Errorf("%w: the path %q leaves the workspace", ErrInvalidRequest, item)
		}
		out = append(out, cleaned)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no path was requested", ErrInvalidRequest)
	}
	return out, nil
}
