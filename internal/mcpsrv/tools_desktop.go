package mcpsrv

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/policy"
)

// desktopCapability is the capability a template declares when it offers a
// graphical desktop.
const desktopCapability = "desktop"

// DesktopOpenInput selects the instance whose desktop is opened.
type DesktopOpenInput struct {
	// InstanceID identifies the instance.
	InstanceID string `json:"instance_id" jsonschema:"The identifier of the instance, for example \"ins_01J...\"."`
}

// DesktopOpenOutput describes a desktop session.
type DesktopOpenOutput struct {
	// SessionID identifies the desktop session.
	SessionID string `json:"session_id"`
	// URL is the endpoint that serves the session.
	URL string `json:"url"`
	// Kind is the kind of the session, for example "vnc".
	Kind string `json:"kind"`
}

// DesktopCloseInput selects the desktop session that is closed.
type DesktopCloseInput struct {
	// InstanceID identifies the instance.
	InstanceID string `json:"instance_id" jsonschema:"The identifier of the instance, for example \"ins_01J...\"."`
	// SessionID identifies the desktop session. It is optional: a client
	// that never opened a session may still ask the node to close one.
	SessionID string `json:"session_id,omitempty" jsonschema:"The identifier of the desktop session. Leave it empty to close the session of the instance."`
}

// DesktopCloseOutput describes the closed session.
type DesktopCloseOutput struct {
	// SessionID identifies the desktop session.
	SessionID string `json:"session_id"`
	// State is the state the session entered.
	State string `json:"state"`
}

// addDesktopTools registers the tools of the desktops.
//
// The tools exist even though the node runs CPU only in this release: a
// client that asks for a desktop receives the capability_unavailable code
// and an explanation, instead of a tool that does not exist. The contract
// of the surface therefore stays stable for the phases that add the
// desktop.
func (s *Server) addDesktopTools(server *mcp.Server, actor policy.Actor) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop.open",
		Description: "Open the graphical desktop of an instance. The node answers with " +
			"the capability_unavailable code when the template of the instance does not " +
			"offer a desktop, which is the case for every template of this release.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DesktopOpenInput) (*mcp.CallToolResult, DesktopOpenOutput, error) {
		if failure := requireScope(actor, policy.ScopeInstancesRead, correlationIDFrom(ctx)); failure != nil {
			return nil, DesktopOpenOutput{}, failure
		}
		out, err := s.openDesktop(ctx, actor, input)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "desktop.close",
		Description: "Close the graphical desktop of an instance. The node answers with " +
			"the capability_unavailable code when the template of the instance does not " +
			"offer a desktop, which is the case for every template of this release.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DesktopCloseInput) (*mcp.CallToolResult, DesktopCloseOutput, error) {
		if failure := requireScope(actor, policy.ScopeInstancesRead, correlationIDFrom(ctx)); failure != nil {
			return nil, DesktopCloseOutput{}, failure
		}
		out, err := s.closeDesktop(ctx, actor, input)
		return nil, out, err
	})
}

// openDesktop opens the desktop of an instance.
func (s *Server) openDesktop(ctx context.Context, actor policy.Actor, input DesktopOpenInput) (DesktopOpenOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.InstanceID)
	if id == "" {
		return DesktopOpenOutput{}, invalidRequest("the instance identifier is required", correlationID)
	}
	template, offered, err := s.desktopTemplate(ctx, actor, id)
	if err != nil {
		return DesktopOpenOutput{}, err
	}
	if !offered {
		return DesktopOpenOutput{}, &ToolError{
			Code: CodeCapabilityUnavailable,
			Message: "the template " + template + " does not declare the desktop capability; " +
				"this node runs command line workloads only. Run the workload with " +
				"instance.exec and collect its output with artifact.export.",
			CorrelationID: correlationID,
			Details:       map[string]any{"instance_id": id, "template": template, "capability": desktopCapability},
		}
	}
	return DesktopOpenOutput{}, &ToolError{
		Code: CodeCapabilityUnavailable,
		Message: "the template " + template + " declares the desktop capability, but this " +
			"node does not serve desktop sessions yet. Run the workload with instance.exec " +
			"and collect its output with artifact.export.",
		CorrelationID: correlationID,
		Details:       map[string]any{"instance_id": id, "template": template, "capability": desktopCapability},
	}
}

// closeDesktop closes the desktop of an instance.
func (s *Server) closeDesktop(ctx context.Context, actor policy.Actor, input DesktopCloseInput) (DesktopCloseOutput, error) {
	correlationID := correlationIDFrom(ctx)
	id := strings.TrimSpace(input.InstanceID)
	if id == "" {
		return DesktopCloseOutput{}, invalidRequest("the instance identifier is required", correlationID)
	}
	template, offered, err := s.desktopTemplate(ctx, actor, id)
	if err != nil {
		return DesktopCloseOutput{}, err
	}
	if !offered {
		return DesktopCloseOutput{}, &ToolError{
			Code: CodeCapabilityUnavailable,
			Message: "the template " + template + " does not declare the desktop capability, " +
				"so no desktop session was ever opened for the instance. Destroy the " +
				"instance with instance.destroy to release its resources.",
			CorrelationID: correlationID,
			Details:       map[string]any{"instance_id": id, "template": template, "capability": desktopCapability},
		}
	}
	return DesktopCloseOutput{}, &ToolError{
		Code: CodeCapabilityUnavailable,
		Message: "the template " + template + " declares the desktop capability, but this " +
			"node does not serve desktop sessions yet, so no session was ever opened. " +
			"Destroy the instance with instance.destroy to release its resources.",
		CorrelationID: correlationID,
		Details:       map[string]any{"instance_id": id, "template": template, "capability": desktopCapability},
	}
}

// desktopTemplate checks that an instance exists, that the caller owns it
// and whether its template offers a desktop.
//
// The check is honest: the node refuses the request when the template does
// not declare the desktop capability, instead of advertising a session it
// cannot serve.
func (s *Server) desktopTemplate(ctx context.Context, actor policy.Actor, instanceID string) (string, bool, error) {
	correlationID := correlationIDFrom(ctx)
	instance, err := s.store.InstanceByID(ctx, instanceID)
	if err != nil {
		return "", false, classify(err, correlationID)
	}
	if !ownsResource(actor, instance.UserID) {
		return "", false, permissionDenied(
			"the instance belongs to another account",
			correlationID,
			map[string]any{"instance_id": instanceID},
		)
	}
	template := strings.TrimSpace(instance.TemplateID)
	if s.orch == nil || s.orch.Catalogue() == nil {
		return template, false, nil
	}
	spec, ok := s.orch.Catalogue().Get(template, "")
	if !ok {
		return template, false, nil
	}
	for _, capability := range spec.Spec.Capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), desktopCapability) {
			return template, true, nil
		}
	}
	return template, false, nil
}
