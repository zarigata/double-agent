// Package api implements the HTTP surface of the Double Agent control plane.
//
// The same handler serves the REST API under /api/v1, the embedded Web UI
// under / and the certificate authority under /ca.crt. Every request passes
// through one middleware chain, so authentication, CSRF protection, the
// security headers and the audit trail cannot be bypassed by choosing a
// different entry point.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/auth"
	"github.com/zarigata/double-agent/internal/config"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
	"github.com/zarigata/double-agent/internal/templates"
)

// maxJSONBody is the largest JSON request body the API accepts. The limit
// keeps a hostile client from exhausting the memory of the daemon with a
// single request.
const maxJSONBody = 1 << 20

// defaultLimit is the number of records a listing returns when the caller
// does not ask for a specific size.
const defaultLimit = 100

// maxLimit is the largest number of records a listing may return.
const maxLimit = 1000

// Execution and lifecycle defaults.
const (
	// defaultExecTimeout bounds a command that does not ask for a timeout.
	// A command without a deadline could hold a worker forever.
	defaultExecTimeout = 15 * time.Minute
	// maxExecTimeout is the longest lifetime a command may ask for.
	maxExecTimeout = time.Hour
	// defaultStopGrace is the time an instance may take to shut down.
	defaultStopGrace = 10 * time.Second
	// consoleTTL is the lifetime of a console endpoint.
	consoleTTL = 5 * time.Minute
	// defaultUploadLimit is the largest upload accepted when the
	// configuration does not name a limit.
	defaultUploadLimit = 256 << 20
	// uploadTTL is the lifetime of a pending upload.
	uploadTTL = 24 * time.Hour
	// downloadTTL is the lifetime of a download token.
	downloadTTL = 5 * time.Minute
)

// Options configures the API server.
type Options struct {
	// Store is the metadata database.
	Store *store.Store
	// Orchestrator drives the control plane workflow.
	Orchestrator *orchestrator.Orchestrator
	// Artifacts is the controlled file store.
	Artifacts *artifacts.Store
	// Config is the daemon configuration.
	Config config.Config
	// Logger receives the request logs.
	Logger *slog.Logger
	// Registry holds the runtime backends of the node.
	Registry *runtime.Registry
	// Catalogue holds the published templates.
	Catalogue *templates.Catalogue
}

// Server is the HTTP surface of the control plane.
type Server struct {
	store     *store.Store
	orch      *orchestrator.Orchestrator
	artifacts *artifacts.Store
	cfg       config.Config
	log       *slog.Logger
	registry  *runtime.Registry
	catalogue *templates.Catalogue
	sessions  *auth.SessionManager
	mux       *http.ServeMux

	// uploads manages the resumable uploads of the node. The state lives
	// next to the artifact store, so the MCP surface and this surface
	// agree on an upload.
	uploads *artifacts.Uploads

	// downloads manages the download tokens of the node.
	downloads *artifacts.Downloads

	// now returns the current time. Tests replace it to get deterministic
	// timestamps.
	now func() time.Time

	// fail records why the server could not be built. Every request then
	// fails with an internal error instead of panicking.
	fail error
}

