// Package mcpsrv implements the MCP surface of the Double Agent control plane.
//
// The package exposes the tools documented in docs/MCP_API_AND_DISCOVERY.md
// over the Streamable HTTP transport. Every tool is small, strongly typed and
// idempotent where it matters, and every failure carries one of the stable
// error codes of the document together with a correlation identifier and a
// retry hint.
//
// Two rules shape the whole package:
//
//   - An MCP client key never inherits the power of a browser session. The
//     administrative scopes are dropped when the actor is built and the actor
//     is never an administrator, and the surface exposes no tool that could
//     administer the accounts of the node.
//   - Secrets are opaque references. A tool never returns a secret value and
//     never writes one into a log.
//
// The intent-level tools of the document are not part of this surface: they
// belong to the work the roadmap schedules after the MVP. The desktop tools
// are part of the surface even though the node runs CPU only: they answer
// with the capability_unavailable code and an explanation, so the contract
// of the surface stays stable for the phases that add a desktop.
package mcpsrv

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/orchestrator"
	"github.com/zarigata/double-agent/internal/policy"
	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/store"
)

// ToolNames lists the tools exposed by the control plane.
//
// The list is the contract of the surface: a certified client checks that the
// node answers with exactly these names.
var ToolNames = []string{
	"artifact.download",
	"artifact.export",
	"artifact.list",
	"capabilities.get",
	"desktop.close",
	"desktop.open",
	"docs.search",
	"file.upload.begin",
	"file.upload.complete",
	"instance.create",
	"instance.destroy",
	"instance.exec",
	"instance.extend_ttl",
	"instance.get",
	"instance.stop",
	"job.cancel",
	"job.get",
	"job.list",
	"job.logs",
	"template.get",
	"template.list",
}

// ResourceNames lists the agent-readable resources of the node.
//
// The names are the URIs of docs/MCP_API_AND_DISCOVERY.md. The templates,
// jobs and artifacts are served through resource templates, so only their
// shape appears here.
var ResourceNames = []string{
	"double-agent://capabilities/current",
	"double-agent://docs/errors",
	"double-agent://docs/quickstart",
	"double-agent://docs/security-model",
}

// Options configures the MCP server.
type Options struct {
	// Logger receives the logs of the server.
	Logger *slog.Logger
	// Store is the metadata database.
	Store *store.Store
	// Registry holds the runtime backends of the node. When it is empty the
	// registry of the orchestrator is used.
	Registry *runtime.Registry
	// Version is the version reported to the clients.
	Version string
	// CAPath is the certificate authority published at /ca.crt. When it is
	// empty the endpoint answers that no certificate is available.
	CAPath string
	// Artifacts is the controlled file store of the node. When it is empty
	// the store of the orchestrator is used.
	Artifacts *artifacts.Store
	// ArtifactRetention is the lifetime of an artifact of the node.
	ArtifactRetention time.Duration
	// UploadTTL is the lifetime of a pending upload.
	UploadTTL time.Duration
	// UploadMaxBytes is the largest upload the node accepts. When it is
	// empty the limit of the artifact store is used.
	UploadMaxBytes int64
	// DownloadTTL is the lifetime of a download token.
	DownloadTTL time.Duration
}

// Server is the MCP surface of the control plane.
//
// The server keeps one MCP server per actor: the tools are bound to the
// actor, so two credentials never share a session or a scope set.
type Server struct {
	orch     *orchestrator.Orchestrator
	store    *store.Store
	registry *runtime.Registry
	log      *slog.Logger
	version  string
	caPath   string

	// artifacts is the controlled file store of the node.
	artifacts *artifacts.Store
	// uploads manages the resumable uploads of the node.
	uploads *artifacts.Uploads
	// downloads manages the download tokens of the node.
	downloads *artifacts.Downloads
	// retention is the lifetime of an artifact.
	retention time.Duration
	// now returns the current time. Tests replace it.
	now func() time.Time

	// mu guards the cache of per-actor servers.
	mu sync.Mutex
	// actors maps the fingerprint of an actor to its MCP server.
	actors map[string]*mcp.Server
}

