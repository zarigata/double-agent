# Security, storage, and lifecycle

## Trust model

The initial deployment is a trusted home, studio, or team LAN—not a hostile
public multi-tenant cloud. Clients and tasks may still be compromised, so the
host boundary is enforced even when the agent has root inside an instance.

## Protected assets

- host operating system and runtime sockets;
- administrator credentials and API keys;
- LLM, registry, repository, and model-provider secrets;
- other users' instances and artifacts;
- GPU devices not assigned to the job;
- control-plane metadata and audit history;
- private LAN services and management endpoints.

## Core boundary

Instances never receive Docker/libvirt sockets, arbitrary host mounts, the host
PID namespace, privileged mode, unallocated devices, or direct vault access.
Dangerous capabilities are denied by default. Linux security controls should
include namespaces, cgroups v2, seccomp, capability dropping, read-only base
filesystems, AppArmor/SELinux where available, and controlled device access.

## Identity and authentication

The installer bootstraps one administrator. Passwords use a modern memory-hard
hash. Client keys are random, shown once, stored hashed, scoped, attributable,
expiring when appropriate, and immediately revocable. Browser sessions use
secure cookies, CSRF protection, rotation, and inactivity limits.

## Authorization

Policy evaluates actor, scope, ownership, template, runtime, resource quantity,
device, network policy, TTL, concurrent jobs, and approval requirements.
Administrator power in the Web UI does not automatically transfer to an MCP key.

## Secret handling

Secrets are encrypted at rest with a server key separated from the database.
Tools reference values such as `secret/github-read-token`; only the runtime
materializes the value for the authorized job. Redaction occurs before logs,
errors, events, artifacts, and LLM context leave the execution boundary.

## Network controls

- bind only to administrator-selected LAN interfaces;
- require HTTPS and reject unsafe `Origin` values;
- never expose guest VNC/RDP ports directly to the LAN;
- proxy desktops through authenticated, short-lived sessions;
- block access from guests to the control plane and host metadata;
- make outbound network policy explicit per template;
- keep internet exposure unsupported in the MVP.

## Artifact safety

Artifact paths are normalized and scoped to the job. Export prevents traversal,
symlink escapes, special devices, decompression bombs, and uncontrolled size.
Every artifact has an owner, MIME type, digest, size, source job, retention
deadline, and access policy. Downloads use authenticated or short-lived URLs.

## Persistence classes

| Class | Examples | Default retention |
|---|---|---|
| Control state | users, sessions, recipes, audit | persistent |
| Rebuildable cache | OCI images, packages, models | quota/eviction based |
| User artifacts | reports, images, binaries | policy based |
| Instance state | overlays, temporary workspace | destroyed with TTL |

Sessions persist as metadata and references, not complete machines. Recreating
an environment resolves the recorded recipe against currently available
hardware and verified images.

## Lifecycle

1. authorize the request;
2. resolve template and recipe;
3. reserve quotas and resources;
4. provision and attest the instance;
5. execute work while collecting safe telemetry;
6. export approved artifacts;
7. stop and destroy the instance;
8. release devices and storage;
9. retain only policy-approved metadata and artifacts.

TTL is mandatory. Extensions are scoped and audited. A garbage collector and a
reconciler remove expired or orphaned resources after crashes or host restarts.

## Audit

Audit records include actor, action, target, decision, correlation ID, policy
version, timestamp, result, and safe metadata. Events are append-oriented,
tamper-evident where practical, and written in stable English vocabulary so
operators can search across locales.

## Threats that require explicit tests

- host mount or runtime-socket access;
- lateral access between instances;
- path traversal and symlink escape;
- SSRF against control-plane or metadata endpoints;
- DNS rebinding and unsafe browser origins;
- revoked, expired, or brute-forced credentials;
- secret leakage into logs or AI context;
- tampered templates or images;
- huge artifacts and decompression bombs;
- workloads that ignore shutdown;
- cleanup failure after daemon or host restart.

## Backup

Backups contain consistent control state, configuration, templates, recipes,
local CA material, and optionally selected artifacts. They exclude ephemeral
instances, overlays, OCI images, models, and high-volume logs by default.
Backups are encrypted and restorable on hardware without the original GPU.

## Public or commercial deployment gate

Before internet exposure or hostile multi-tenancy, the project requires a new
threat model, stronger isolation such as microVMs, OAuth/OIDC, rate limiting,
central policy, durable audit export, abuse controls, external security review,
and a commercial licensing review.
