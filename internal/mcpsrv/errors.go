package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
)

// Code is a stable, machine-readable error code.
//
// The vocabulary is the one documented in docs/MCP_API_AND_DISCOVERY.md, so
// that a REST client and an MCP client see exactly the same codes. The codes
// are part of the contract: they never change meaning, and a new failure mode
// gets a new code instead of an overloaded one.
type Code string

// The error codes of the control plane.
const (
	// CodeAuthenticationFailed means the credential is missing, invalid or no
	// longer usable.
	CodeAuthenticationFailed Code = "authentication_failed"
	// CodePermissionDenied means the actor is known but not allowed to
	// perform the request.
	CodePermissionDenied Code = "permission_denied"
	// CodeQuotaExceeded means a node or account limit was reached.
	CodeQuotaExceeded Code = "quota_exceeded"
	// CodeCapabilityUnavailable means the node cannot serve the request, for
	// example because the runtime backend is down.
	CodeCapabilityUnavailable Code = "capability_unavailable"
	// CodeRuntimeError means the execution backend failed.
	CodeRuntimeError Code = "runtime_error"
	// CodeJobTimeout means the operation did not finish in time.
	CodeJobTimeout Code = "job_timeout"
	// CodeArtifactExpired means the artifact is past its retention deadline.
	CodeArtifactExpired Code = "artifact_expired"
	// CodeHumanActionRequired means an operator has to act before the
	// request can continue.
	CodeHumanActionRequired Code = "human_action_required"
	// CodeNotFound means the addressed resource does not exist.
	CodeNotFound Code = "not_found"
	// CodeInvalidRequest means the request itself is malformed.
	CodeInvalidRequest Code = "invalid_request"
	// CodeDigestMismatch means the content of an upload does not match the
	// digest the client announced.
	CodeDigestMismatch Code = "digest_mismatch"
	// CodeConflict means the request collides with the current state of the
	// resource.
	CodeConflict Code = "conflict"
	// CodeInternal means the node failed for an unexpected reason.
	CodeInternal Code = "internal_error"
)

// ToolError is the failure of a tool call.
//
// The error carries the stable code of the document, an English message, the
// correlation identifier of the call and a retry hint. The SDK turns it into a
// tool error rather than a protocol error, so the client sees the message and
// the model is able to correct itself.
//
// The wrapped cause never reaches the client: it may name a host path or a
// socket. It stays in the log of the node.
type ToolError struct {
	// Code is the machine-readable code of the failure.
	Code Code
	// Message is the human-readable explanation, written in plain English.
	Message string
	// CorrelationID ties the failure to the audit records of the node.
	CorrelationID string
	// Retryable reports whether repeating the call may succeed.
	Retryable bool
	// Details carries safe, structured context about the failure.
	Details map[string]any
	// Err is the wrapped cause, kept for the logs.
	Err error
}

