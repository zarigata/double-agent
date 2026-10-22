package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/store"
)

// createInstanceRequest is the body of the instance creation endpoint.
//
// The template is accepted under two names: "template" is the name used by
// the MCP tool, "template_id" is the older name of the REST API. Both are
// understood so that a client written for either surface keeps working.
type createInstanceRequest struct {
	Template        string            `json:"template"`
	TemplateID      string            `json:"template_id"`
	TemplateVersion string            `json:"template_version"`
	Name            string            `json:"name"`
	Env             map[string]string `json:"env"`
	Environment     map[string]string `json:"environment"`
	TTLSeconds      int64             `json:"ttl_seconds"`
	SecretRefs      []string          `json:"secret_refs"`
	IdempotencyKey  string            `json:"idempotency_key"`
	// Resources is accepted for compatibility with the MCP tool. The
	// resource envelope of an instance is derived from its template, so the
	// field is recorded but never applied.
	Resources map[string]any `json:"resources"`
}

// createInstanceResponse is the answer of the instance creation endpoint.
//
// The shape follows docs/MCP_API_AND_DISCOVERY.md: the client receives a job
// to watch and a link that points at it.
type createInstanceResponse struct {
	JobID      string            `json:"job_id"`
	InstanceID string            `json:"instance_id"`
	State      string            `json:"state"`
	Links      map[string]string `json:"links"`
}

