package mcpsrv

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/store"
)

// JobGetInput selects a job.
type JobGetInput struct {
	// JobID identifies the job.
	JobID string `json:"job_id" jsonschema:"The identifier of the job, for example \"job_01J...\"."`
}

// JobGetOutput carries one job.
type JobGetOutput struct {
	// Job is the requested job.
	Job JobView `json:"job"`
}

// JobListInput narrows a job listing.
type JobListInput struct {
	// States filters the jobs by state, for example "failed" or "running".
	States []string `json:"states,omitempty" jsonschema:"The states the answer may contain, for example [\"running\", \"failed\"]."`
	// Limit caps the number of jobs. The node returns at most a hundred.
	Limit int `json:"limit,omitempty" jsonschema:"The greatest number of jobs to return. Leave it empty for the default of fifty."`
}

// JobListOutput carries the jobs of the account.
type JobListOutput struct {
	// Jobs are the jobs, newest first.
	Jobs []JobView `json:"jobs"`
}

// JobCancelInput selects the job to cancel.
type JobCancelInput struct {
	// JobID identifies the job.
	JobID string `json:"job_id" jsonschema:"The identifier of the job, for example \"job_01J...\"."`
}

// JobCancelOutput is the answer of a cancellation.
type JobCancelOutput struct {
	// JobID is the job that was canceled.
	JobID string `json:"job_id"`
	// State is the state the job entered.
	State string `json:"state"`
}

// JobLogsInput selects the log of a job.
type JobLogsInput struct {
	// JobID identifies the job.
	JobID string `json:"job_id" jsonschema:"The identifier of the job, for example \"job_01J...\"."`
	// Tail caps the number of entries. The node returns at most two hundred.
	Tail int `json:"tail,omitempty" jsonschema:"The greatest number of entries to return. Leave it empty for the default of fifty."`
}

// JobLogEntry is one entry of the log of a job.
type JobLogEntry struct {
	// Timestamp is the moment the entry was written.
	Timestamp time.Time `json:"timestamp"`
	// Level is the severity of the entry.
	Level string `json:"level"`
	// Message describes the entry in English.
	Message string `json:"message"`
}

// JobLogsOutput carries the log of a job.
type JobLogsOutput struct {
	// JobID is the job the log belongs to.
	JobID string `json:"job_id"`
	// State is the state of the job.
	State string `json:"state"`
	// Entries are the entries of the log, oldest first.
	Entries []JobLogEntry `json:"entries"`
}

// addJobTools registers the tools of the jobs.
func (s *Server) addJobTools(server *mcp.Server, actor policy.Actor) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "job.get",
		Description: "Read one job: its state, the instance it produced, the error that " +
			"stopped it and the recipe that reproduces it. The recipe never carries a " +
			"secret: a secret is an opaque reference.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input JobGetInput) (*mcp.CallToolResult, JobGetOutput, error) {
		if failure := requireScope(actor, policy.ScopeJobsRead, correlationIDFrom(ctx)); failure != nil {
			return nil, JobGetOutput{}, failure
		}
		out, err := s.getJob(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "job.list",
		Description: "List the jobs of the account, newest first. Filter them by state " +
			"to follow the work that is still running.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input JobListInput) (*mcp.CallToolResult, JobListOutput, error) {
		if failure := requireScope(actor, policy.ScopeJobsRead, correlationIDFrom(ctx)); failure != nil {
			return nil, JobListOutput{}, failure
		}
		out, err := s.listJobs(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "job.cancel",
		Description: "Cancel a job that has not reached a final state. The workload of the " +
			"job is stopped and the job is recorded as canceled.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input JobCancelInput) (*mcp.CallToolResult, JobCancelOutput, error) {
		if failure := requireScope(actor, policy.ScopeJobsCancel, correlationIDFrom(ctx)); failure != nil {
			return nil, JobCancelOutput{}, failure
		}
		out, err := s.cancelJob(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "job.logs",
		Description: "Read the log of a job. The control plane records the lifecycle of a " +
			"job rather than the raw output of the workload: the entries describe the " +
			"state changes of the job and the error that stopped it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input JobLogsInput) (*mcp.CallToolResult, JobLogsOutput, error) {
		if failure := requireScope(actor, policy.ScopeJobsRead, correlationIDFrom(ctx)); failure != nil {
			return nil, JobLogsOutput{}, failure
		}
		out, err := s.jobLogs(ctx, actor, input)
		return nil, out, err
	})
}

