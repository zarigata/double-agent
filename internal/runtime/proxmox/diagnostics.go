package proxmox

import (
	"context"
	"fmt"
	"strings"

	"github.com/zarigata/double-agent/internal/runtime"
)

// CheckTemplate verifies that the configured template can be cloned.
//
// The check is the one Prepare performs for a recipe without an image: the
// guest must exist on the node, be marked as a template and be part of the
// allow list of the operator. The backend never clones a guest that fails
// this check, so the doctor finds the problem before the first workload.
func (r *Runtime) CheckTemplate(ctx context.Context) error {
	return r.Prepare(ctx, runtime.Recipe{})
}

// CheckGuestAgent verifies the readiness of the guest agent channel.
//
// A vmid of zero inspects the configuration of the template, which says
// whether the agent channel is enabled but not whether the agent package is
// part of the image. A positive vmid runs a harmless command in a running,
// disposable guest of the operator's choice, which proves the whole channel:
// the agent answers, a command is executed and the guest names the
// virtualization it runs under.
func (r *Runtime) CheckGuestAgent(ctx context.Context, vmid int) (runtime.GuestAgentReport, error) {
	if vmid <= 0 {
		return r.checkTemplateAgent(ctx)
	}
	return r.probeGuest(ctx, vmid)
}

// checkTemplateAgent reads the agent setting of the configuration of the
// template.
//
// The backend enables the agent channel on every clone it creates, so the
// setting of the template is a hint, not a gate. The agent package inside the
// image cannot be examined through the API of a stopped guest.
func (r *Runtime) checkTemplateAgent(ctx context.Context) (runtime.GuestAgentReport, error) {
	config, err := r.client.GetConfig(ctx, r.templateVMID)
	if err != nil {
		return runtime.GuestAgentReport{}, fmt.Errorf("proxmox: read the configuration of template %d: %w", r.templateVMID, err)
	}
	report := runtime.GuestAgentReport{Configured: config.Agent.Enabled()}
	if report.Configured {
		report.Detail = fmt.Sprintf("template %d enables the guest agent; the qemu-guest-agent package inside the image can only be proven with a running guest (--proxmox-vmid)", r.templateVMID)
	} else {
		report.Detail = fmt.Sprintf("template %d does not set agent=1; the backend still enables the agent on every clone, and the qemu-guest-agent package inside the image can only be proven with a running guest (--proxmox-vmid)", r.templateVMID)
	}
	return report, nil
}

// probeGuest runs a command inside a running guest and reads the
// virtualization the guest reports.
//
// systemd-detect-virt answers "kvm" for a guest with hardware acceleration
// and "qemu" for a guest under software emulation. An image without systemd
// or a guest that cannot tell falls back to the hypervisor flag of the
// processor, which QEMU only presents to a guest that runs with KVM.
func (r *Runtime) probeGuest(ctx context.Context, vmid int) (runtime.GuestAgentReport, error) {
	report := runtime.GuestAgentReport{Answered: true}
	result, err := r.run(ctx, vmid, []string{"systemd-detect-virt"}, "")
	if err != nil {
		return runtime.GuestAgentReport{}, err
	}
	switch virt := strings.TrimSpace(string(result.Stdout)); {
	case result.ExitCode == 0 && (virt == "kvm" || virt == "qemu" || virt == "microsoft" || virt == "vmware" || virt == "xen" || virt == "hyper-v"):
		report.VirtType = virt
	case result.ExitCode == 0 && virt != "":
		// An accelerator this backend does not measure, e.g. "apple".
		report.VirtType = virt
	case result.ExitCode == 1 && virt == "", result.ExitCode == 127:
		// The guest could not tell or the utility is missing, so the
		// hypervisor flag of the processor is the fallback.
		return r.probeGuestByFlag(ctx, vmid)
	default:
		report.Detail = fmt.Sprintf("the guest agent answered, but systemd-detect-virt reported exit code %d (%s)", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
		return report, nil
	}
	report.Detail = describeVirtualization(report.VirtType, "systemd-detect-virt")
	return report, nil
}

// probeGuestByFlag reads the hypervisor flag of the processor of the guest.
//
// QEMU presents the flag only to a guest that runs with KVM: a guest under
// software emulation believes it runs on bare metal. The flag is a fallback,
// because a guest that a different hypervisor accelerated also carries it.
func (r *Runtime) probeGuestByFlag(ctx context.Context, vmid int) (runtime.GuestAgentReport, error) {
	result, err := r.run(ctx, vmid, []string{"grep", "-q", "hypervisor", "/proc/cpuinfo"}, "")
	if err != nil {
		return runtime.GuestAgentReport{}, err
	}
	report := runtime.GuestAgentReport{Answered: true}
	if result.ExitCode == 0 {
		report.VirtType = "kvm"
	} else {
		report.VirtType = "qemu"
	}
	report.Detail = describeVirtualization(report.VirtType, "the hypervisor flag of the processor")
	return report, nil
}

// describeVirtualization turns the virtualization a guest reported into the
// sentence the doctor prints.
func describeVirtualization(virt, source string) string {
	switch virt {
	case "kvm":
		return fmt.Sprintf("the guest runs with KVM hardware acceleration (%s)", source)
	case "qemu":
		return "the guest runs under QEMU software emulation without KVM: the host is unsuitable for performance, GPU, desktop and Windows work (" + source + ")"
	default:
		return fmt.Sprintf("the guest reports the virtualization %q (%s)", virt, source)
	}
}