// NewServer creates the MCP server of the node.
func NewServer(orch *orchestrator.Orchestrator, opts Options) (*Server, error) {
	if orch == nil {
		return nil, errors.New("mcpsrv: the orchestrator is required")
	}
	if opts.Store == nil {
		return nil, errors.New("mcpsrv: the metadata store is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	registry := opts.Registry
	if registry == nil {
		registry = orch.Runtimes()
	}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "0.0.0-dev"
	}
	store := opts.Artifacts
	if store == nil {
		store = orch.Artifacts()
	}
	if store == nil {
		return nil, errors.New("mcpsrv: the artifact store is required")
	}
	retention := opts.ArtifactRetention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	maxUpload := opts.UploadMaxBytes
	if maxUpload <= 0 {
		maxUpload = store.MaxSize()
	}
	uploads, err := artifacts.NewUploads(artifacts.UploadOptions{
		Root:     filepath.Join(store.Root(), "uploads"),
		Store:    store,
		MaxBytes: maxUpload,
		TTL:      opts.UploadTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("mcpsrv: open the upload manager: %w", err)
	}
	downloads, err := artifacts.NewDownloads(artifacts.DownloadOptions{
		Root: filepath.Join(store.Root(), "downloads"),
		TTL:  opts.DownloadTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("mcpsrv: open the download manager: %w", err)
	}
	return &Server{
		orch:      orch,
		store:     opts.Store,
		registry:  registry,
		log:       logger.With("component", "mcpsrv"),
		version:   version,
		caPath:    strings.TrimSpace(opts.CAPath),
		artifacts: store,
		uploads:   uploads,
		downloads: downloads,
		retention: retention,
		now:       func() time.Time { return time.Now().UTC() },
		actors:    map[string]*mcp.Server{},
	}, nil
}

// Version returns the version reported to the clients.
func (s *Server) Version() string { return s.version }

// MCPServer returns the MCP server that serves an actor.
//
// The server is built once per actor and then reused: the schema of every
// tool is inferred when the server is built, and an inference per request
// would waste the work of the node.
func (s *Server) MCPServer(actor policy.Actor) *mcp.Server {
	key := actorKey(actor)
	s.mu.Lock()
	defer s.mu.Unlock()
	if server, ok := s.actors[key]; ok {
		return server
	}
	server := s.buildServer(actor)
	s.actors[key] = server
	return server
}

// buildServer creates an MCP server with the tools of the surface.
func (s *Server) buildServer(actor policy.Actor) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "double-agent",
		Title:   "Double Agent",
		Version: s.version,
	}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       s.log,
	})

	s.addCapabilitiesTool(server, actor)
	s.addTemplateTools(server, actor)
	s.addDocsTool(server, actor)
	s.addInstanceTools(server, actor)
	s.addJobTools(server, actor)
	s.addArtifactTools(server, actor)
	s.addUploadTools(server, actor)
	s.addDesktopTools(server, actor)
	s.addResources(server, actor)

	return server
}

// instructions tells the client what the surface is for.
const instructions = "Double Agent runs disposable workloads on this node. " +
	"Create an instance from a template, run commands inside it with an argument " +
	"vector, and collect the results as artifacts. Every failure carries a stable " +
	"error code, a correlation identifier and a retry hint."

// actorKey builds the fingerprint of an actor.
//
// The fingerprint covers the identity and the scopes of the actor, so a key
// that was issued with fewer scopes never receives the server of a key that
// was issued with more.
func actorKey(actor policy.Actor) string {
	scopes := actor.Scopes.List()
	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		names = append(names, string(scope))
	}
	return strings.Join([]string{
		actor.Type,
		actor.ID,
		actor.Username,
		strings.Join(names, ","),
	}, "\x00")
}

// requireScope reports a permission error when the actor misses a scope.
//
// The scope is checked before the work starts, so a call that the key may not
// perform never reaches the control plane.
func requireScope(actor policy.Actor, scope policy.Scope, correlationID string) *ToolError {
	decision := policy.Evaluate(actor, scope)
	if decision.Allowed {
		return nil
	}
	return permissionDenied(
		"the credential does not carry the scope this call needs",
		correlationID,
		map[string]any{"scope": string(scope)},
	)
}

// requestCorrelationID returns the correlation identifier of a request.
//
// A client may supply its own identifier, which lets an operator follow one
// call from the client to the audit records of the node.
func requestCorrelationID(r *http.Request) string {
	if r == nil {
		return ids.New(ids.PrefixRequest)
	}
	supplied := strings.TrimSpace(r.Header.Get("X-Request-Id"))
	if validCorrelationID(supplied) {
		return supplied
	}
	return ids.New(ids.PrefixRequest)
}

// validCorrelationID reports whether a client supplied identifier may be
// reused.
func validCorrelationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

// ownsResource reports whether the actor owns a resource.
//
// The MCP actor is never an administrator, so the ownership check is strict:
// a client never sees the resources of another account.
func ownsResource(actor policy.Actor, ownerID string) bool {
	return actor.ID != "" && ownerID != "" && actor.ID == ownerID
}
