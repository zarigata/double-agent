package mcpsrv

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/policy"
)

// CapabilitiesInput is the input of the capabilities tool.
//
// The tool describes the node, so it takes no argument.
type CapabilitiesInput struct{}

// RuntimeCapability describes one execution backend of the node.
type RuntimeCapability struct {
	// Name is the identifier of the backend, for example "docker".
	Name string `json:"name"`
	// Kind is the runtime family, "container" or "vm".
	Kind string `json:"kind"`
	// Default reports whether the backend is the default of the node.
	Default bool `json:"default"`
	// Healthy reports whether the backend may be used.
	Healthy bool `json:"healthy"`
	// Error explains why an unhealthy backend cannot be used. It never
	// contains a host path.
	Error string `json:"error,omitempty"`
}

// CapabilitiesOutput describes what the node can do.
//
// The answer is the discovery document of the control plane: the backends,
// the published templates, the scopes of the caller and the tools it may call.
type CapabilitiesOutput struct {
	// Version is the version of the control plane.
	Version string `json:"version"`
	// Runtimes lists the execution backends of the node.
	Runtimes []RuntimeCapability `json:"runtimes"`
	// Templates lists the published templates of the node.
	Templates []TemplateView `json:"templates"`
	// Scopes are the scopes the caller holds.
	Scopes []string `json:"scopes"`
	// Tools are the names of the tools of this surface.
	Tools []string `json:"tools"`
	// Resources are the agent-readable resources of the node.
	Resources []string `json:"resources"`
}

// TemplateListInput is the input of the template list.
type TemplateListInput struct{}

// TemplateListOutput is the catalogue of the published templates.
type TemplateListOutput struct {
	// Templates lists the published templates, newest version last.
	Templates []TemplateView `json:"templates"`
}

// TemplateGetInput selects one version of a template.
type TemplateGetInput struct {
	// Template is the identifier of the template.
	Template string `json:"template" jsonschema:"The identifier of the template, for example \"alpine-shell\"."`
	// Version pins the version of the template. An empty version selects the
	// latest published one.
	Version string `json:"version,omitempty" jsonschema:"The published version to read. Leave it empty to read the latest version."`
}

// TemplateGetOutput carries one template.
type TemplateGetOutput struct {
	// Template is the requested template.
	Template TemplateView `json:"template"`
}

// addCapabilitiesTool registers the capabilities tool.
func (s *Server) addCapabilitiesTool(server *mcp.Server, actor policy.Actor) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "capabilities.get",
		Description: "Describe this node: the execution backends it offers, the published " +
			"templates, the scopes of the calling credential and the tools of the surface. " +
			"Call it first to learn what the node can do before creating an instance.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ CapabilitiesInput) (*mcp.CallToolResult, CapabilitiesOutput, error) {
		if failure := requireScope(actor, policy.ScopeCapabilitiesRead, correlationIDFrom(ctx)); failure != nil {
			return nil, CapabilitiesOutput{}, failure
		}
		return nil, s.capabilities(ctx, actor), nil
	})
}

// capabilities builds the discovery document of the node.
func (s *Server) capabilities(ctx context.Context, actor policy.Actor) CapabilitiesOutput {
	runtimes := make([]RuntimeCapability, 0)
	if s.registry != nil {
		for _, name := range s.registry.Names() {
			backend, ok := s.registry.Get(name)
			if !ok {
				continue
			}
			view := RuntimeCapability{
				Name:    backend.Name(),
				Kind:    string(backend.Kind()),
				Default: s.registry.Default == name,
				Healthy: true,
			}
			if err := backend.Health(ctx); err != nil {
				view.Healthy = false
				view.Error = "the backend is not available"
				// The detail stays in the log: it may name a socket or a
				// path of the host.
				s.log.Warn("a runtime backend is not healthy", "backend", name, "error", err)
			}
			runtimes = append(runtimes, view)
		}
	}

	catalogue := make([]TemplateView, 0)
	if s.orch != nil && s.orch.Catalogue() != nil {
		for _, tmpl := range s.orch.Catalogue().List() {
			catalogue = append(catalogue, viewTemplate(tmpl))
		}
	}

	scopes := make([]string, 0)
	for _, scope := range actor.Scopes.List() {
		scopes = append(scopes, string(scope))
	}

	return CapabilitiesOutput{
		Version:   s.version,
		Runtimes:  runtimes,
		Templates: catalogue,
		Scopes:    scopes,
		Tools:     append([]string{}, ToolNames...),
		Resources: append([]string{}, ResourceNames...),
	}
}

// addTemplateTools registers the template tools.
func (s *Server) addTemplateTools(server *mcp.Server, actor policy.Actor) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "template.list",
		Description: "List the published templates of this node with their version, image, " +
			"resource envelope, egress policy and default lifetime. A template is the " +
			"starting point of every instance.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ TemplateListInput) (*mcp.CallToolResult, TemplateListOutput, error) {
		if failure := requireScope(actor, policy.ScopeTemplatesRead, correlationIDFrom(ctx)); failure != nil {
			return nil, TemplateListOutput{}, failure
		}
		return nil, s.listTemplates(), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "template.get",
		Description: "Describe one version of a template, including the steps it runs, the " +
			"environment it provides and the documentation that explains it. Pin the " +
			"version to reproduce an old workload; an empty version selects the latest one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input TemplateGetInput) (*mcp.CallToolResult, TemplateGetOutput, error) {
		if failure := requireScope(actor, policy.ScopeTemplatesRead, correlationIDFrom(ctx)); failure != nil {
			return nil, TemplateGetOutput{}, failure
		}
		name := strings.TrimSpace(input.Template)
		if name == "" {
			return nil, TemplateGetOutput{}, invalidRequest("the template is required", correlationIDFrom(ctx))
		}
		if s.orch == nil || s.orch.Catalogue() == nil {
			return nil, TemplateGetOutput{}, &ToolError{
				Code:          CodeCapabilityUnavailable,
				Message:       "the node has no template catalogue",
				CorrelationID: correlationIDFrom(ctx),
			}
		}
		tmpl, ok := s.orch.Catalogue().Get(name, strings.TrimSpace(input.Version))
		if !ok {
			return nil, TemplateGetOutput{}, &ToolError{
				Code:          CodeNotFound,
				Message:       "the template does not exist",
				CorrelationID: correlationIDFrom(ctx),
				Details:       map[string]any{"template": name, "version": input.Version},
			}
		}
		return nil, TemplateGetOutput{Template: viewTemplate(tmpl)}, nil
	})
}

// listTemplates renders the catalogue of the node.
func (s *Server) listTemplates() TemplateListOutput {
	out := TemplateListOutput{Templates: make([]TemplateView, 0)}
	if s.orch == nil || s.orch.Catalogue() == nil {
		return out
	}
	for _, tmpl := range s.orch.Catalogue().List() {
		out.Templates = append(out.Templates, viewTemplate(tmpl))
	}
	return out
}