// New creates the API server.
//
// The returned handler routes on the complete request path, so the daemon can
// mount it at both "/api/" and "/" without stripping a prefix.
func New(opts Options) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		store:     opts.Store,
		orch:      opts.Orchestrator,
		artifacts: opts.Artifacts,
		cfg:       opts.Config,
		log:       logger.With("component", "api"),
		registry:  opts.Registry,
		catalogue: opts.Catalogue,
		now:       func() time.Time { return time.Now().UTC() },
	}
	if opts.Store == nil {
		s.fail = errors.New("api: the metadata store is required")
	} else {
		sessions, err := auth.NewSessionManager(auth.SessionOptions{
			Store:  opts.Store,
			TTL:    opts.Config.Auth.SessionTTL.Duration(),
			Secure: opts.Config.Listen.TLS.Enabled,
		})
		if err != nil {
			s.fail = fmt.Errorf("api: create the session manager: %w", err)
		} else {
			s.sessions = sessions
		}
	}
	if s.artifacts != nil {
		uploads, err := artifacts.NewUploads(artifacts.UploadOptions{
			Root:     filepath.Join(s.artifacts.Root(), "uploads"),
			Store:    s.artifacts,
			MaxBytes: s.cfg.Artifacts.MaxUploadBytes,
			TTL:      uploadTTL,
		})
		if err != nil {
			s.fail = fmt.Errorf("api: open the upload manager: %w", err)
		} else {
			s.uploads = uploads
		}
		downloads, err := artifacts.NewDownloads(artifacts.DownloadOptions{
			Root: filepath.Join(s.artifacts.Root(), "downloads"),
			TTL:  downloadTTL,
		})
		if err != nil {
			s.fail = fmt.Errorf("api: open the download manager: %w", err)
		} else {
			s.downloads = downloads
		}
	}
	s.mux = s.buildMux()
	return s
}

// ServeHTTP applies the middleware chain and dispatches the request.
//
// The order of the chain matters: the request identifier and the security
// headers are set before anything can fail, and authentication happens before
// the CSRF check so that the check knows how the caller authenticated.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.fail != nil {
		s.log.Error("the API is not available", "error", s.fail)
		writeError(w, r, CodeInternal, "the API is not available", nil)
		return
	}
	// The authentication state is created before the chain so that the
	// request log can name the actor of a request even though it runs
	// before the authentication.
	state := &authState{}
	r = r.WithContext(context.WithValue(r.Context(), authKey, state))

	handler := s.requestIDMiddleware(
		s.loggingMiddleware(
			s.recoveryMiddleware(
				s.securityHeadersMiddleware(
					s.corsMiddleware(
						s.authMiddleware(
							s.csrfMiddleware(s.mux)))))))
	handler.ServeHTTP(w, r)
}

