package runtime

import (
	"fmt"
	"sort"
)

// Registry holds the runtime backends available on this node, keyed by name.
type Registry struct {
	backends map[string]Runtime
	// Default is the backend used when a template does not pin one.
	Default string
}

// NewRegistry creates an empty runtime registry.
func NewRegistry() *Registry {
	return &Registry{backends: map[string]Runtime{}}
}

// Register adds a backend, replacing any previous backend with the same name.
func (r *Registry) Register(rt Runtime) {
	if r.backends == nil {
		r.backends = map[string]Runtime{}
	}
	r.backends[rt.Name()] = rt
}

// Get returns the backend registered under name.
func (r *Registry) Get(name string) (Runtime, bool) {
	rt, ok := r.backends[name]
	return rt, ok
}

// DefaultBackend returns the default backend.
func (r *Registry) DefaultBackend() (Runtime, bool) {
	if r.Default == "" {
		return nil, false
	}
	return r.Get(r.Default)
}

// Names lists the registered backend names in a stable order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.backends))
	for name := range r.backends {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ErrUnknownBackend is returned when a template references an unknown backend.
//
// The value receiver keeps both the value and the pointer form usable with
// errors.Is and errors.As.
type ErrUnknownBackend struct{ Name string }

func (e ErrUnknownBackend) Error() string {
	return fmt.Sprintf("runtime: unknown backend %q", e.Name)
}
