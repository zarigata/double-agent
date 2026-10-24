# Build plan

## Goal

Deliver a safe, evidence-backed MCP control plane that lets authorized AI
agents create disposable workloads, execute commands, and return artifacts.
Build capabilities in dependency order; do not advertise a phase until its
acceptance evidence is saved.

## Current pilot baseline

| Item | State |
|---|---|
| Control plane | Implemented in Go; `go test ./...`, docs and locale checks pass locally (see [validation report](VALIDATION_REPORT.md)) |
| MCP | Core instance, job, upload, artifact, discovery, and documentation tools implemented |
| Docker | Implemented; lifecycle demonstrated end to end on a development machine, twice in a row, including TTL cleanup and restart reconciliation |
| Proxmox target | `pve` at `192.168.1.79`, Proxmox VE 9.2.21 |
| VM template | `301` — `da-pilot-debian-template`, Debian 13 genericcloud, immutable, `kvm: 0` |
| Proxmox runtime | Lifecycle validated on the target host on 2026-10-22: clone, guest-agent exec, artifact export and destroy, twice; doctor checks and KVM/QEMU detection answered correctly |
| Proxmox smoke | The earlier smoke guest `302` no longer exists; live guest-agent checks now target any running `da-` guest |
| Virtualization | KVM is unavailable on this host; the template itself sets `kvm: 0`, and the doctor confirms a guest runs under QEMU software emulation. A Debian-13 genericcloud clone needs roughly 18–25 minutes of first boot before its guest agent answers |
| CI | Workflow green on GitHub since 2026-10-24 ([run 40100674485](https://github.com/zarigata/double-agent/actions/runs/40100674485)): Go vet/test/build with `-race`, docs and locale checks, GitGuardian, and the Docker lifecycle smoke test twice on a clean `ubuntu-latest` host (25 of 25 per pass, no leaked containers). The Proxmox lifecycle job runs on demand on a self-hosted runner once the `proxmox` environment exists |
| Configuration guard | An enabled Proxmox runtime without complete credentials, endpoint, node, storage, or an allow list that includes the template is refused at startup |

The absence of KVM makes this host unsuitable for performance, GPU, desktop,
or Windows validation. It remains useful for lifecycle, authentication, MCP,
artifact, and guest-agent tests.

## Delivery sequence

### 1. Make the CPU control plane repeatable

1. Install the Go version from `go.mod` on a build host.
2. Run `go test ./...`, `node scripts/check-docs.mjs`, and
   `node web/test-i18n.mjs` in CI.
3. Build a versioned daemon and CLI artifact.
4. Start one CPU-only Docker node with a temporary data directory.
5. Run `scripts/validate-node.sh`; retain its sanitized output as release
   evidence.

**Gate:** an authenticated client can create, execute in, export from, and
destroy a Docker instance twice in succession, with no leaked resources.
Met on 2026-10-24 by the CI Docker smoke job on a clean `ubuntu-latest`
runner (run
[40100674485](https://github.com/zarigata/double-agent/actions/runs/40100674485));
the same lifecycle also passes on the development machine. The clean-host
install path (installer, systemd units) remains part of phase 3.

### 2. Prove the Proxmox runtime

1. Keep template `301` immutable and clone only full, disposable guests.
2. ~~Verify that VM `302` responds through QEMU guest agent, runs a harmless
   command, writes a small artifact, and is cleanly destroyed.~~ Done on
   2026-10-22 against the target host through the control plane (the earlier
   smoke guest `302` no longer exists; see the
   [validation report](VALIDATION_REPORT.md)).
3. ~~Change the runtime configuration to template `301` and allow only that
   template after the smoke test passes.~~ Done (`deploy/double-agent.yaml`).
4. Authenticate with a least-privilege Proxmox API token rather than
   `root@pam`; install the Proxmox CA and turn off `insecure`. The recorded
   run used the bootstrap posture (`root@pam`, `insecure: true`, trusted
   LAN).
5. Put disposable guests on a dedicated bridge/VLAN before accepting
   untrusted work. `vmbr0` is acceptable only for the trusted pilot.

**Gate:** the lifecycle is repeatable (executed twice on the target host),
the runtime refuses unmarked VMs (unit proven; no operator guest was touched
in the recorded run), and the guest agent supports command and file
operations (proven live: `uname -sr` exec and a 17-byte artifact export with
digest). Open item: a fully green `scripts/proxmox-lifecycle-test.sh` run —
the recorded run succeeded operationally but the script's export acceptance
list missed HTTP 201 (now fixed).

### 3. Package and secure the single-node release

1. Implement the signed installer and systemd units described in the
   operations documentation.
2. Configuration validation for every enabled runtime and a `doctor`
   check that verifies template/guest-agent readiness are implemented;
   the interrupted state a restart leaves behind is reconciled at startup.
3. CI coverage for the MCP transport, the Docker lifecycle, and a disposable
   Proxmox integration job exists in `.github/workflows/ci.yml`; the Proxmox
   job needs a self-hosted runner and credentials before it can run.
4. Exercise backup/restore, token revocation, and reconciliation after an
   interrupted job on a clean host (the restart reconciliation itself is
   covered by unit tests and demonstrated on the development machine).

**Gate:** a clean supported Linux host can install, pass diagnostics, pair one
MCP client, and recover from an interrupted job.

### 4. Add capability-specific work

Only after the CPU release gates pass: validate GPU containers per vendor,
then desktops/browser work, optional delegated AI, Windows VMs, and finally
GPU passthrough. Each capability needs a separate hardware matrix and smoke
test; none inherits support from the CPU pilot.

## Operational safeguards

- Do not bulk-delete Proxmox VMs. Delete only confirmed disposable guests with
  the `da-` name prefix or `double-agent` description marker.
- Keep one smoke VM at a time while the thin pool has no automatic expansion
  protection; monitor free space before cloning.
- Never send Proxmox passwords, API tokens, or MCP keys through MCP responses,
  logs, or repository files.
