# Installation and operations

> **Current state:** this document describes the intended release installer.
> It is not present in this repository yet. Follow
> [Pilot readiness](PILOT_READINESS.md) for the current manual validation path.

## Supported host profile

The initial target is a dedicated x86_64 Linux server or workstation with
hardware virtualization available. Ubuntu LTS is the first supported platform;
other distributions follow after automated installation tests.

Minimum development profile:

- 4 CPU cores, 16 GB RAM, and 100 GB free storage;
- systemd and cgroups v2;
- Docker Engine for container workloads;
- Proxmox VE 8+ for the currently implemented VM backend;
- optional NVIDIA, AMD, or Intel GPU with a compatible host driver.

## One-line installation goal

```bash
curl -fsSL https://install.double-agent.dev | sudo sh
```

The convenience command downloads a versioned installer. The installer must
verify signatures/checksums before executing the payload, display the selected
release and source, and support an offline/manual package path.

## Installation stages

1. verify OS, architecture, free resources, systemd, cgroups, and virtualization;
2. create a locked `double-agent` service account and data directories;
3. install the signed daemon, CLI, embedded Web UI, and service units;
4. prompt for the first administrator username and password;
5. generate a local CA, server certificate, and recovery material;
6. detect Docker, Proxmox/KVM, and GPU devices;
7. ask before installing or changing any runtime/driver dependency;
8. run capability smoke tests;
9. start the service and print URL, fingerprint, and next steps.

Unattended installation accepts values through a root-readable configuration
file or secret input—not plaintext command-line flags that leak into history.

## Filesystem layout

```text
/usr/bin/double-agent
/usr/bin/double-agentd
/etc/double-agent/config.yaml
/etc/double-agent/pki/
/var/lib/double-agent/state/
/var/lib/double-agent/cache/
/var/lib/double-agent/artifacts/
/var/lib/double-agent/instances/
/var/log/double-agent/
```

Permissions isolate service data from ordinary host users. Instance workspaces
and artifacts have separate quotas and mount policies.

## Hardware diagnostics

`double-agent doctor --json` reports each capability as `ready`, `degraded`,
`driver_missing`, `runtime_missing`, `unsupported`, `untested`, or
`needs_reboot`. It explains the evidence and exact action required. Driver or
runtime installation always requires administrator approval and a post-change
smoke test.

## Services

```text
double-agent.service        daemon, MCP, API, and Web UI
double-agent-gc.timer       expired instance and artifact collection
double-agent-update.timer   update check only
```

Docker and libvirt remain independent system services. The daemon receives the
minimum permissions needed to call their local APIs.

## Default ports

| Use | Proposed default | Exposure |
|---|---:|---|
| Web UI, API, MCP | 7443/TCP | selected LAN interfaces |
| mDNS | 5353/UDP | local multicast |
| Guest VNC/RDP | dynamic/internal | never directly on LAN |
| Guest agent | vsock/virtio/private | host-to-guest only |

Manual IP/hostname configuration always remains available where multicast is
blocked or `.local` naming conflicts with network policy.

## Certificates and pairing

Bootstrap generates a local CA and server certificate with known hostnames/IPs
as SANs. The installer displays a SHA-256 fingerprint. The bridge pairs with a
short-lived code and pins the CA/fingerprint. There is no permanent “ignore TLS”
mode. Operators may replace local PKI with their own reverse proxy/certificates.

## Administrative CLI

```text
double-agent status
double-agent doctor [--json]
double-agent discover
double-agent pair <server>
double-agent connect <client>
double-agent users ...
double-agent keys ...
double-agent templates ...
double-agent instances ...
double-agent jobs ...
double-agent artifacts ...
double-agent backup
double-agent restore
double-agent update
double-agent uninstall
double-agent mcp-bridge ...
```

Mutating commands show their target and request confirmation for interactive
use. Authenticated automation may opt into `--yes`.

## Daily operations

The dashboard exposes host reserves, CPU/RAM/storage, GPU health and memory,
queued/running jobs, instance TTLs, caches, artifacts, runtime health, and
actionable failures. Local alerts cover resource pressure, unavailable runtimes,
stuck cleanup, repeated template failures, certificate expiry, updates, and
suspicious login or pairing activity.

## Updates and rollback

Updates are signed, downloaded and verified before stopping the service, and
preceded by a control-state backup. Database migrations are transactional.
Health checks gate activation, and rollback restores the prior binary/schema
when supported. GPU drivers are never bundled into automatic daemon updates.

## Uninstallation

`double-agent uninstall` lists affected jobs and instances, requires confirmation
before stopping them, removes product services and binaries, and separately asks
whether to preserve `/var/lib/double-agent`. It never removes Docker, libvirt, or
drivers that existed before Double Agent.

## Kubernetes

Kubernetes is a later runtime driver for real multi-node demand. It is not a
dependency and is never installed automatically on a single host.
