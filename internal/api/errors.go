package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
)

// Code is a stable, machine-readable error code.
//
// The vocabulary is the one documented in docs/MCP_API_AND_DISCOVERY.md so
// that a REST client and an MCP client see exactly the same codes.
type Code string

// The error codes of the control plane.
const (
	// CodeAuthenticationFailed means the credentials are missing, invalid or
	// no longer usable.
	CodeAuthenticationFailed Code = "authentication_failed"
	// CodePermissionDenied means the actor is known but not allowed to
	// perform the request.
	CodePermissionDenied Code = "permission_denied"
	// CodeQuotaExceeded means a node or user limit was reached.
	CodeQuotaExceeded Code = "quota_exceeded"
	// CodeCapabilityUnavailable means the node cannot serve the request,
	// for example because the runtime backend is down.
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

// Error is a failure that carries a stable code.
//
// The wrapped cause is kept for the logs; it is never sent to the client
// because it may contain host paths or other internal details.
type Error struct {
	// Code is the machine-readable code of the failure.
	Code Code
	// Message is the human-readable explanation. It is written in plain
	// English and never contains a host path or a secret.
	Message string
	// Details carries safe, structured context about the failure.
	Details map[string]any
	// Err is the wrapped cause.
	Err error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the wrapped cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// errorf builds an API error with a wrapped cause.
func errorf(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Err: cause}
}

// errorBody is the payload of the error envelope.
type errorBody struct {
	// Code is the machine-readable code.
	Code Code `json:"code"`
	// Message is the human-readable explanation.
	Message string `json:"message"`
	// Retryable reports whether repeating the request may succeed. The
	// hint is published under both spellings so that a client written for
	// either one keeps working.
	Retryable bool `json:"retryable"`
	Retry     bool `json:"retry"`
	// CorrelationID ties the failure to the request and to the audit log.
	CorrelationID string `json:"correlation_id"`
	// Details carries safe, structured context.
	Details map[string]any `json:"details,omitempty"`
}

// errorEnvelope is the body of every failed request.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// statusFor maps a code onto the HTTP status of the answer.
func statusFor(code Code) int {
	switch code {
	case CodeAuthenticationFailed:
		return http.StatusUnauthorized
	case CodePermissionDenied:
		return http.StatusForbidden
	case CodeQuotaExceeded:
		return http.StatusTooManyRequests
	case CodeCapabilityUnavailable:
		return http.StatusServiceUnavailable
	case CodeRuntimeError:
		return http.StatusInternalServerError
	case CodeJobTimeout:
		return http.StatusGatewayTimeout
	case CodeArtifactExpired:
		return http.StatusGone
	case CodeHumanActionRequired, CodeConflict:
		return http.StatusConflict
	case CodeNotFound:
		return http.StatusNotFound
	case CodeInvalidRequest, CodeDigestMismatch:
		return http.StatusBadRequest
	case CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// retryableFor reports whether a client may usefully repeat the request.
func retryableFor(code Code) bool {
	switch code {
	case CodeQuotaExceeded, CodeCapabilityUnavailable, CodeRuntimeError, CodeJobTimeout:
		return true
	default:
		return false
	}
}

// writeError answers a request with the standard error envelope.
func writeError(w http.ResponseWriter, r *http.Request, code Code, message string, details map[string]any) {
	writeErrorStatus(w, r, statusFor(code), code, message, details)
}

// writeErrorStatus answers a request with the standard error envelope and an
// explicit status, for the cases where the status does not follow from the
// code.
func writeErrorStatus(w http.ResponseWriter, r *http.Request, status int, code Code, message string, details map[string]any) {
	envelope := errorEnvelope{Error: errorBody{
		Code:          code,
		Message:       message,
		Retryable:     retryableFor(code),
		Retry:         retryableFor(code),
		CorrelationID: requestID(r),
		Details:       details,
	}}
	writeJSON(w, r, status, envelope)
}

// writeError answers a request with the envelope of an API error.
func (e *Error) write(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, e.Code, e.Message, e.Details)
}

// classify maps an error of another package onto the stable code vocabulary.
//
// The mapping is deliberately coarse: the client learns what kind of failure
// happened, never where on the host it happened.
func classify(err error) *Error {
	if err == nil {
		return nil
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errorf(CodeNotFound, "the resource does not exist", err)
	case errors.Is(err, store.ErrConflict):
		return errorf(CodeConflict, "the request conflicts with an existing resource", err)
	case errors.Is(err, orchestrator.ErrQuotaExceeded):
		return errorf(CodeQuotaExceeded, "the node or the account reached a limit", err)
	case errors.Is(err, orchestrator.ErrPolicyDenied):
		return errorf(CodePermissionDenied, "the request is not allowed for this actor", err)
	case errors.Is(err, orchestrator.ErrTemplateNotFound):
		return errorf(CodeNotFound, "the template does not exist", err)
	case errors.Is(err, orchestrator.ErrInstanceNotFound):
		return errorf(CodeNotFound, "the instance does not exist", err)
	case errors.Is(err, orchestrator.ErrConflict):
		return errorf(CodeConflict, "the request conflicts with the current state", err)
	case errors.Is(err, orchestrator.ErrInvalidRequest):
		return errorf(CodeInvalidRequest, "the request is not valid", err)
	case errors.Is(err, artifacts.ErrNotFound):
		return errorf(CodeNotFound, "the artifact does not exist", err)
	case errors.Is(err, artifacts.ErrTooLarge):
		return errorf(CodeInvalidRequest, "the file is larger than the accepted size", err)
	case errors.Is(err, artifacts.ErrInvalidName):
		return errorf(CodeInvalidRequest, "the file name is not acceptable", err)
	case errors.Is(err, artifacts.ErrDigestMismatch):
		return errorf(CodeDigestMismatch, "the content does not match the announced digest", err)
	case errors.Is(err, artifacts.ErrInvalidDigest):
		return errorf(CodeInvalidRequest, "the digest is not a sha256 value", err)
	case errors.Is(err, artifacts.ErrSizeMismatch):
		return errorf(CodeInvalidRequest, "the content does not match the announced size", err)
	case errors.Is(err, artifacts.ErrUploadTooLarge):
		return errorf(CodeQuotaExceeded, "the file is larger than the accepted size", err)
	case errors.Is(err, artifacts.ErrUploadNotFound):
		return errorf(CodeNotFound, "the upload does not exist", err)
	case errors.Is(err, artifacts.ErrUploadForbidden):
		return errorf(CodePermissionDenied, "the upload belongs to another user", err)
	case errors.Is(err, artifacts.ErrUploadExpired):
		return errorf(CodeConflict, "the upload is no longer available", err)
	case errors.Is(err, artifacts.ErrUploadFinished):
		return errorf(CodeConflict, "the upload was already completed", err)
	case errors.Is(err, artifacts.ErrTokenInvalid):
		return errorf(CodeNotFound, "the download link is not available", err)
	case errors.Is(err, artifacts.ErrTokenExpired):
		return errorf(CodeNotFound, "the download link is not available", err)
	case errors.Is(err, auth.ErrMismatch), errors.Is(err, auth.ErrInvalidHash):
		return errorf(CodeAuthenticationFailed, "the credentials are not valid", err)
	case errors.Is(err, auth.ErrWeakPassword):
		return errorf(CodeInvalidRequest, "the password does not meet the policy", err)
	case errors.Is(err, auth.ErrSessionExpired), errors.Is(err, auth.ErrSessionInvalid), errors.Is(err, auth.ErrUserDisabled):
		return errorf(CodeAuthenticationFailed, "the session is not valid", err)
	case errors.Is(err, runtime.ErrNotSupported):
		return errorf(CodeCapabilityUnavailable, "the node does not support this operation", err)
	case errors.Is(err, runtime.ErrUnavailable):
		return errorf(CodeCapabilityUnavailable, "the runtime backend is not available", err)
	case errors.Is(err, runtime.ErrAgentUnavailable):
		return errorf(CodeCapabilityUnavailable, "the guest agent of the instance is not answering; the guest may still be booting or the image carries no agent", err)
	case errors.Is(err, runtime.ErrNotFound):
		return errorf(CodeNotFound, "the workload does not exist", err)
	case errors.Is(err, context.DeadlineExceeded):
		return errorf(CodeJobTimeout, "the operation did not finish in time", err)
	case errors.Is(err, context.Canceled):
		return errorf(CodeConflict, "the operation was canceled", err)
	default:
		return errorf(CodeInternal, "the request could not be completed", err)
	}
}