// Error implements the error interface.
//
// The payload is JSON so that a client reads the code without parsing English
// prose, while the message itself stays readable for a human operator.
func (e *ToolError) Error() string {
	payload := struct {
		Code          Code           `json:"code"`
		Message       string         `json:"message"`
		CorrelationID string         `json:"correlation_id,omitempty"`
		Retryable     bool           `json:"retryable"`
		Details       map[string]any `json:"details,omitempty"`
	}{
		Code:          e.Code,
		Message:       e.Message,
		CorrelationID: e.CorrelationID,
		Retryable:     e.Retryable,
		Details:       e.Details,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return string(encoded)
}

// Unwrap exposes the wrapped cause to errors.Is and errors.As.
func (e *ToolError) Unwrap() error { return e.Err }

// invalidRequest reports a malformed request.
func invalidRequest(message, correlationID string) *ToolError {
	return &ToolError{Code: CodeInvalidRequest, Message: message, CorrelationID: correlationID}
}

// permissionDenied reports a request the actor is not allowed to perform.
func permissionDenied(message, correlationID string, details map[string]any) *ToolError {
	return &ToolError{
		Code:          CodePermissionDenied,
		Message:       message,
		CorrelationID: correlationID,
		Details:       details,
	}
}

// classify maps an error of the control plane onto the stable code vocabulary.
//
// The mapping is deliberately coarse: the client learns what kind of failure
// happened, never where on the host it happened.
func classify(err error, correlationID string) *ToolError {
	if err == nil {
		return nil
	}
	var toolErr *ToolError
	if errors.As(err, &toolErr) {
		if toolErr.CorrelationID == "" {
			toolErr.CorrelationID = correlationID
		}
		return toolErr
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &ToolError{Code: CodeNotFound, Message: "the resource does not exist", CorrelationID: correlationID, Err: err}
	case errors.Is(err, store.ErrConflict):
		return &ToolError{Code: CodeConflict, Message: "the request conflicts with an existing resource", CorrelationID: correlationID, Err: err}
	case errors.Is(err, orchestrator.ErrQuotaExceeded):
		return &ToolError{Code: CodeQuotaExceeded, Message: "the node or the account reached a limit", CorrelationID: correlationID, Err: err}
	case errors.Is(err, orchestrator.ErrPolicyDenied):
		return &ToolError{Code: CodePermissionDenied, Message: "the request is not allowed for this actor", CorrelationID: correlationID, Err: err}
	case errors.Is(err, orchestrator.ErrTemplateNotFound):
		return &ToolError{Code: CodeNotFound, Message: "the template does not exist", CorrelationID: correlationID, Err: err}
	case errors.Is(err, orchestrator.ErrInstanceNotFound):
		return &ToolError{Code: CodeNotFound, Message: "the instance does not exist", CorrelationID: correlationID, Err: err}
	case errors.Is(err, orchestrator.ErrConflict):
		return &ToolError{Code: CodeConflict, Message: "the request conflicts with the current state", CorrelationID: correlationID, Err: err}
	case errors.Is(err, runtime.ErrNotSupported):
		return &ToolError{Code: CodeCapabilityUnavailable, Message: "the node does not support this operation", CorrelationID: correlationID, Err: err}
	case errors.Is(err, runtime.ErrUnavailable):
		return &ToolError{Code: CodeCapabilityUnavailable, Message: "the runtime backend is not available", CorrelationID: correlationID, Err: err}
	case errors.Is(err, runtime.ErrNotFound):
		return &ToolError{Code: CodeNotFound, Message: "the workload does not exist", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrDigestMismatch):
		return &ToolError{Code: CodeDigestMismatch, Message: "the content does not match the announced digest", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrInvalidDigest):
		return &ToolError{Code: CodeInvalidRequest, Message: "the digest is not a sha256 value", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrSizeMismatch):
		return &ToolError{Code: CodeInvalidRequest, Message: "the content does not match the announced size", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrUploadTooLarge):
		return &ToolError{Code: CodeQuotaExceeded, Message: "the file is larger than the accepted size", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrUploadNotFound):
		return &ToolError{Code: CodeNotFound, Message: "the upload does not exist", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrUploadForbidden):
		return &ToolError{Code: CodePermissionDenied, Message: "the upload belongs to another account", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrUploadExpired):
		return &ToolError{Code: CodeConflict, Message: "the upload is no longer available", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrUploadFinished):
		return &ToolError{Code: CodeConflict, Message: "the upload was already completed", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrTokenInvalid):
		return &ToolError{Code: CodeNotFound, Message: "the download token is not valid", CorrelationID: correlationID, Err: err}
	case errors.Is(err, artifacts.ErrTokenExpired):
		return &ToolError{Code: CodeNotFound, Message: "the download token expired", CorrelationID: correlationID, Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &ToolError{Code: CodeJobTimeout, Message: "the operation did not finish in time", CorrelationID: correlationID, Retryable: true, Err: err}
	case errors.Is(err, context.Canceled):
		return &ToolError{Code: CodeConflict, Message: "the operation was canceled", CorrelationID: correlationID, Err: err}
	default:
		return &ToolError{Code: CodeInternal, Message: "the request could not be completed", CorrelationID: correlationID, Err: err}
	}
}

// classifyRuntime maps a failure of an execution backend.
//
// A backend that fails is a runtime error and not an internal one: the
// request was understood, the workload could not be served.
func classifyRuntime(err error, correlationID string) *ToolError {
	classified := classify(err, correlationID)
	if classified == nil {
		return nil
	}
	if classified.Code == CodeInternal {
		classified.Code = CodeRuntimeError
		classified.Retryable = true
	}
	return classified
}

// statusFor maps a code onto the HTTP status used by the transport.
//
// The transport answers before the MCP session exists, so the status is the
// only signal a client gets from a refused connection.
func statusFor(code Code) int {
	switch code {
	case CodeAuthenticationFailed:
		return 401
	case CodePermissionDenied:
		return 403
	case CodeInvalidRequest, CodeNotFound, CodeConflict, CodeDigestMismatch:
		return 400
	case CodeQuotaExceeded:
		return 429
	case CodeJobTimeout:
		return 504
	case CodeCapabilityUnavailable, CodeRuntimeError, CodeHumanActionRequired, CodeArtifactExpired:
		return 503
	default:
		return 500
	}
}
