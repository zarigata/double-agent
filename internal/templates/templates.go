// Package templates implements the versioned template catalogue of the Double
// Agent control plane.
//
// A template is a declarative description of a workload: the image to run, the
// resources it may use, the network policy it runs under and the steps that
// are executed inside it. Templates are immutable once published: a change
// creates a new version, which keeps historic jobs reproducible.
package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zarigata/double-agent/internal/runtime"
)

// Spec is the declarative description of a template.
type Spec struct {
	// ID is the stable template identifier, e.g. "python-repl".
	ID string `yaml:"id" json:"id"`
	// Version is the semantic version of this template.
	Version string `yaml:"version" json:"version"`
	// Title is the human readable name.
	Title string `yaml:"title" json:"title"`
	// Description explains what the template does.
	Description string `yaml:"description" json:"description"`
	// Runtime selects the runtime family, "container" or "vm".
	Runtime runtime.Kind `yaml:"runtime" json:"runtime"`
	// Backend pins the runtime backend. Empty means the node default.
	Backend string `yaml:"backend" json:"backend,omitempty"`
	// Image is the container or virtual machine image reference.
	Image string `yaml:"image" json:"image"`
	// ImageDigest pins the image to an exact digest for reproducibility.
	ImageDigest string `yaml:"image_digest" json:"image_digest,omitempty"`
	// Resources is the resource envelope of the workload.
	Resources runtime.Resources `yaml:"resources" json:"resources"`
	// Network is the egress policy of the workload.
	Network runtime.NetworkPolicy `yaml:"network" json:"network"`
	// Environment lists the environment variables handed to the workload.
	Environment map[string]string `yaml:"environment" json:"environment,omitempty"`
	// Steps are the commands executed inside the workload.
	Steps []runtime.Step `yaml:"steps" json:"steps,omitempty"`
	// Capabilities are the optional capabilities the workload may request.
	Capabilities []string `yaml:"capabilities" json:"capabilities,omitempty"`
	// TTLSeconds is the default lifetime of an instance created from this
	// template.
	TTLSeconds int `yaml:"ttl_seconds" json:"ttl_seconds,omitempty"`
	// Labels are attached to every instance created from this template.
	Labels map[string]string `yaml:"labels" json:"labels,omitempty"`
	// Secrets lists the names of the secrets the workload may request. The
	// values are never stored in the template.
	Secrets []string `yaml:"secrets" json:"secrets,omitempty"`
	// Documentation is the markdown body shown in the template catalogue.
	Documentation string `yaml:"documentation" json:"documentation,omitempty"`
}

// Template is a published, immutable template version.
type Template struct {
	Spec Spec `json:"spec"`
	// Digest is the SHA-256 of the canonicalised spec.
	Digest string `json:"digest"`
}

// Validate checks a template specification.
func (s Spec) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("templates: id is required")
	}
	if !validIdentifier(s.ID) {
		return fmt.Errorf("templates: id %q must match [a-z0-9][a-z0-9-]*", s.ID)
	}
	if s.Version == "" {
		return fmt.Errorf("templates: version is required")
	}
	if s.Image == "" {
		return fmt.Errorf("templates: image is required")
	}
	if s.Runtime == "" {
		s.Runtime = runtime.KindContainer
	}
	if s.Runtime != runtime.KindContainer && s.Runtime != runtime.KindVM {
		return fmt.Errorf("templates: runtime %q is not supported", s.Runtime)
	}
	switch s.Network.Egress {
	case "", "none", "restricted", "allowed":
	default:
		return fmt.Errorf("templates: network egress %q is not supported", s.Network.Egress)
	}
	if s.Network.Egress == "restricted" && len(s.Network.Allowlist) == 0 && len(s.Network.AllowedDNS) == 0 {
		return fmt.Errorf("templates: restricted network needs an allowlist or allowed dns names")
	}
	if s.Resources.CPU <= 0 {
		return fmt.Errorf("templates: resources.cpu must be positive")
	}
	if s.Resources.MemoryMB <= 0 {
		return fmt.Errorf("templates: resources.memory_mb must be positive")
	}
	for i, step := range s.Steps {
		if len(step.Argv) == 0 {
			return fmt.Errorf("templates: step %d has no command", i)
		}
	}
	return nil
}

