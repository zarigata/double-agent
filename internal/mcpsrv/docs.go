package mcpsrv

import (
	"sort"
	"strings"
)

// docSection is one section of the documentation of the node.
//
// The documentation is written in English: English is the canonical language
// of the product, and the schemas, the identifiers and the error codes stay
// in English whatever the locale of the operator.
type docSection struct {
	// URI is the resource that serves the section.
	URI string `json:"uri"`
	// Title is the name of the section.
	Title string `json:"title"`
	// Body is the prose of the section.
	Body string `json:"body"`
}

// builtinDocs is the documentation every node carries.
//
// The corpus is small on purpose: it explains the contract of the surface,
// which is the part an agent needs before it creates an instance. The
// documentation of the templates is added to the corpus by the node, so a
// search also finds the prose of the template that is published.
var builtinDocs = []docSection{
	{
		URI:   "double-agent://docs/quickstart",
		Title: "Create an instance",
		Body: "Call instance.create with the identifier of a template, for example dev-linux, " +
			"and the lifetime the workload needs. The node answers with the identifier of the " +
			"job and the identifier of the instance. The job prepares the instance in the " +
			"background: read the job with job.get until its state is ready, and read the " +
			"instance with instance.get to see the resources it received.",
	},
	{
		URI:   "double-agent://docs/quickstart",
		Title: "Run a command",
		Body: "Call instance.exec with the identifier of the instance and the argument vector " +
			"of the command, for example [\"npm\", \"test\"]. The arguments are an array and " +
			"are never handed to a shell, so a command can never smuggle a second command " +
			"into the guest. The answer carries the exit code, the standard output and the " +
			"standard error of the run.",
	},
	{
		URI:   "double-agent://docs/quickstart",
		Title: "Collect the results",
		Body: "Call artifact.list to see the files the jobs of the account produced, and " +
			"artifact.download to get the link that serves the content of one artifact. The " +
			"link carries a token that may be redeemed once and that expires shortly, so a " +
			"leaked link reveals nothing. A large file is never embedded in a tool answer.",
	},
	{
		URI:   "double-agent://docs/quickstart",
		Title: "Send a file to the node",
		Body: "Call file.upload.begin with the name of the file and its exact size. The node " +
			"answers with an opaque handle and the endpoint that accepts the bytes. Send the " +
			"raw content of the file to that endpoint with a PUT request, then call " +
			"file.upload.complete with the sha256 of the file. The node hashes the bytes it " +
			"received and refuses a file that does not match its digest, so a truncated " +
			"transfer never reaches the artifact store.",
	},
	{
		URI:   "double-agent://docs/quickstart",
		Title: "Export the files of a workload",
		Body: "Call artifact.export with the identifier of an instance and the paths of the " +
			"files the workload produced. The node copies the files out of the instance, " +
			"hashes them and records them as artifacts. The copy lands outside of the " +
			"workspace of the guest, so the workload cannot read it back.",
	},
	{
		URI:   "double-agent://docs/security-model",
		Title: "The boundary of an instance",
		Body: "A workload may be root inside its own instance. It never receives the runtime " +
			"socket of the host, the file system of the host, the process table of the host, " +
			"the devices that were not assigned to it, the raw secrets of the node or the " +
			"instances and the artifacts of another account.",
	},
	{
		URI:   "double-agent://docs/security-model",
		Title: "Credentials of a client",
		Body: "An MCP client presents a scoped and revocable key as a bearer token. The key " +
			"never inherits the power of a browser session: the administrative scopes are " +
			"dropped when the credential is accepted, and no tool of the surface administers " +
			"the accounts or the keys of the node.",
	},
	{
		URI:   "double-agent://docs/security-model",
		Title: "Secrets",
		Body: "A secret is an opaque reference such as secret/npm-read-token. The node " +
			"resolves the reference inside the guest. A tool never returns a secret value and " +
			"never writes one into a log or into an answer.",
	},
}

// builtinErrorDocs documents the failure vocabulary of the surface.
var builtinErrorDocs = []docSection{
	{
		URI:   "double-agent://docs/errors",
		Title: "The error codes",
		Body: "Every failure carries a stable code: authentication_failed, permission_denied, " +
			"quota_exceeded, capability_unavailable, runtime_error, job_timeout, " +
			"artifact_expired and human_action_required. A code never changes meaning, so a " +
			"client may rely on it.",
	},
	{
		URI:   "double-agent://docs/errors",
		Title: "The shape of a failure",
		Body: "A failure carries the code, a message in English, the correlation identifier of " +
			"the call and a retry hint. The details of a failure never name a path of the " +
			"host and never carry a secret.",
	},
	{
		URI:   "double-agent://docs/errors",
		Title: "Retrying a call",
		Body: "A call that failed with quota_exceeded or with runtime_error may be repeated. A " +
			"call that failed with permission_denied needs a credential that carries more " +
			"scopes. A call that failed with authentication_failed needs a new credential. " +
			"Creation calls accept an idempotency key, so a retry never starts a second " +
			"workload.",
	},
}

// documentationCorpus builds the corpus of the node.
//
// The documentation of every published template joins the corpus, so a search
// also finds the prose that describes a template.
func (s *Server) documentationCorpus() []docSection {
	corpus := make([]docSection, 0, len(builtinDocs)+len(builtinErrorDocs))
	corpus = append(corpus, builtinDocs...)
	corpus = append(corpus, builtinErrorDocs...)
	if s.orch != nil && s.orch.Catalogue() != nil {
		for _, tmpl := range s.orch.Catalogue().List() {
			if strings.TrimSpace(tmpl.Spec.Documentation) == "" {
				continue
			}
			corpus = append(corpus, docSection{
				URI:   "double-agent://templates/" + tmpl.Spec.ID,
				Title: tmpl.Spec.Title,
				Body:  tmpl.Spec.Documentation,
			})
		}
	}
	return corpus
}

// searchDocs ranks the sections of the corpus against a query.
//
// The ranking is deliberately simple: the title of a section weighs more than
// its body. The corpus is small enough that a full text index would cost
// more than it saves.
func searchDocs(corpus []docSection, query string, limit int) []docSection {
	terms := docTerms(query)
	results := make([]docSection, 0, len(corpus))
	for _, section := range corpus {
		if scoreSection(section, terms) <= 0 {
			continue
		}
		results = append(results, section)
	}
	sort.SliceStable(results, func(i, j int) bool {
		left, right := scoreSection(results[i], terms), scoreSection(results[j], terms)
		if left != right {
			return left > right
		}
		return results[i].URI < results[j].URI
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

// scoreSection weighs a section against the terms of a query.
func scoreSection(section docSection, terms []string) int {
	if len(terms) == 0 {
		return 1
	}
	title := strings.ToLower(section.Title)
	body := strings.ToLower(section.Body)
	score := 0
	for _, term := range terms {
		switch {
		case strings.Contains(title, term):
			score += 4
		case strings.Contains(body, term):
			score++
		}
	}
	return score
}

// docTerms splits a query into the terms that are searched for.
func docTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == ';' || r == '.'
	})
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) < 2 {
			continue
		}
		terms = append(terms, field)
	}
	return terms
}

// excerpt renders the beginning of a section, cut at a word boundary.
func excerpt(section docSection, limit int) string {
	text := strings.Join(strings.Fields(section.Body), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := text[:limit]
	if index := strings.LastIndex(cut, " "); index > 0 {
		cut = cut[:index]
	}
	return cut + "..."
}
