# Proxmox integration test plan

This plan validates the Proxmox VM runtime of the control plane against a
real Proxmox VE node. It uses **only disposable guests** — guests the backend
itself created, marked with the `da-` name prefix and the `double-agent`
description — plus, optionally, the existing disposable smoke guest. The
immutable template and every other guest of the node are never modified or
deleted by this plan.

Follow [Pilot readiness](PILOT_READINESS.md) first: the CPU Docker lifecycle
must be repeatable before this plan is worth running, because a failure that
also exists on Docker is not a Proxmox failure.

## Current target

| Item | Value |
|---|---|
| Proxmox node | `pve`, Proxmox VE 9.2.21, `192.168.1.79:8006` |
| Template | VM `301` (`da-pilot-debian-template`), Debian 13 genericcloud, immutable, `kvm: 0` |
| Smoke guest | none — the earlier VM `302` was removed; the live doctor check targets any running `da-` guest |
| Virtualization | **No KVM on this host** — guests run under QEMU software emulation |
| Allowed templates | `301` only (`deploy/double-agent.yaml`) |

The absence of KVM is why this plan proves lifecycle, authentication, guest
agent and artifact behavior — and explicitly **not** performance, GPU,
desktop, or Windows behavior. `double-agentd doctor --proxmox-vmid` reports
whether a guest sees KVM or software emulation, so the distinction is checked,
not assumed.

The first recorded run (2026-10-22) executed the lifecycle twice on this
target: clone, guest-agent exec, artifact export (with digest) and destroy
all worked. The first boot of a clone takes roughly 18–25 minutes before the
guest agent answers — the runtime waits for the agent inside every call and
the script retries through that window. Baking `qemu-guest-agent` into the
image and adding a virtio-rng device to the clones are the recommended
template hardening steps to shrink that window.

## Preconditions

1. Template `301` is marked as a template in Proxmox, is in
   `allowed_templates`, and has enough space on `local-lvm` for a full clone.
2. The template image contains `qemu-guest-agent` and runs it. The
   genericcloud image relies on the cloud-init snippet for this; the recorded
   2026-10-22 run showed a roughly 18–25 minute first boot before the agent
   answered on the KVM-less host.
3. Credentials come from the environment, never from a file or a command
   line: `DOUBLE_AGENT_PROXMOX_PASSWORD`, or `DOUBLE_AGENT_PROXMOX_TOKEN_ID`
   with `DOUBLE_AGENT_PROXMOX_TOKEN_SECRET`. Prefer a least-privilege API
   token over `root@pam`.
4. The control plane refuses an incomplete Proxmox configuration at startup
   (missing endpoint, credentials, node, storage, or an allow list that does
   not include the template), so a configuration mistake is caught before any
   guest exists.

## Automated run

`scripts/proxmox-lifecycle-test.sh` executes the whole plan below against a
temporary node on `127.0.0.1:7444`. It drives everything through the
authenticated API, so the only guests it can delete are the ones the backend
created and marked.

```sh
go build -o build/double-agentd ./cmd/double-agentd
export DOUBLE_AGENT_PROXMOX_ENDPOINT=https://192.168.1.79:8006
export DOUBLE_AGENT_PROXMOX_NODE=pve
export DOUBLE_AGENT_PROXMOX_STORAGE=local-lvm
export DOUBLE_AGENT_PROXMOX_TEMPLATE_VMID=301
export DOUBLE_AGENT_PROXMOX_USERNAME=root@pam   # user of the password credential
export DOUBLE_AGENT_ADMIN_PASSWORD=...        # administrator of the temporary node
export DOUBLE_AGENT_PROXMOX_PASSWORD=...      # or the API token pair
# optional, for the live guest-agent and KVM/QEMU check:
export DOUBLE_AGENT_PROXMOX_SMOKE_VMID=100  # any running disposable da- guest
export DOUBLE_AGENT_PROXMOX_INSECURE=true     # trusted LAN only; install the CA instead
scripts/proxmox-lifecycle-test.sh
```

The script prints one PASS/FAIL line per check and exits non-zero on the
first class of failure it cannot recover from. Its output, with credentials
never included, is the evidence to retain.

The CI workflow runs the same script on demand (`.github/workflows/ci.yml`,
job `proxmox`) on a self-hosted runner labelled `proxmox` with the variables
and secrets of the `proxmox` environment. It never runs on pull requests.

## What is verified, step by step

1. **Configuration and doctor.** The temporary node starts with the Proxmox
   backend enabled and `proxmox-linux` as the default template.
   `double-agentd doctor` must report the API reachable, node `pve` online,
   template `301` present, a template, and allowed. With
   `--proxmox-vmid <guest>` on a running `da-` guest it additionally proves
   the guest agent answers and reports whether the guest runs with KVM or
   under QEMU software emulation.
2. **Clone.** `instance.create` with the `proxmox-linux` template clones
   template `301` as a **full** clone: the new guest carries a `da-` name and
   the `double-agent` description, gets `agent=1`, its own network device on
   the configured bridge, and is started. The template itself is untouched.
3. **Guest-agent exec.** `uname -sr` runs inside the guest through the QEMU
   guest agent and returns the guest kernel.
4. **Artifact transfer.** A small file is written inside the guest and
   exported through `artifact.export`; the control plane records its digest.
   (The export path from a VM goes through the guest agent, not a bind
   mount.)
5. **Destroy.** `instance.destroy` stops the guest and deletes it with
   `destroy-unreferenced-disks=1`, so no disk is left on the storage.
   The deletion safeguard refuses any guest without the ownership marker.
6. **Repeat.** Steps 2–5 run twice in a row: the gate is repeatability, not a
   single success.

After the run, confirm on the Proxmox side (UI, `qm list`, or an API read
with the operator's own credentials) that:

- the two lifecycle guests are gone, including their disks;
- template `301` still exists, is still a template, and was not started;
- no other guest of the node changed.

## Manual cleanup policy

The script and the backend only ever delete guests they created and marked.
If a run is interrupted and a `da-` guest is left behind:

1. Confirm the guest's name starts with `da-` **or** its description contains
   `double-agent`.
2. Confirm it is not the configured template (`301`) and not a guest someone
   else is using.
3. Delete it by hand in Proxmox with "destroy unreferenced disks" enabled.

An unmarked, ambiguous, or manually created guest is never deleted without an
operator decision. See [Build plan](BUILD_PLAN.md) for the safeguards.

## Evidence to retain

For each run keep, without any credential or token:

- the sanitized script output (PASS/FAIL lines and doctor tables);
- daemon version (`double-agentd version`), Proxmox version, and date;
- the guest identifiers the run created and destroyed;
- the doctor line that reports KVM or QEMU software emulation.

With KVM absent on this host, a green run of this plan **does not** validate
performance, GPU, desktop, or Windows support; those require their own
hardware matrix per [Build plan](BUILD_PLAN.md) phase 4.
