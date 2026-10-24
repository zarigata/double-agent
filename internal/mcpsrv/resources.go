package mcpsrv

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zarigata/double-agent/internal/policy"
)

// The URIs of the resources of the node.
const (
	// resourceCapabilities is the discovery document of the node.
	resourceCapabilities = "double-agent://capabilities/current"
	// resourceTemplates is the prefix of the template resources.
	resourceTemplates = "double-agent://templates/"
	// resourceJobs is the prefix of the job resources.
	resourceJobs = "double-agent://jobs/"
	// resourceArtifacts is the prefix of the artifact resources.
	resourceArtifacts = "double-agent://artifacts/"
)

// addResources registers the agent-readable resources of the node.
//
// The resources carry the prose of the documentation and the metadata of the
// templates, the jobs and the artifacts. They are the wiki the product
// promises: a client may read them without calling a tool.
func (s *Server) addResources(server *mcp.Server, actor policy.Actor) {
	for _, uri := range []string{
		"double-agent://docs/quickstart",
		"double-agent://docs/security-model",
		"double-agent://docs/errors",
	} {
		server.AddResource(&mcp.Resource{
			URI:         uri,
			Name:        uri,
			Description: "The documentation of the Double Agent control plane.",
			MIMEType:    "text/markdown",
		}, s.docResourceHandler(uri))
	}

	server.AddResource(&mcp.Resource{
		URI:         resourceCapabilities,
		Name:        "Capabilities of this node",
		Description: "The discovery document of the node: the backends, the templates and the tools.",
		MIMEType:    "application/json",
	}, s.capabilitiesResourceHandler(actor))

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: resourceTemplates + "{template_id}",
		Name:        "Template",
		Description: "The specification of one published template.",
		MIMEType:    "application/json",
	}, s.templateResourceHandler(actor))

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: resourceJobs + "{job_id}",
		Name:        "Job",
		Description: "The state of one job.",
		MIMEType:    "application/json",
	}, s.jobResourceHandler(actor))

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: resourceArtifacts + "{artifact_id}/metadata",
		Name:        "Artifact metadata",
		Description: "The metadata of one artifact.",
		MIMEType:    "application/json",
	}, s.artifactResourceHandler(actor))
}

// docResourceHandler serves one page of the documentation.
func (s *Server) docResourceHandler(uri string) mcp.ResourceHandler {
	return func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		var page strings.Builder
		for _, section := range s.documentationCorpus() {
			if section.URI != uri {
				continue
			}
			page.WriteString("## ")
			page.WriteString(section.Title)
			page.WriteString("\n\n")
			page.WriteString(section.Body)
			page.WriteString("\n\n")
		}
		if page.Len() == 0 {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
			{URI: uri, MIMEType: "text/markdown", Text: page.String()},
		}}, nil
	}
}

// capabilitiesResourceHandler serves the discovery document of the node.
func (s *Server) capabilitiesResourceHandler(actor policy.Actor) mcp.ResourceHandler {
	return func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		encoded, err := json.MarshalIndent(s.capabilities(ctx, actor), "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
			{URI: resourceCapabilities, MIMEType: "application/json", Text: string(encoded)},
		}}, nil
	}
}

// templateResourceHandler serves the specification of a template.
func (s *Server) templateResourceHandler(actor policy.Actor) mcp.ResourceHandler {
	return func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		name := strings.TrimPrefix(uri, resourceTemplates)
		if name == "" || name == uri {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		if s.orch == nil || s.orch.Catalogue() == nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		tmpl, ok := s.orch.Catalogue().Get(name, "")
		if !ok {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		encoded, err := json.MarshalIndent(viewTemplate(tmpl), "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
			{URI: uri, MIMEType: "application/json", Text: string(encoded)},
		}}, nil
	}
}

// jobResourceHandler serves the state of a job.
func (s *Server) jobResourceHandler(actor policy.Actor) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		id := strings.TrimPrefix(uri, resourceJobs)
		if id == "" || id == uri {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		job, err := s.store.JobByID(ctx, id)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		if !ownsResource(actor, job.UserID) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		encoded, err := json.MarshalIndent(viewJob(job), "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
			{URI: uri, MIMEType: "application/json", Text: string(encoded)},
		}}, nil
	}
}

// artifactResourceHandler serves the metadata of an artifact.
func (s *Server) artifactResourceHandler(actor policy.Actor) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		rest := strings.TrimPrefix(uri, resourceArtifacts)
		if rest == uri || rest == "" {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		id := strings.TrimSuffix(rest, "/metadata")
		record, err := s.store.ArtifactByID(ctx, id)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		if !ownsResource(actor, record.UserID) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		encoded, err := json.MarshalIndent(viewArtifact(record), "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
			{URI: uri, MIMEType: "application/json", Text: string(encoded)},
		}}, nil
	}
}
