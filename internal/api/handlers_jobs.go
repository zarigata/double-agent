package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zarigata/double-agent/internal/store"
)

// jobView is the public shape of a job.
type jobView struct {
	ID             string         `json:"id"`
	Kind           string         `json:"kind"`
	State          string         `json:"state"`
	TemplateID     string         `json:"template_id,omitempty"`
	Runtime        string         `json:"runtime,omitempty"`
	InstanceID     string         `json:"instance_id,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	Request        map[string]any `json:"request,omitempty"`
	Recipe         map[string]any `json:"recipe,omitempty"`
	OwnerID        string         `json:"owner_id"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
}

// handleJobList answers with the jobs of the caller.
func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	states, err := parseJobStates(r.URL.Query().Get("state"))
	if err != nil {
		writeError(w, r, CodeInvalidRequest, "the state filter is not known", nil)
		return
	}
	jobs, err := s.store.ListJobs(r.Context(), store.JobFilter{UserID: scopedUserID(actorFrom(r)), States: states, Limit: limitQuery(r)})
	if err != nil {
		writeError(w, r, CodeInternal, "the jobs could not be loaded", nil)
		return
	}
	views := make([]jobView, 0, len(jobs))
	for _, job := range jobs {
		views = append(views, viewJob(job))
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"jobs": views})
}

// handleJobGet answers with one job.
func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) {
	job, ok := s.loadJob(w, r)
	if !ok {
		return
	}
	writeJSON(w, r, http.StatusOK, viewJob(job))
}

// handleJobCancel cancels a job that has not finished yet.
func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	if err := s.orch.CancelJob(r.Context(), actorFrom(r), id, requestID(r)); err != nil {
		classify(err).write(w, r)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"job_id": id,
		"state":  string(store.JobCanceled),
	})
}

// loadJob loads a job and checks that the caller owns it.
func (s *Server) loadJob(w http.ResponseWriter, r *http.Request) (store.Job, bool) {
	id := pathValue(r, "id")
	job, err := s.store.JobByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, CodeNotFound, "the job does not exist",
				map[string]any{"job_id": id})
			return store.Job{}, false
		}
		writeError(w, r, CodeInternal, "the job could not be loaded", nil)
		return store.Job{}, false
	}
	if !ownsResource(actorFrom(r), job.UserID) {
		writeError(w, r, CodePermissionDenied, "the job belongs to another user",
			map[string]any{"job_id": id})
		return store.Job{}, false
	}
	return job, true
}

// viewJob renders a job for a client.
func viewJob(job store.Job) jobView {
	return jobView{
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
		OwnerID:        job.UserID,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
		StartedAt:      job.StartedAt,
		FinishedAt:     job.FinishedAt,
	}
}

// parseJobStates reads a comma separated list of job states.
func parseJobStates(raw string) ([]store.JobState, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	states := make([]store.JobState, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		state := store.JobState(part)
		if !state.Valid() {
			return nil, errors.New("api: the job state is not known")
		}
		states = append(states, state)
	}
	return states, nil
}
