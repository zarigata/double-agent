package api

import (
	"encoding/json"
	"net/http"

	"github.com/zarigata/double-agent/internal/templates"
)

// runtimeView describes a runtime backend of the node.
type runtimeView struct {
	// Name is the identifier of the backend, for example "docker".
	Name string `json:"name"`
	// Kind is the runtime family, "container" or "vm".
	Kind string `json:"kind"`
	// Healthy reports whether the backend may be used.
	Healthy bool `json:"healthy"`
	// Error explains why an unhealthy backend cannot be used. It never
	// contains a host path.
	Error string `json:"error,omitempty"`
}

// handleHealth answers the liveness probe of the daemon.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{"status": "ok"})
}

// handleCapabilities describes what this node can do.
//
// The answer is the discovery document of the control plane: the backends,
// the templates, the limits and the network policy.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	runtimes := make([]runtimeView, 0)
	if s.registry != nil {
		for _, name := range s.registry.Names() {
			backend, ok := s.registry.Get(name)
			if !ok {
				continue
			}
			view := runtimeView{Name: backend.Name(), Kind: string(backend.Kind()), Healthy: true}
			if err := backend.Health(r.Context()); err != nil {
				view.Healthy = false
				view.Error = "the backend is not available"
				// The detail stays in the log: it may name a socket or a
				// path of the host.
				s.log.Warn("a runtime backend is not healthy",
					"backend", backend.Name(), "error", err, "request_id", requestID(r))
			}
			runtimes = append(runtimes, view)
		}
	}

	catalogue := make([]map[string]any, 0)
	if s.catalogue != nil {
		for _, tmpl := range s.catalogue.List() {
			catalogue = append(catalogue, templateView(tmpl))
		}
	}

	limits := map[string]any{
		"max_concurrent_jobs":    s.cfg.Limits.MaxConcurrentJobs,
		"max_instances":          s.cfg.Limits.MaxInstances,
		"max_instances_per_user": s.cfg.Limits.MaxInstancesPerUser,
		"default_ttl_seconds":    int64(s.cfg.Limits.DefaultTTL.Duration().Seconds()),
		"max_ttl_seconds":        int64(s.cfg.Limits.MaxTTL.Duration().Seconds()),
		"host_reserve": map[string]any{
			"cpu":       s.cfg.Limits.HostReserve.CPU,
			"memory_mb": s.cfg.Limits.HostReserve.MemoryMB,
		},
	}
	network := map[string]any{
		"allowed_origins": s.cfg.Listen.AllowedOrigins,
		"tls_enabled":     s.cfg.Listen.TLS.Enabled,
		"default_egress":  "none",
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"runtimes":  runtimes,
		"templates": catalogue,
		"limits":    limits,
		"network":   network,
	})
}

// handleTemplateList answers with the published template catalogue.
func (s *Server) handleTemplateList(w http.ResponseWriter, r *http.Request) {
	catalogue := make([]map[string]any, 0)
	if s.catalogue != nil {
		for _, tmpl := range s.catalogue.List() {
			catalogue = append(catalogue, templateView(tmpl))
		}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"templates": catalogue})
}

// handleTemplateGet answers with one version of a template.
//
// The version may be pinned with the "version" query parameter. An empty
// parameter selects the highest published version.
func (s *Server) handleTemplateGet(w http.ResponseWriter, r *http.Request) {
	id := pathValue(r, "id")
	version := r.URL.Query().Get("version")
	if s.catalogue == nil {
		writeError(w, r, CodeNotFound, "the template does not exist", nil)
		return
	}
	tmpl, ok := s.catalogue.Get(id, version)
	if !ok {
		writeError(w, r, CodeNotFound, "the template does not exist",
			map[string]any{"template_id": id, "version": version})
		return
	}
	writeJSON(w, r, http.StatusOK, templateView(tmpl))
}

// templateView renders a template as a flat JSON object.
//
// The body is the specification of the template with its digest added, so a
// client sees the same fields the catalogue stores.
func templateView(tmpl templates.Template) map[string]any {
	encoded, err := json.Marshal(tmpl.Spec)
	if err != nil {
		return map[string]any{"id": tmpl.Spec.ID, "version": tmpl.Spec.Version}
	}
	out := map[string]any{}
	if err := json.Unmarshal(encoded, &out); err != nil {
		return map[string]any{"id": tmpl.Spec.ID, "version": tmpl.Spec.Version}
	}
	out["digest"] = tmpl.Digest
	return out
}
