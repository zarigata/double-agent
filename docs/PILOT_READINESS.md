# Pilot readiness: current implementation and next actions

## What exists now

The repository is no longer only a specification. Commit `106a9c8` adds a Go
control plane with SQLite state, authentication and scoped keys, a Web/API
surface, MCP Streamable HTTP, artifact upload/download, TTL cleanup, Docker,
and a Proxmox VM runtime. The Proxmox backend clones a configured template,
marks every clone `double-agent`, and refuses to delete guests without that
marker.

Local validation now exists but is **not a clean-host proof**: on 2026-10-22
the full Go test suite, the Markdown checks and the locale checks pass on a
development Mac, and the Docker lifecycle — create, exec, upload, export,
destroy, TTL cleanup, and reconciliation after a restart — was demonstrated
twice in a row there (see the [validation report](VALIDATION_REPORT.md) for
commands and output). The Proxmox lifecycle ran twice on the target host the
same day with the same outcome. A clean, supported Linux host has not run it
yet.

The configuration now fails fast: an enabled Proxmox runtime without complete
credentials, endpoint, node, storage, or an allow list that includes the
template is refused at startup, and `double-agentd doctor` checks the
configured template and the guest agent, including whether a guest runs with
KVM or under QEMU software emulation (`doctor --proxmox-vmid`). Jobs a restart
interrupts are marked failed and instance states are aligned with their
backend at startup.

## Recommended first pilot

Keep the first pilot narrow: one trusted LAN host, CPU-only Docker workloads,
one administrative user, and no Internet exposure. Validate the complete
create → execute → export → destroy path before enabling GPU, desktops,
delegation, Windows, or untrusted users.

Required evidence for the pilot:

1. Install the exact Go version declared in `go.mod` and run `go test ./...`.
2. Build the daemon and run `double-agentd doctor` against a temporary data
   directory; record its output without secrets.
3. Start the daemon with the Docker runtime and run
   `scripts/validate-node.sh` against its TLS endpoint.
4. Preserve the successful command output, daemon version, host OS/kernel,
   Docker version, and test date with the release candidate.

The installer, systemd unit, signed release flow, automatic certificate setup,
and a certified external MCP client remain release work; do not represent them
as available today.

## Proxmox testing plan

The configured endpoint is `https://192.168.1.79:8006`, node `pve`, storage
`local-lvm`, bridge `vmbr0`, and pilot template VMID `301`. VM `301` is the
immutable Debian 13 cloud-image template; VM `302` is its disposable smoke
clone. Configuration is not proof that the guest is ready: confirm the QEMU
guest agent before enabling it through the control plane.

The complete step-by-step plan, its automated script and its cleanup policy
live in [Proxmox integration test plan](PROXMOX_TEST_PLAN.md). The essentials
the template must provide:

1. VM `301` is a Proxmox template, is in the allow-list, boots with the
   chosen bridge, and has enough storage for a **full clone**.
2. `qemu-guest-agent` is installed and enabled in the template. The runtime
   requires it for command execution and file transfer, and the doctor
   command verifies it against a running disposable guest.
3. A DHCP or otherwise documented network configuration. A clone currently
   attaches to `vmbr0`; it is not network-isolated by the control plane.
4. A least-privilege Proxmox API token over `root@pam`, with its secret only
   in `DOUBLE_AGENT_PROXMOX_TOKEN_SECRET`, and certificate validation after
   installing the Proxmox CA. `insecure: true` is suitable only for a
   short, trusted-LAN bootstrap test.
5. One disposable test clone through Double Agent, `uname -a` through the
   guest agent, a small artifact export, destruction, and confirmation that
   the disks are gone — twice.

## Cleanup policy

Do not bulk-delete existing Proxmox guests. A guest may be deleted by this
project only when its name begins with `da-` **or** its description contains
`double-agent`, its role and owner are confirmed, and it is not the configured
template. This mirrors the runtime's deletion safeguard. Any unmarked,
ambiguous, running, or manually-created guest requires an operator decision.

## Priority gaps

| Priority | Gap | Why it matters | Next action |
|---|---|---|---|
| P0 | No clean-host Linux proof | The recorded Docker evidence comes from a development Mac | Run the same lifecycle on a clean supported Linux host |
| P0 | Proxmox run used the bootstrap credential | `root@pam` with `insecure: true` is a trusted-LAN bootstrap posture | Create a least-privilege API token, install the Proxmox CA, turn off `insecure` |
| P0 | VM network attaches to `vmbr0` | A test clone can reach the LAN | Use a dedicated isolated bridge/VLAN before untrusted work |
| P1 | Release installer is documented but absent | Users may mistake a design for a command | Implement and test a signed installer or label it as planned |
| P1 | No CI evidence on the remote | The workflow exists but has not run on GitHub yet | Push and keep the first green run |
| P1 | Docs previously called implemented tools “planned” | Agents need an accurate contract | Keep tool status synchronized with `internal/mcpsrv/server.go` |

Proxmox status after the 2026-10-22 run: the full lifecycle (clone, exec
through the guest agent, artifact export, destroy) was executed twice on the
target host, the doctor checks answered correctly — including the KVM vs
QEMU software-emulation distinction — and the node was left with only its
templates. A Debian-13 genericcloud clone on this KVM-less host needs roughly
18–25 minutes of first boot before its guest agent answers; the runtime now
waits for the agent (bounded) instead of failing, and the test script retries
through that window. Baking `qemu-guest-agent` into the template image (the
genericcloud image relies on the cloud-init snippet) and adding a virtio-rng
device would cut that window and are the recommended template hardening.

## Definition of “ready for the next phase”

Do not advance from the pilot until the CPU lifecycle is repeatable twice on a
clean host, automatic cleanup leaves no Docker/Proxmox resources behind, MCP
authentication and scope denial are demonstrated, and the evidence above is
checked into or attached to the release. GPU and VM support then require their
own separate proof; a successful Docker test does not validate Proxmox.