// getJob reads one job.
func (s *Server) getJob(ctx context.Context, actor policy.Actor, input JobGetInput) (JobGetOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.JobID)
	if id == "" {
		return JobGetOutput{}, invalidRequest("the job identifier is required", correlationID)
	}
	job, err := s.store.JobByID(ctx, id)
	if err != nil {
		return JobGetOutput{}, classify(err, correlationID)
	}
	if !ownsResource(actor, job.UserID) {
		return JobGetOutput{}, permissionDenied(
			"the job belongs to another account",
			correlationID,
			map[string]any{"job_id": id},
		)
	}
	return JobGetOutput{Job: viewJob(job)}, nil
}

// listJobs lists the jobs of the account.
func (s *Server) listJobs(ctx context.Context, actor policy.Actor, input JobListInput) (JobListOutput, error) {
	correlationID := correlationIDFrom(ctx)
	states := make([]store.JobState, 0, len(input.States))
	for _, raw := range input.States {
		state := store.JobState(strings.TrimSpace(raw))
		if !state.Valid() {
			return JobListOutput{}, &ToolError{
				Code:          CodeInvalidRequest,
				Message:       "the state filter is not known",
				CorrelationID: correlationID,
			}
		}
		states = append(states, state)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	jobs, err := s.store.ListJobs(ctx, store.JobFilter{
		UserID: actor.ID,
		States: states,
		Limit:  limit,
	})
	if err != nil {
		return JobListOutput{}, classify(err, correlationID)
	}
	views := make([]JobView, 0, len(jobs))
	for _, job := range jobs {
		if !ownsResource(actor, job.UserID) {
			continue
		}
		views = append(views, viewJob(job))
	}
	return JobListOutput{Jobs: views}, nil
}

// cancelJob cancels a job that has not finished.
func (s *Server) cancelJob(ctx context.Context, actor policy.Actor, input JobCancelInput) (JobCancelOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.JobID)
	if id == "" {
		return JobCancelOutput{}, invalidRequest("the job identifier is required", correlationID)
	}
	if err := s.orch.CancelJob(ctx, actor, id, correlationID); err != nil {
		return JobCancelOutput{}, classify(err, correlationID)
	}
	return JobCancelOutput{JobID: id, State: string(store.JobCanceled)}, nil
}

// jobLogs renders the lifecycle log of a job.
//
// The control plane stores the metadata of a job, not the output of the
// workload, so the log describes the life of the job: the moments it was
// created, started and finished, and the error that stopped it.
func (s *Server) jobLogs(ctx context.Context, actor policy.Actor, input JobLogsInput) (JobLogsOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.JobID)
	if id == "" {
		return JobLogsOutput{}, invalidRequest("the job identifier is required", correlationID)
	}
	job, err := s.store.JobByID(ctx, id)
	if err != nil {
		return JobLogsOutput{}, classify(err, correlationID)
	}
	if !ownsResource(actor, job.UserID) {
		return JobLogsOutput{}, permissionDenied(
			"the job belongs to another account",
			correlationID,
			map[string]any{"job_id": id},
		)
	}
	limit := input.Tail
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	entries := make([]JobLogEntry, 0, 8)
	entries = append(entries, JobLogEntry{
		Timestamp: job.CreatedAt,
		Level:     "info",
		Message:   "the job was created with the kind " + string(job.Kind),
	})
	if job.StartedAt != nil {
		entries = append(entries, JobLogEntry{
			Timestamp: *job.StartedAt,
			Level:     "info",
			Message:   "the job started",
		})
	}
	entries = append(entries, JobLogEntry{
		Timestamp: job.UpdatedAt,
		Level:     "info",
		Message:   "the job is in the state " + string(job.State),
	})
	if job.ErrorCode != "" {
		entries = append(entries, JobLogEntry{
			Timestamp: job.UpdatedAt,
			Level:     "error",
			Message:   job.ErrorCode + ": " + job.ErrorMessage,
		})
	}
	if job.FinishedAt != nil {
		entries = append(entries, JobLogEntry{
			Timestamp: *job.FinishedAt,
			Level:     "info",
			Message:   "the job finished with the state " + string(job.State),
		})
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return JobLogsOutput{JobID: id, State: string(job.State), Entries: entries}, nil
}
