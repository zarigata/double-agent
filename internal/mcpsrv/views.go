package mcpsrv

import (
	"time"

	"github.com/zarigata/double-agent/internal/store"
	"github.com/zarigata/double-agent/internal/templates"
)

// ResourcesView is the resource envelope of a workload.
type ResourcesView struct {
	// CPU is the number of cores the workload may use.
	CPU float64 `json:"cpu"`
	// MemoryMB is the memory the workload may use, in megabytes.
	MemoryMB int `json:"memory_mb"`
	// DiskMB is the size of the workspace, in megabytes.
	DiskMB int `json:"disk_mb,omitempty"`
	// GPUCount is the number of accelerators the workload may use.
	GPUCount int `json:"gpu_count,omitempty"`
	// GPUVendor is the family of the accelerators.
	GPUVendor string `json:"gpu_vendor,omitempty"`
}

// NetworkView is the egress policy of a workload.
type NetworkView struct {
	// Egress is one of "none", "restricted" or "allowed".
	Egress string `json:"egress"`
	// Allowlist lists the destinations the workload may reach.
	Allowlist []string `json:"allowlist,omitempty"`
	// AllowedDNS lists the names the workload may resolve.
	AllowedDNS []string `json:"allowed_dns,omitempty"`
}

// TemplateView is the public shape of a template.
//
// The documentation of a template is part of the view: an agent reads it
// before it creates an instance.
type TemplateView struct {
	// ID is the stable identifier of the template.
	ID string `json:"id"`
	// Version is the published version of the template.
	Version string `json:"version"`
	// Title is the human readable name.
	Title string `json:"title"`
	// Description explains what the template does.
	Description string `json:"description"`
	// Runtime is the runtime family, "container" or "vm".
	Runtime string `json:"runtime"`
	// Backend pins the runtime backend. It is empty when the template uses
	// the default of the node.
	Backend string `json:"backend,omitempty"`
	// Image is the image the workload runs.
	Image string `json:"image"`
	// ImageDigest pins the image to an exact digest.
	ImageDigest string `json:"image_digest,omitempty"`
	// Resources is the resource envelope of the workload.
	Resources ResourcesView `json:"resources"`
	// Network is the egress policy of the workload.
	Network NetworkView `json:"network"`
	// TTLSeconds is the default lifetime of an instance.
	TTLSeconds int `json:"ttl_seconds"`
	// Labels are attached to every instance of the template.
	Labels map[string]string `json:"labels,omitempty"`
	// Capabilities are the optional capabilities the template may request.
	Capabilities []string `json:"capabilities,omitempty"`
	// Secrets lists the names of the secrets the workload may request. The
	// values are never part of the template.
	Secrets []string `json:"secrets,omitempty"`
	// Documentation is the prose that describes the template.
	Documentation string `json:"documentation,omitempty"`
	// Digest is the fingerprint of the specification.
	Digest string `json:"digest"`
}

// viewTemplate renders a template for a client.
func viewTemplate(tmpl templates.Template) TemplateView {
	return TemplateView{
		ID:            tmpl.Spec.ID,
		Version:       tmpl.Spec.Version,
		Title:         tmpl.Spec.Title,
		Description:   tmpl.Spec.Description,
		Runtime:       string(tmpl.Spec.Runtime),
		Backend:       tmpl.Spec.Backend,
		Image:         tmpl.Spec.Image,
		ImageDigest:   tmpl.Spec.ImageDigest,
		Resources:     viewResources(tmpl.Spec.Resources.CPU, tmpl.Spec.Resources.MemoryMB, tmpl.Spec.Resources.DiskMB, tmpl.Spec.Resources.GPUCount, tmpl.Spec.Resources.GPUVendor),
		Network:       viewNetwork(tmpl.Spec.Network.Egress, tmpl.Spec.Network.Allowlist, tmpl.Spec.Network.AllowedDNS),
		TTLSeconds:    tmpl.Spec.TTLSeconds,
		Labels:        tmpl.Spec.Labels,
		Capabilities:  tmpl.Spec.Capabilities,
		Secrets:       tmpl.Spec.Secrets,
		Documentation: tmpl.Spec.Documentation,
		Digest:        tmpl.Digest,
	}
}

// viewResources renders a resource envelope.
func viewResources(cpu float64, memoryMB, diskMB, gpuCount int, gpuVendor string) ResourcesView {
	return ResourcesView{
		CPU:       cpu,
		MemoryMB:  memoryMB,
		DiskMB:    diskMB,
		GPUCount:  gpuCount,
		GPUVendor: gpuVendor,
	}
}

// viewNetwork renders an egress policy.
func viewNetwork(egress string, allowlist, allowedDNS []string) NetworkView {
	return NetworkView{Egress: egress, Allowlist: allowlist, AllowedDNS: allowedDNS}
}

// InstanceView is the public shape of an instance.
//
// The workspace of the host is never part of the view.
type InstanceView struct {
	// ID identifies the instance.
	ID string `json:"id"`
	// JobID is the job that created the instance.
	JobID string `json:"job_id,omitempty"`
	// TemplateID is the template the instance was created from.
	TemplateID string `json:"template_id"`
	// Runtime is the backend that hosts the instance.
	Runtime string `json:"runtime"`
	// Name is the human readable name of the instance.
	Name string `json:"name,omitempty"`
	// State is the lifecycle state of the instance.
	State string `json:"state"`
	// Resources is the effective resource envelope.
	Resources map[string]any `json:"resources,omitempty"`
	// ExpiresAt is the moment the instance is reaped.
	ExpiresAt time.Time `json:"expires_at"`
	// CreatedAt is the moment the instance was created.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the moment the instance last changed.
	UpdatedAt time.Time `json:"updated_at"`
	// Links point at the agent-readable resources of the instance.
	Links map[string]string `json:"links"`
}