// buildMux registers every route of the API.
//
// The method of a route is resolved inside the handler instead of in the mux
// pattern so that a wrong method is reported with the same JSON envelope as
// every other failure.
func (s *Server) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Health and discovery.
	s.route(mux, "/api/v1/healthz",
		methodRoute{methods: []string{http.MethodGet}, public: true, action: "health.read", handler: s.handleHealth})
	s.route(mux, "/api/v1/capabilities",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeCapabilitiesRead, action: "capabilities.read", handler: s.handleCapabilities})

	// Authentication.
	s.route(mux, "/api/v1/auth/login",
		methodRoute{methods: []string{http.MethodPost}, public: true, action: "auth.login", handler: s.handleLogin})
	s.route(mux, "/api/v1/auth/logout",
		methodRoute{methods: []string{http.MethodPost}, action: "auth.logout", handler: s.handleLogout})
	s.route(mux, "/api/v1/auth/me",
		methodRoute{methods: []string{http.MethodGet}, action: "auth.me", handler: s.handleMe})

	// Templates.
	s.route(mux, "/api/v1/templates",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeTemplatesRead, action: "template.list", handler: s.handleTemplateList})
	s.route(mux, "/api/v1/templates/{id}",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeTemplatesRead, action: "template.get", handler: s.handleTemplateGet})

	// Instances.
	s.route(mux, "/api/v1/instances",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeInstancesRead, action: "instance.list", handler: s.handleInstanceList},
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeInstancesCreate, action: "instance.create", handler: s.handleInstanceCreate})
	s.route(mux, "/api/v1/instances/{id}",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeInstancesRead, action: "instance.get", handler: s.handleInstanceGet},
		methodRoute{methods: []string{http.MethodDelete}, scope: policy.ScopeInstancesDestroy, action: "instance.destroy", handler: s.handleInstanceDestroy})
	s.route(mux, "/api/v1/instances/{id}/exec",
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeInstancesExec, action: "instance.exec", handler: s.handleInstanceExec})
	s.route(mux, "/api/v1/instances/{id}/stop",
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeInstancesStop, action: "instance.stop", handler: s.handleInstanceStop})
	s.route(mux, "/api/v1/instances/{id}/extend-ttl",
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeInstancesCreate, action: "instance.extend_ttl", handler: s.handleInstanceExtendTTL})
	s.route(mux, "/api/v1/instances/{id}/console",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeInstancesRead, action: "instance.console", handler: s.handleInstanceConsole})

	// Jobs.
	s.route(mux, "/api/v1/jobs",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeJobsRead, action: "job.list", handler: s.handleJobList})
	s.route(mux, "/api/v1/jobs/{id}",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeJobsRead, action: "job.get", handler: s.handleJobGet})
	s.route(mux, "/api/v1/jobs/{id}/cancel",
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeJobsCancel, action: "job.cancel", handler: s.handleJobCancel})

	// Artifacts.
	s.route(mux, "/api/v1/artifacts",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeArtifactsRead, action: "artifact.list", handler: s.handleArtifactList},
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeArtifactsWrite, action: "artifact.upload", handler: s.handleArtifactUpload})
	s.route(mux, "/api/v1/artifacts/export",
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeArtifactsWrite, action: "artifact.export", handler: s.handleArtifactExport})
	s.route(mux, "/api/v1/artifacts/{id}",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeArtifactsRead, action: "artifact.get", handler: s.handleArtifactGet},
		methodRoute{methods: []string{http.MethodDelete}, scope: policy.ScopeArtifactsWrite, action: "artifact.delete", handler: s.handleArtifactDelete})
	s.route(mux, "/api/v1/artifacts/{id}/download",
		methodRoute{methods: []string{http.MethodGet}, public: true, action: "artifact.download", handler: s.handleArtifactDownload})

	// Uploads.
	s.route(mux, "/api/v1/uploads",
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeArtifactsWrite, action: "upload.begin", handler: s.handleUploadBegin})
	s.route(mux, "/api/v1/uploads/{id}",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeArtifactsRead, action: "upload.status", handler: s.handleUploadStatus},
		methodRoute{methods: []string{http.MethodPut}, scope: policy.ScopeArtifactsWrite, action: "upload.content", handler: s.handleUploadContent})
	s.route(mux, "/api/v1/uploads/{id}/complete",
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeArtifactsWrite, action: "upload.complete", handler: s.handleUploadComplete})

	// Administration.
	s.route(mux, "/api/v1/admin/users",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeUsersManage, action: "user.list", handler: s.handleUserList},
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeUsersManage, action: "user.create", handler: s.handleUserCreate})
	s.route(mux, "/api/v1/admin/users/{id}/disable",
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeUsersManage, action: "user.disable", handler: s.handleUserDisable})
	s.route(mux, "/api/v1/admin/keys",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeKeysManage, action: "key.list", handler: s.handleKeyList},
		methodRoute{methods: []string{http.MethodPost}, scope: policy.ScopeKeysManage, action: "key.create", handler: s.handleKeyCreate})
	s.route(mux, "/api/v1/admin/keys/{id}",
		methodRoute{methods: []string{http.MethodDelete}, scope: policy.ScopeKeysManage, action: "key.revoke", handler: s.handleKeyRevoke})
	s.route(mux, "/api/v1/audit",
		methodRoute{methods: []string{http.MethodGet}, scope: policy.ScopeAdmin, action: "audit.list", handler: s.handleAuditList})

	// The Web UI and the certificate authority.
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// methodRoute binds the methods of a path to a handler, a scope and an
// audit action.
type methodRoute struct {
	// methods are the HTTP methods the handler answers.
	methods []string
	// scope is the permission the caller needs. An empty scope only asks
	// for an authenticated caller.
	scope policy.Scope
	// public marks a route that answers without an authenticated caller.
	public bool
	// action is the stable vocabulary of the audit log.
	action string
	// handler answers the request.
	handler http.HandlerFunc
}

