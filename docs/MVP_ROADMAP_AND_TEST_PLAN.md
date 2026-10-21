# MVP roadmap and test plan

## Strategy

Build demonstrable vertical slices. The first slice must cross installation,
authentication, MCP, scheduling, runtime, artifacts, and cleanup with CPU only.
GPU, desktop, delegation, and VMs follow as independently testable increments.

## Phase 0 — technical proofs

- inventory the target Intel server;
- run a minimal Intel GPU container workload;
- repeat probes on available NVIDIA and AMD hardware;
- render and export with headless Blender;
- run the selected image engine on Intel and one additional vendor;
- expose a container desktop through an authenticated gateway;
- boot user-provided Linux and Windows images with QEMU/KVM;
- prototype remote MCP plus a `stdio` bridge;
- validate mDNS, pairing, and certificate pinning;
- benchmark cold start, warm start, and artifact throughput.

Gate: publish no broad compatibility claim before recording exact hardware,
kernel, driver, runtime, and workload evidence.

## Phase 1 — CPU vertical slice

Deliver the Ubuntu package/installer, administrator bootstrap, daemon, minimal
Web UI, SQLite, keys, audit, CPU Docker runtime, `dev-linux` template, essential
MCP tools, artifacts, TTL cleanup, reconciliation, diagnostics, backup, and clean
host integration tests.

Acceptance demo: install with one command, pair a client, create a container,
upload source, run tests, download a report, and observe automatic destruction.

## Phase 2 — heterogeneous GPU

Deliver normalized NVIDIA/AMD/Intel inventory, CDI/device-node adapters,
exclusive/shared scheduling, image and Blender templates, licensed model cache,
vendor diagnostics, explicit CPU fallback, and automated compatibility reports.

Acceptance demo: the same `image.generate` request resolves on each supported
vendor and returns an artifact with recipe, backend, seed, versions, and hashes.

## Phase 3 — desktop and browser

Deliver `desktop-linux` and `browser-worker`, authenticated one-time console
links, watch/takeover/pause control, screenshots/downloads as artifacts, template
egress policy, and tests proving internal ports are not exposed directly.

## Phase 4 — optional internal AI

Deliver OpenAI-compatible local and hosted adapters, `task.delegate`, hybrid
mode, step/time/cost/resource limits, separate audit attribution, human-input
pauses, and a repeatable evaluation suite.

## Phase 5 — virtual machines

Deliver libvirt, Linux cloud images, the guest agent, user-supplied Windows image
import, ephemeral overlays, Windows exec/desktop, `windows-test`, and experimental
VFIO behind a capability flag.

## Phase 6 — MVP release

Complete hardening, migrations, rollback, documentation, MCP resources, SBOM,
signed releases, legal review of the source-available license, clean-install
testing, one certified client, and a public limitations report.

## Post-MVP

1. multi-node operation with node agents and mTLS;
2. Kubernetes/KubeVirt runtime;
3. OAuth 2.1 and enterprise identity;
4. microVM isolation;
5. private template registry;
6. physical Mac worker;
7. reservations and internal chargeback;
8. CI/CD integrations;
9. declarative policy and advanced approvals;
10. commercial operation outside the LAN after security review.

## Test pyramid

### Unit

Schema validation, state machines, scheduler, quotas, scopes, redaction, recipe
resolution, retention, garbage collection, locale fallback, and device inventory.

### Contract

MCP versions, private Web API, runtime interface, guest agent, template manifests,
recipes, audit events, and translation catalog parity.

### Integration

Real Docker/libvirt, SQLite recovery, mDNS/pairing, large and interrupted uploads,
restart during jobs, missing resources, artifact cleanup, and locale persistence.

### Hardware

| Dimension | Minimum release evidence |
|---|---|
| NVIDIA | supported consumer GPU; CUDA and Vulkan |
| AMD | target device; ROCm/HIP and Vulkan |
| Intel | initial server; Level Zero/OpenVINO and Vulkan |
| CPU | x86_64 host without GPU |
| Runtime | Docker; libvirt for VM phase |
| Guest | Linux; Windows with user-provided media |

### Security

Attempt host mounts, runtime-socket access, lateral movement, path escapes, SSRF,
DNS rebinding, revoked/expired tokens, brute force, secret leakage, template
tampering, decompression bombs, resource exhaustion, and shutdown resistance.

### Client compatibility

Claude Code, Cursor, Codex, MCP Inspector, and the generic `stdio` bridge must
pass tool listing/calls, resources, long jobs, cancellation, reconnect, and
artifact transfer.

### Internationalization

- all catalogs contain the same keys;
- English loads when a locale or key is missing;
- `en`, `en-US`, `pt`, and `pt-BR` resolve correctly;
- selection persists and updates the document `lang` attribute;
- APIs, state values, logs, and identifiers never change with UI locale;
- layouts tolerate text expansion and keyboard-only selection.

## Failure scenarios

Kill the daemon during provisioning, restart Docker/libvirt, revoke GPU access,
fill temporary storage, interrupt uploads, reboot with active jobs, expire a
token mid-job, corrupt a model download, lose a desktop process, and force
cleanup retries/quarantine. A release is incomplete if it cannot reconcile.