// viewInstance renders an instance for a client.
func viewInstance(instance store.Instance) InstanceView {
	return InstanceView{
		ID:         instance.ID,
		JobID:      instance.JobID,
		TemplateID: instance.TemplateID,
		Runtime:    instance.Runtime,
		Name:       instance.Name,
		State:      string(instance.State),
		Resources:  instance.Resources,
		ExpiresAt:  instance.ExpiresAt,
		CreatedAt:  instance.CreatedAt,
		UpdatedAt:  instance.UpdatedAt,
		Links:      instanceLinks(instance.ID),
	}
}

// instanceLinks builds the links of an instance.

// JobView is the public shape of a job.
type JobView struct {
	// ID identifies the job.
	ID string `json:"id"`
	// Kind is the operation the job performs.
	Kind string `json:"kind"`
	// State is the lifecycle state of the job.
	State string `json:"state"`
	// TemplateID is the template the job used.
	TemplateID string `json:"template_id,omitempty"`
	// Runtime is the backend the job used.
	Runtime string `json:"runtime,omitempty"`
	// InstanceID is the instance the job produced.
	InstanceID string `json:"instance_id,omitempty"`
	// IdempotencyKey is the key that makes the job replayable.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// ErrorCode is the stable code of the failure, when the job failed.
	ErrorCode string `json:"error_code,omitempty"`
	// ErrorMessage explains the failure in English.
	ErrorMessage string `json:"error_message,omitempty"`
	// Request is the request the job was created from.
	Request map[string]any `json:"request,omitempty"`
	// Recipe is the resolved, reproducible plan of the job.
	Recipe map[string]any `json:"recipe,omitempty"`
	// CreatedAt is the moment the job was created.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the moment the job last changed.
	UpdatedAt time.Time `json:"updated_at"`
	// StartedAt is the moment the job started, when it did.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// FinishedAt is the moment the job finished, when it did.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Links point at the agent-readable resources of the job.
	Links map[string]string `json:"links"`
}

// viewJob renders a job for a client.
func viewJob(job store.Job) JobView {
	return JobView{
		ID:             job.ID,
		Kind:           string(job.Kind),
		State:          string(job.State),
		TemplateID:     job.TemplateID,
		Runtime:        job.Runtime,
		InstanceID:     job.InstanceID,
		IdempotencyKey: job.IdempotencyKey,
		ErrorCode:      job.ErrorCode,
		ErrorMessage:   job.ErrorMessage,
		Request:        job.Request,
		Recipe:         job.Recipe,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
		StartedAt:      job.StartedAt,
		FinishedAt:     job.FinishedAt,
		Links:          jobLinks(job),
	}
}

// jobLinks builds the links of a job.
func jobLinks(job store.Job) map[string]string {
	links := map[string]string{"self": "double-agent://jobs/" + job.ID}
	if job.InstanceID != "" {
		links["instance"] = "double-agent://instances/" + job.InstanceID
	}
	return links
}

// ArtifactView is the public shape of an artifact.
//
// The path of the file on the host is never part of the view.
type ArtifactView struct {
	// ID identifies the artifact.
	ID string `json:"id"`
	// Name is the display name of the file.
	Name string `json:"name"`
	// MIME is the detected content type.
	MIME string `json:"mime"`
	// Size is the length of the file, in bytes.
	Size int64 `json:"size"`
	// Digest is the SHA-256 of the content, hex encoded.
	Digest string `json:"digest"`
	// State is the lifecycle state of the artifact.
	State string `json:"state"`
	// JobID is the job that produced the artifact.
	JobID string `json:"job_id,omitempty"`
	// InstanceID is the instance that produced the artifact.
	InstanceID string `json:"instance_id,omitempty"`
	// DownloadURL is the authenticated endpoint that serves the content.
	DownloadURL string `json:"download_url"`
	// CreatedAt is the moment the artifact was stored.
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is the retention deadline of the artifact.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Links point at the agent-readable resources of the artifact.
	Links map[string]string `json:"links"`
}

// viewArtifact renders an artifact for a client.
func viewArtifact(record store.Artifact) ArtifactView {
	return ArtifactView{
		ID:          record.ID,
		Name:        record.Name,
		MIME:        record.MIME,
		Size:        record.Size,
		Digest:      record.Digest,
		State:       string(record.State),
		JobID:       record.JobID,
		InstanceID:  record.InstanceID,
		DownloadURL: "/api/v1/artifacts/" + record.ID + "/download",
		CreatedAt:   record.CreatedAt,
		ExpiresAt:   record.ExpiresAt,
		Links: map[string]string{
			"self":     "double-agent://artifacts/" + record.ID + "/metadata",
			"download": "/api/v1/artifacts/" + record.ID + "/download",
		},
	}
}

func instanceLinks(instanceID string) map[string]string {
	return map[string]string{
		"self": "double-agent://instances/" + instanceID,
	}
}
