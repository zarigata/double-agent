package mcpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/policy"
)

// contextKey is the type of the context keys of this package. A private type
// keeps the keys from colliding with the keys of another package.
type contextKey int

const (
	// actorContextKey holds the authenticated actor of a request.
	actorContextKey contextKey = iota
	// correlationIDContextKey holds the correlation identifier of a request.
	correlationIDContextKey
)

// handler serves the HTTP surface of the MCP server.
type handler struct {
	server *Server
	// streamable is the MCP transport. It is stateless, so the node keeps no
	// session between the requests of a client.
	streamable *mcp.StreamableHTTPHandler
}

// NewHandler returns the HTTP handler of the MCP endpoint.
//
// The handler serves the Streamable HTTP transport at /mcp and the
// certificate authority at /ca.crt. The transport is stateless and answers
// with JSON: a client that loses its connection may simply open a new one.
//
// Every request to /mcp carries a client key as a bearer token. A request
// without a usable credential is refused before the transport is reached, so
// an unauthenticated client never sees the tools of the node.
func NewHandler(srv *Server) http.Handler {
	h := &handler{server: srv}
	h.streamable = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		actor, ok := actorFrom(r.Context())
		if !ok {
			return nil
		}
		return srv.MCPServer(actor)
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       srv.log,
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", h)
	mux.Handle("/ca.crt", http.HandlerFunc(h.serveCA))
	return mux
}

// ServeHTTP dispatches the requests of the MCP surface.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ca.crt" {
		h.serveCA(w, r)
		return
	}
	h.serveMCP(w, r)
}

// serveMCP authenticates the caller and hands the request to the transport.
func (h *handler) serveMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodDelete:
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "the streamable transport accepts GET, POST and DELETE requests", http.StatusMethodNotAllowed)
		return
	}

	actor, failure := h.server.authenticate(r)
	if failure != nil {
		h.writeFailure(w, r, failure)
		return
	}

	correlationID := requestCorrelationID(r)
	w.Header().Set("X-Request-Id", correlationID)
	ctx := context.WithValue(r.Context(), actorContextKey, actor)
	ctx = context.WithValue(ctx, correlationIDContextKey, correlationID)
	h.streamable.ServeHTTP(w, r.WithContext(ctx))
}

// serveCA publishes the certificate authority of the node.
//
// The certificate is public: it is the anchor a client pins to trust the
// server. It never carries a secret.
func (h *handler) serveCA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "the certificate authority is read with GET", http.StatusMethodNotAllowed)
		return
	}
	if h.server.caPath == "" {
		http.Error(w, "the certificate authority is not available", http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(h.server.caPath)
	if err != nil {
		h.server.log.Error("failed to read the certificate authority", "error", err)
		http.Error(w, "the certificate authority is not available", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// writeFailure answers a request the node refuses.
//
// The envelope carries the stable code, the message, the correlation
// identifier and the retry hint, exactly like the envelope of the REST API.
func (h *handler) writeFailure(w http.ResponseWriter, r *http.Request, failure *ToolError) {
	h.server.log.Warn("the request was refused",
		"code", failure.Code,
		"path", r.URL.Path,
		"correlation_id", failure.CorrelationID,
	)
	w.Header().Set("X-Request-Id", failure.CorrelationID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusFor(failure.Code))
	envelope := map[string]any{
		"error": map[string]any{
			"code":           failure.Code,
			"message":        failure.Message,
			"retryable":      failure.Retryable,
			"correlation_id": failure.CorrelationID,
		},
	}
	if len(failure.Details) > 0 {
		envelope["error"].(map[string]any)["details"] = failure.Details
	}
	_ = json.NewEncoder(w).Encode(envelope)
}

// actorFrom returns the actor of a request.
func actorFrom(ctx context.Context) (policy.Actor, bool) {
	actor, ok := ctx.Value(actorContextKey).(policy.Actor)
	return actor, ok
}

// correlationIDFrom returns the correlation identifier of a call.
//
// The identifier of the request that carried the call is reused, so one
// identifier follows a call from the client to the audit records of the node.
// A call that does not come from a request gets a fresh identifier.
func correlationIDFrom(ctx context.Context) string {
	if ctx != nil {
		if value, ok := ctx.Value(correlationIDContextKey).(string); ok && value != "" {
			return value
		}
	}
	return ids.New(ids.PrefixRequest)
}