// validIdentifier reports whether the identifier is a safe slug.
func validIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Digest computes the stable digest of a specification.
func (s Spec) Digest() string {
	canonical, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// New builds a published template from a specification.
func New(spec Spec) (Template, error) {
	if err := spec.Validate(); err != nil {
		return Template{}, err
	}
	return Template{Spec: spec, Digest: spec.Digest()}, nil
}

// Catalogue holds the published templates of a node.
type Catalogue struct {
	templates map[string][]Template
}

// NewCatalogue creates an empty catalogue.
func NewCatalogue() *Catalogue {
	return &Catalogue{templates: map[string][]Template{}}
}

// Publish adds a template version to the catalogue.
func (c *Catalogue) Publish(t Template) error {
	if err := t.Spec.Validate(); err != nil {
		return err
	}
	versions := c.templates[t.Spec.ID]
	for _, existing := range versions {
		if existing.Spec.Version == t.Spec.Version {
			if existing.Digest != t.Digest {
				return fmt.Errorf("templates: version %s of %q already exists with a different digest",
					t.Spec.Version, t.Spec.ID)
			}
			return nil
		}
	}
	c.templates[t.Spec.ID] = append(versions, t)
	return nil
}

// Get returns a specific version of a template.
func (c *Catalogue) Get(id, version string) (Template, bool) {
	versions, ok := c.templates[id]
	if !ok {
		return Template{}, false
	}
	if version == "" {
		// The empty version selects the highest published version.
		best := versions[0]
		for _, v := range versions[1:] {
			if compareVersions(v.Spec.Version, best.Spec.Version) > 0 {
				best = v
			}
		}
		return best, true
	}
	for _, v := range versions {
		if v.Spec.Version == version {
			return v, true
		}
	}
	return Template{}, false
}

// List returns every published template, ordered by identifier and version.
func (c *Catalogue) List() []Template {
	out := make([]Template, 0, len(c.templates))
	for _, versions := range c.templates {
		out = append(out, versions...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Spec.ID != out[j].Spec.ID {
			return out[i].Spec.ID < out[j].Spec.ID
		}
		return compareVersions(out[i].Spec.Version, out[j].Spec.Version) < 0
	})
	return out
}

// IDs returns the identifiers of every published template.
func (c *Catalogue) IDs() []string {
	out := make([]string, 0, len(c.templates))
	for id := range c.templates {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// compareVersions compares two dotted version strings numerically.
func compareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = atoi(as[i])
		}
		if i < len(bs) {
			bv = atoi(bs[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a, b)
}

// atoi parses a leading integer, ignoring any pre-release suffix.
func atoi(s string) int {
	s = strings.SplitN(s, "-", 2)[0]
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// LoadDir reads every YAML template manifest in a directory.
func LoadDir(dir string) ([]Template, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("templates: read %s: %w", dir, err)
	}
	var out []Template
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("templates: read %s: %w", path, err)
		}
		t, err := Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("templates: %s: %w", path, err)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Spec.ID != out[j].Spec.ID {
			return out[i].Spec.ID < out[j].Spec.ID
		}
		return compareVersions(out[i].Spec.Version, out[j].Spec.Version) < 0
	})
	return out, nil
}

// Parse reads a template manifest.
func Parse(raw []byte) (Template, error) {
	var spec Spec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return Template{}, fmt.Errorf("templates: parse manifest: %w", err)
	}
	return New(spec)
}