// route registers the handlers of a path.
//
// A path is registered once, even when it accepts several methods, because
// the Go mux refuses a second registration of the same pattern. The method
// is resolved here so that a wrong method is reported with the same JSON
// envelope as every other failure.
func (s *Server) route(mux *http.ServeMux, pattern string, routes ...methodRoute) {
	allowed := make([]string, 0, len(routes))
	byMethod := map[string]methodRoute{}
	for _, entry := range routes {
		for _, method := range entry.methods {
			allowed = append(allowed, method)
			byMethod[method] = entry
		}
	}
	guarded := make(map[string]http.HandlerFunc, len(byMethod))
	for method, entry := range byMethod {
		guarded[method] = s.guard(entry.scope, entry.public, entry.action, entry.handler)
	}
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		_, ok := byMethod[method]
		if !ok && method == http.MethodHead {
			// A HEAD request is answered like a GET request without a body.
			_, ok = byMethod[http.MethodGet]
			method = http.MethodGet
		}
		if !ok {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writeErrorStatus(w, r, http.StatusMethodNotAllowed, CodeInvalidRequest,
				"the method is not allowed for this endpoint",
				map[string]any{"allow": allowed})
			return
		}
		guarded[method](w, r)
	})
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Default().Error("failed to encode a response", "error", err, "request_id", requestID(r))
		body = []byte(`{"error":{"code":"internal_error","message":"the response could not be encoded"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// decodeJSON reads a JSON request body into dst.
//
// An empty body is accepted: the handlers validate the fields they need and
// report a missing field themselves.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errorf(CodeInvalidRequest, "the request body is too large", err)
		}
		return errorf(CodeInvalidRequest, "the request body is not valid JSON", err)
	}
	return nil
}

// writeDecodeError answers a request that carried an unreadable body.
func writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	classified := classify(err)
	classified.write(w, r)
}

// requestID returns the correlation identifier of a request.
func requestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if value, ok := r.Context().Value(requestIDKey).(string); ok {
		return value
	}
	return ""
}

// actorFrom returns the authenticated actor of a request.
func actorFrom(r *http.Request) policy.Actor {
	return authStateFrom(r).actor
}

// authStateFrom returns the authentication state of a request.
//
// The state is never nil, so a caller can read it without a check.
func authStateFrom(r *http.Request) *authState {
	if state, ok := r.Context().Value(authKey).(*authState); ok && state != nil {
		return state
	}
	return &authState{}
}

// limitQuery reads the size of a listing from the query string.
func limitQuery(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultLimit
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultLimit
	}
	if value > maxLimit {
		return maxLimit
	}
	return value
}

// pathValue returns a wildcard of the route pattern.
func pathValue(r *http.Request, name string) string {
	return strings.TrimSpace(r.PathValue(name))
}

// audit writes a security relevant record to the audit log.
func (s *Server) audit(r *http.Request, actor policy.Actor, action, decision, result string) {
	if s.store == nil {
		return
	}
	actorType := actor.Type
	if actorType == "" {
		actorType = store.ActorUser
	}
	event := store.AuditEvent{
		ID:            ids.New(ids.PrefixAudit),
		Timestamp:     s.now(),
		ActorType:     actorType,
		ActorID:       actor.ID,
		Action:        action,
		TargetType:    "http",
		TargetID:      r.Method + " " + r.URL.Path,
		Decision:      decision,
		CorrelationID: requestID(r),
		Result:        result,
	}
	if _, err := s.store.AppendAuditEvent(r.Context(), event); err != nil {
		s.log.Error("failed to write an audit event", "action", action, "error", err)
	}
}

// ownsResource reports whether the actor may act on a resource of ownerID.
//
// Administrators may act on every resource of the node.
func ownsResource(actor policy.Actor, ownerID string) bool {
	return policy.Owns(actor, ownerID)
}

// scopedUserID returns the owner a listing is limited to.
//
// A caller without the administrative scope only ever sees its own records.
func scopedUserID(actor policy.Actor) string {
	if actor.Scopes.Has(policy.ScopeAdmin) {
		return ""
	}
	return actor.ID
}
