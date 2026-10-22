package store

import "time"

// Role is the authorization role of a user.
type Role string

const (
	// RoleAdmin may perform every operation, including user administration.
	RoleAdmin Role = "admin"
	// RoleUser may use the resources granted by its scopes.
	RoleUser Role = "user"
)

// User is a local account.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// APIKey is a revocable client credential. The secret material is never
// stored: only the SHA-256 hash of the key is kept.
type APIKey struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	KeyHash    string     `json:"-"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Revoked reports whether the key can no longer be used.
func (k APIKey) Revoked() bool { return k.RevokedAt != nil }

// Expired reports whether the key is past its expiry.
func (k APIKey) Expired(now time.Time) bool {
	return k.ExpiresAt != nil && !now.Before(*k.ExpiresAt)
}

// Active reports whether the key may still authenticate a request.
func (k APIKey) Active(now time.Time) bool {
	return !k.Revoked() && !k.Expired(now)
}

// Session is a browser session.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	TokenHash string    `json:"-"`
	CSRFToken string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	LastSeen  time.Time `json:"last_seen_at"`
}

// Expired reports whether the session may no longer be used.
func (s Session) Expired(now time.Time) bool { return !now.Before(s.ExpiresAt) }

// JobState enumerates the job lifecycle states.
type JobState string

const (
	JobQueued    JobState = "queued"
	JobPreparing JobState = "preparing"
	JobRunning   JobState = "running"
	JobExporting JobState = "exporting"
	JobCompleted JobState = "completed"
	JobFailed    JobState = "failed"
	JobCanceled  JobState = "canceled"
	JobExpired   JobState = "expired"
)

// Valid reports whether the state is a known job state.
func (s JobState) Valid() bool {
	switch s {
	case JobQueued, JobPreparing, JobRunning, JobExporting,
		JobCompleted, JobFailed, JobCanceled, JobExpired:
		return true
	}
	return false
}

// Terminal reports whether the job has reached a final state.
func (s JobState) Terminal() bool {
	switch s {
	case JobCompleted, JobFailed, JobCanceled, JobExpired:
		return true
	}
	return false
}

// JobKind names the operation a job performs.
type JobKind string

const (
	JobKindInstanceCreate  JobKind = "instance.create"
	JobKindInstanceExec    JobKind = "instance.exec"
	JobKindInstanceStop    JobKind = "instance.stop"
	JobKindInstanceDestroy JobKind = "instance.destroy"
	JobKindArtifactExport  JobKind = "artifact.export"
	JobKindTemplateRun     JobKind = "template.run"
)

// Job is an observable asynchronous operation.
type Job struct {
	ID             string
	UserID         string
	SessionID      string
	Kind           JobKind
	State          JobState
	TemplateID     string
	Runtime        string
	InstanceID     string
	Request        map[string]any
	Recipe         map[string]any
	ErrorCode      string
	ErrorMessage   string
	IdempotencyKey string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// InstanceState enumerates the instance lifecycle states.
type InstanceState string

const (
	InstanceRequested InstanceState = "requested"
	InstancePreparing InstanceState = "preparing"
	InstanceStarting  InstanceState = "starting"
	InstanceReady     InstanceState = "ready"
	InstanceStopping  InstanceState = "stopping"
	InstanceDestroyed InstanceState = "destroyed"
	InstanceFailed    InstanceState = "failed"
)

// Valid reports whether the state is a known instance state.
func (s InstanceState) Valid() bool {
	switch s {
	case InstanceRequested, InstancePreparing, InstanceStarting, InstanceReady,
		InstanceStopping, InstanceDestroyed, InstanceFailed:
		return true
	}
	return false
}

// Terminal reports whether the instance has reached a final state.
func (s InstanceState) Terminal() bool {
	return s == InstanceDestroyed || s == InstanceFailed
}

// Instance is a concrete disposable container or virtual machine.
type Instance struct {
	ID         string
	JobID      string
	UserID     string
	TemplateID string
	Runtime    string
	RuntimeID  string
	Name       string
	State      InstanceState
	Resources  map[string]any
	Workspace  string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ArtifactState enumerates the artifact lifecycle states.
type ArtifactState string

const (
	ArtifactAvailable ArtifactState = "available"
	ArtifactExpired   ArtifactState = "expired"
	ArtifactDeleted   ArtifactState = "deleted"
)

// Artifact is a controlled exported file.
type Artifact struct {
	ID         string
	UserID     string
	JobID      string
	InstanceID string
	Name       string
	Path       string
	MIME       string
	Size       int64
	Digest     string
	State      ArtifactState
	CreatedAt  time.Time
	ExpiresAt  *time.Time
}

// AuditEvent is a security and operational record.
type AuditEvent struct {
	ID            string
	Timestamp     time.Time
	ActorType     string
	ActorID       string
	Action        string
	TargetType    string
	TargetID      string
	Decision      string
	CorrelationID string
	PolicyVersion string
	Result        string
	Metadata      map[string]any
}

// Audit decisions.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Actor types used in audit records.
const (
	ActorUser   = "user"
	ActorAPIKey = "api_key"
	ActorSystem = "system"
)
