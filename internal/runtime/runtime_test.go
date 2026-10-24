package runtime_test

import (
	"testing"

	"github.com/zarigata/double-agent/internal/runtime"
	"github.com/zarigata/double-agent/internal/runtime/docker"
	"github.com/zarigata/double-agent/internal/runtime/local"
	"github.com/zarigata/double-agent/internal/runtime/proxmox"
)

// The backends have to satisfy the contract of the control plane. The
// assertions fail at compile time when a backend drifts from the interface.
var (
	_ runtime.Runtime = (*docker.Runtime)(nil)
	_ runtime.Runtime = (*local.Runtime)(nil)
	_ runtime.Runtime = (*proxmox.Runtime)(nil)
)

// TestBackendIdentity checks the identity every backend reports.
func TestBackendIdentity(t *testing.T) {
	cases := []struct {
		backend runtime.Runtime
		name    string
		kind    runtime.Kind
	}{
		{backend: &docker.Runtime{}, name: "docker", kind: runtime.KindContainer},
		{backend: &local.Runtime{}, name: "local", kind: runtime.KindContainer},
		{backend: &proxmox.Runtime{}, name: "proxmox", kind: runtime.KindVM},
	}
	for _, test := range cases {
		if test.backend.Name() != test.name {
			t.Errorf("name = %q, want %q", test.backend.Name(), test.name)
		}
		if test.backend.Kind() != test.kind {
			t.Errorf("kind = %q, want %q", test.backend.Kind(), test.kind)
		}
	}
}
