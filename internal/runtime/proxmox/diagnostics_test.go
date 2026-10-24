package proxmox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/runtime"
)

// TestCheckTemplateVerifiesTheConfiguredTemplate checks that the doctor finds
// a template that is missing, not a template or outside the allow list.
func TestCheckTemplateVerifiesTheConfiguredTemplate(t *testing.T) {
	fake := newFakeProxmox()
	backend := newTestRuntime(t, fake, nil)
	ctx := context.Background()
	if err := backend.CheckTemplate(ctx); err != nil {
		t.Fatalf("check of a healthy template: %v", err)
	}
	fake.guests[200].Template = false
	if err := backend.CheckTemplate(ctx); err == nil {
		t.Fatal("a guest that is not a template passed the check")
	}
	fake.guests[200].Template = true
	delete(fake.guests, 200)
	if err := backend.CheckTemplate(ctx); err == nil {
		t.Fatal("a missing template passed the check")
	}
}

// TestCheckTemplateRefusesADisallowedTemplate checks that the allow list of
// the operator is honoured by the doctor as well.
func TestCheckTemplateRefusesADisallowedTemplate(t *testing.T) {
	fake := newFakeProxmox()
	backend := newTestRuntime(t, fake, func(cfg *Config) {
		cfg.AllowedTemplates = []int{201}
	})
	if err := backend.CheckTemplate(context.Background()); err == nil {
		t.Fatal("a template outside the allow list passed the check")
	}
}

// TestCheckGuestAgentInspectsTheTemplate checks that a stopped template is
// reported as a configuration-only answer that names what is unproven.
func TestCheckGuestAgentInspectsTheTemplate(t *testing.T) {
	fake := newFakeProxmox()
	fake.guests[200].Config["agent"] = "1"
	backend := newTestRuntime(t, fake, nil)
	report, err := backend.CheckGuestAgent(context.Background(), 0)
	if err != nil {
		t.Fatalf("check of the template: %v", err)
	}
	if !report.Configured {
		t.Error("the agent setting of the template was not reported")
	}
	if report.Answered {
		t.Error("a stopped template answered a command")
	}
	if report.VirtType != "" {
		t.Errorf("virt type = %q, want none", report.VirtType)
	}
	if !strings.Contains(report.Detail, "running guest") {
		t.Errorf("detail %q does not name what is unproven", report.Detail)
	}
}

// TestCheckGuestAgentProbesARunningGuest checks that the probe tells KVM
// hardware acceleration from QEMU software emulation.
func TestCheckGuestAgentProbesARunningGuest(t *testing.T) {
	cases := []struct {
		name             string
		out              string
		code             int
		wantVirt         string
		wantAccelerated  bool
		wantDetailNeedle string
	}{
		{name: "kvm", out: "kvm\n", code: 0, wantVirt: "kvm", wantAccelerated: true, wantDetailNeedle: "KVM hardware acceleration"},
		{name: "qemu", out: "qemu\n", code: 0, wantVirt: "qemu", wantAccelerated: false, wantDetailNeedle: "software emulation"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeProxmox()
			fake.execAnswers = map[string]fakeExecAnswer{
				"systemd-detect-virt": {out: test.out, code: test.code},
			}
			backend := newTestRuntime(t, fake, nil)
			report, err := backend.CheckGuestAgent(context.Background(), 200)
			if err != nil {
				t.Fatalf("probe of a running guest: %v", err)
			}
			if !report.Answered {
				t.Fatal("the guest agent did not answer")
			}
			if report.VirtType != test.wantVirt {
				t.Errorf("virt type = %q, want %q", report.VirtType, test.wantVirt)
			}
			if report.HardwareAccelerated() != test.wantAccelerated {
				t.Errorf("hardware accelerated = %v, want %v", report.HardwareAccelerated(), test.wantAccelerated)
			}
			if !strings.Contains(report.Detail, test.wantDetailNeedle) {
				t.Errorf("detail %q does not mention %q", report.Detail, test.wantDetailNeedle)
			}
		})
	}
}

// TestCheckGuestAgentFallsBackToTheProcessorFlag checks that a guest without
// systemd is measured through the hypervisor flag of its processor.
func TestCheckGuestAgentFallsBackToTheProcessorFlag(t *testing.T) {
	fake := newFakeProxmox()
	fake.execAnswers = map[string]fakeExecAnswer{
		// The utility is missing from the image.
		"systemd-detect-virt": {out: "", code: 127},
		"grep":                {out: "", code: 0},
	}
	backend := newTestRuntime(t, fake, nil)
	report, err := backend.CheckGuestAgent(context.Background(), 200)
	if err != nil {
		t.Fatalf("probe of a running guest: %v", err)
	}
	if report.VirtType != "kvm" {
		t.Errorf("virt type = %q, want kvm from the processor flag", report.VirtType)
	}
	if !strings.Contains(report.Detail, "hypervisor flag") {
		t.Errorf("detail %q does not name the fallback", report.Detail)
	}
}

// TestCheckGuestAgentFallsBackWithoutTheFlag checks that a guest under
// software emulation is recognised when the utility is missing and the
// processor reports no hypervisor.
func TestCheckGuestAgentFallsBackWithoutTheFlag(t *testing.T) {
	fake := newFakeProxmox()
	fake.execAnswers = map[string]fakeExecAnswer{
		"systemd-detect-virt": {out: "", code: 127},
		"grep":                {out: "", code: 1},
	}
	backend := newTestRuntime(t, fake, nil)
	report, err := backend.CheckGuestAgent(context.Background(), 200)
	if err != nil {
		t.Fatalf("probe of a running guest: %v", err)
	}
	if report.VirtType != "qemu" || report.HardwareAccelerated() {
		t.Errorf("virt type = %q, want qemu without acceleration", report.VirtType)
	}
}

// TestCheckGuestAgentReportsAMissingAgent checks that a guest without the
// agent produces the error that names the missing package.
func TestCheckGuestAgentReportsAMissingAgent(t *testing.T) {
	fake := newFakeProxmox()
	fake.agentAvailable = false
	// The wait for the agent is short, because the agent of this fake never
	// answers.
	backend := newTestRuntime(t, fake, func(cfg *Config) {
		cfg.AgentReadyTimeout = 50 * time.Millisecond
	})
	_, err := backend.CheckGuestAgent(context.Background(), 200)
	if err == nil {
		t.Fatal("a guest without the agent passed the probe")
	}
	if !errors.Is(err, ErrGuestAgentUnavailable) {
		t.Errorf("error %q does not report the missing guest agent", err)
	}
}

// TestRuntimeImplementsTheDoctorChecks checks that the backend offers the
// diagnostics surface the doctor command asks for.
func TestRuntimeImplementsTheDoctorChecks(t *testing.T) {
	var backend interface{} = newTestRuntime(t, newFakeProxmox(), nil)
	if _, ok := backend.(runtime.DoctorChecks); !ok {
		t.Error("the proxmox backend does not implement runtime.DoctorChecks")
	}
}