// Builtin returns the templates that ship with the control plane.
func Builtin() []Template {
	specs := []Spec{
		{
			ID:            "alpine-shell",
			Version:       "1.0.0",
			Title:         "Alpine Shell",
			Description:   "A minimal Alpine container with an interactive shell and no network access.",
			Runtime:       runtime.KindContainer,
			Image:         "docker.io/library/alpine:3.20",
			Resources:     runtime.Resources{CPU: 1, MemoryMB: 512, DiskMB: 2048},
			Network:       runtime.NetworkPolicy{Egress: "none"},
			TTLSeconds:    3600,
			Labels:        map[string]string{"category": "utility"},
			Documentation: "Use for quick shell experiments. The instance has no network access.",
		},
		{
			ID:            "python-repl",
			Version:       "1.0.0",
			Title:         "Python REPL",
			Description:   "A Python 3.12 container prepared for interactive analysis.",
			Runtime:       runtime.KindContainer,
			Image:         "docker.io/library/python:3.12-slim",
			Resources:     runtime.Resources{CPU: 2, MemoryMB: 2048, DiskMB: 4096},
			Network:       runtime.NetworkPolicy{Egress: "none"},
			TTLSeconds:    3600,
			Labels:        map[string]string{"category": "language"},
			Documentation: "Use for Python experiments. Install packages with pip inside the instance.",
		},
		{
			ID:            "node-sandbox",
			Version:       "1.0.0",
			Title:         "Node.js Sandbox",
			Description:   "A Node.js 22 container with a writable workspace.",
			Runtime:       runtime.KindContainer,
			Image:         "docker.io/library/node:22-slim",
			Resources:     runtime.Resources{CPU: 2, MemoryMB: 2048, DiskMB: 4096},
			Network:       runtime.NetworkPolicy{Egress: "none"},
			TTLSeconds:    3600,
			Labels:        map[string]string{"category": "language"},
			Documentation: "Use for JavaScript experiments.",
		},
		{
			ID:            "ubuntu-workbench",
			Version:       "1.0.0",
			Title:         "Ubuntu Workbench",
			Description:   "A full Ubuntu image for longer running tasks that need a rich toolchain.",
			Runtime:       runtime.KindContainer,
			Image:         "docker.io/library/ubuntu:24.04",
			Resources:     runtime.Resources{CPU: 4, MemoryMB: 4096, DiskMB: 8192},
			Network:       runtime.NetworkPolicy{Egress: "none"},
			TTLSeconds:    7200,
			Labels:        map[string]string{"category": "workbench"},
			Documentation: "Use for multi-step tasks that need apt packages.",
		},
		{
			ID:            "proxmox-linux",
			Version:       "1.0.0",
			Title:         "Proxmox Linux Virtual Machine",
			Description:   "A disposable virtual machine cloned from a Proxmox VE template of the node.",
			Runtime:       runtime.KindVM,
			Backend:       "proxmox",
			Image:         "205",
			Resources:     runtime.Resources{CPU: 1, MemoryMB: 1024, DiskMB: 8192},
			// The Proxmox backend attaches guests to the host bridge, so the
			// network of the guest is governed by the host, not by an
			// allowlist of the control plane.
			Network:       runtime.NetworkPolicy{Egress: "allowed"},
			TTLSeconds:    3600,
			Labels:        map[string]string{"category": "virtual-machine"},
			Documentation: "Use for workloads that need a full kernel, a real filesystem or a network device of their own. The guest is a clone: the template of the node is never modified.",
		},
	}
	out := make([]Template, 0, len(specs))
	for _, spec := range specs {
		t, err := New(spec)
		if err != nil {
			// A malformed builtin is a programming error, not a user error.
			panic(fmt.Sprintf("templates: builtin template %s: %v", spec.ID, err))
		}
		out = append(out, t)
	}
	return out
}

// DefaultTTL returns the default lifetime of a template.
func (s Spec) DefaultTTL() time.Duration {
	if s.TTLSeconds <= 0 {
		return time.Hour
	}
	return time.Duration(s.TTLSeconds) * time.Second
}