// instanceView is the public shape of an instance. The workspace path of the
// host is never part of it.
type instanceView struct {
	ID         string         `json:"id"`
	JobID      string         `json:"job_id"`
	TemplateID string         `json:"template_id"`
	Runtime    string         `json:"runtime"`
	Name       string         `json:"name"`
	State      string         `json:"state"`
	Resources  map[string]any `json:"resources,omitempty"`
	OwnerID    string         `json:"owner_id"`
	ExpiresAt  time.Time      `json:"expires_at"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// handleInstanceCreate starts the creation of an instance.
//
// The work happens in the background, so the answer carries the identifier of
// the job that tracks it.
func (s *Server) handleInstanceCreate(w http.ResponseWriter, r *http.Request) {
	var request createInstanceRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	template := strings.TrimSpace(request.Template)
	if template == "" {
		template = strings.TrimSpace(request.TemplateID)
	}
	if template == "" {
		writeError(w, r, CodeInvalidRequest, "the template is required", nil)
		return
	}
	env := map[string]string{}
	for key, value := range request.Environment {
		env[key] = value
	}
	for key, value := range request.Env {
		env[key] = value
	}
	if len(request.Resources) > 0 {
		s.log.Warn("the resource envelope of a request is not applied",
			"request_id", requestID(r), "template", template)
	}
	result, err := s.orch.Create(r.Context(), orchestrator.CreateRequest{
		Actor:           actorFrom(r),
		TemplateID:      template,
		TemplateVersion: strings.TrimSpace(request.TemplateVersion),
		Name:            strings.TrimSpace(request.Name),
		Env:             env,
		TTL:             time.Duration(request.TTLSeconds) * time.Second,
		IdempotencyKey:  strings.TrimSpace(request.IdempotencyKey),
		SecretRefs:      request.SecretRefs,
		CorrelationID:   requestID(r),
	})
	if err != nil {
		classify(err).write(w, r)
		return
	}
	state := string(result.Job.State)
	if state == string(store.JobQueued) {
		// The workflow prepares the instance right away, so a queued job is
		// already preparing.
		state = string(store.JobPreparing)
	}
	writeJSON(w, r, http.StatusAccepted, createInstanceResponse{
		JobID:      result.Job.ID,
		InstanceID: result.Job.InstanceID,
		State:      state,
		Links: map[string]string{
			"status":   "double-agent://jobs/" + result.Job.ID,
			"instance": "double-agent://instances/" + result.Job.InstanceID,
		},
	})
}

// handleInstanceList answers with the instances of the caller.
func (s *Server) handleInstanceList(w http.ResponseWriter, r *http.Request) {
	states, err := parseStates(r.URL.Query().Get("state"))
	if err != nil {
		writeError(w, r, CodeInvalidRequest, "the state filter is not known", nil)
		return
	}
	instances, err := s.store.ListInstances(r.Context(), scopedUserID(actorFrom(r)), states)
	if err != nil {
		writeError(w, r, CodeInternal, "the instances could not be loaded", nil)
		return
	}
	views := make([]instanceView, 0, len(instances))
	for _, instance := range instances {
		views = append(views, viewInstance(instance))
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"instances": views})
}

// handleInstanceGet answers with one instance.
func (s *Server) handleInstanceGet(w http.ResponseWriter, r *http.Request) {
	instance, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	writeJSON(w, r, http.StatusOK, viewInstance(instance))
}

// execRequest is the body of the exec endpoint.
type execRequest struct {
	Argv []string          `json:"argv"`
	Cwd  string            `json:"cwd"`
	Env  map[string]string `json:"env"`
	// Environment is the name used by the MCP tool. "env" wins when both
	// are present.
	Environment    map[string]string `json:"environment"`
	TimeoutSeconds int64             `json:"timeout_seconds"`
}

// handleInstanceExec runs a command inside an instance.
//
// The command is an argument vector: it is never handed to a shell, so a
// workload cannot smuggle a second command into the host.
func (s *Server) handleInstanceExec(w http.ResponseWriter, r *http.Request) {
	var request execRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	if len(request.Argv) == 0 {
		writeError(w, r, CodeInvalidRequest, "the command is required", nil)
		return
	}
	env := map[string]string{}
	for key, value := range request.Environment {
		env[key] = value
	}
	for key, value := range request.Env {
		env[key] = value
	}
	timeout := time.Duration(request.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	if timeout > maxExecTimeout {
		timeout = maxExecTimeout
	}
	response, err := s.orch.Exec(r.Context(), orchestrator.ExecRequest{
		Actor:         actorFrom(r),
		InstanceID:    pathValue(r, "id"),
		Argv:          request.Argv,
		Cwd:           request.Cwd,
		Env:           env,
		Timeout:       timeout,
		CorrelationID: requestID(r),
	})
	if err != nil {
		classify(err).write(w, r)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"exit_code":   response.ExitCode,
		"stdout":      string(response.Stdout),
		"stderr":      string(response.Stderr),
		"duration_ms": response.Duration.Milliseconds(),
		"truncated":   response.Truncated,
	})
}

// stopRequest is the body of the stop endpoint.
type stopRequest struct {
	GraceSeconds int64 `json:"grace_seconds"`
}

// handleInstanceStop stops an instance.
func (s *Server) handleInstanceStop(w http.ResponseWriter, r *http.Request) {
	var request stopRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	grace := time.Duration(request.GraceSeconds) * time.Second
	if grace <= 0 {
		grace = defaultStopGrace
	}
	id := pathValue(r, "id")
	if err := s.orch.Stop(r.Context(), actorFrom(r), id, grace, requestID(r)); err != nil {
		classify(err).write(w, r)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"instance_id": id,
		"state":       string(store.InstanceDestroyed),
	})
}

// extendTTLRequest is the body of the extend-ttl endpoint.
type extendTTLRequest struct {
	TTLSeconds int64 `json:"ttl_seconds"`
}

// handleInstanceExtendTTL pushes the expiry of an instance further away.
func (s *Server) handleInstanceExtendTTL(w http.ResponseWriter, r *http.Request) {
	var request extendTTLRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	ttl := time.Duration(request.TTLSeconds) * time.Second
	id := pathValue(r, "id")
	expires, err := s.orch.ExtendTTL(r.Context(), actorFrom(r), id, ttl, requestID(r))
	if err != nil {
		classify(err).write(w, r)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"instance_id": id,
		"expires_at":  expires,
	})
}

// handleInstanceDestroy stops an instance and releases its resources.
func (s *Server) handleInstanceDestroy(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if err := s.orch.Destroy(r.Context(), actorFrom(r), id, requestID(r)); err != nil {
		classify(err).write(w, r)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"instance_id": id,
		"state":       string(store.InstanceDestroyed),
	})
}

// handleInstanceConsole answers with a short lived console endpoint.
func (s *Server) handleInstanceConsole(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	endpoint, err := s.orch.Console(r.Context(), actorFrom(r), id, requestID(r))
	if err != nil {
		classify(err).write(w, r)
		return
	}
	expires := s.now().Add(consoleTTL)
	writeJSON(w, r, http.StatusOK, map[string]any{
		"instance_id": id,
		"url":         endpoint.URL,
		"kind":        endpoint.Kind,
		"expires_at":  expires,
	})
}

// loadInstance loads an instance and checks that the caller owns it.
func (s *Server) loadInstance(w http.ResponseWriter, r *http.Request) (store.Instance, bool) {
	id := pathValue(r, "id")
	instance, err := s.store.InstanceByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, CodeNotFound, "the instance does not exist",
				map[string]any{"instance_id": id})
			return store.Instance{}, false
		}
		s.log.Error("failed to load an instance", "instance", id, "error", err)
		writeError(w, r, CodeInternal, "the instance could not be loaded", nil)
		return store.Instance{}, false
	}
	if !ownsResource(actorFrom(r), instance.UserID) {
		writeError(w, r, CodePermissionDenied, "the instance belongs to another user",
			map[string]any{"instance_id": id})
		return store.Instance{}, false
	}
	return instance, true
}

// viewInstance renders an instance for a client.
func viewInstance(instance store.Instance) instanceView {
	return instanceView{
		ID:         instance.ID,
		JobID:      instance.JobID,
		TemplateID: instance.TemplateID,
		Runtime:    instance.Runtime,
		Name:       instance.Name,
		State:      string(instance.State),
		Resources:  instance.Resources,
		OwnerID:    instance.UserID,
		ExpiresAt:  instance.ExpiresAt,
		CreatedAt:  instance.CreatedAt,
		UpdatedAt:  instance.UpdatedAt,
	}
}

// parseStates reads a comma separated list of instance states.
func parseStates(raw string) ([]store.InstanceState, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	states := make([]store.InstanceState, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		state := store.InstanceState(part)
		if !state.Valid() {
			return nil, errors.New("api: the instance state is not known")
		}
		states = append(states, state)
	}
	return states, nil
}
