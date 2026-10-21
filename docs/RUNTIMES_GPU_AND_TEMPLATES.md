# Runtimes, GPUs, and templates

## Runtime strategy

Use the lightest isolation that satisfies the task. Linux containers are the
default for speed and GPU access. QEMU/KVM virtual machines are used for another
kernel, Windows, stronger boundaries, or software that cannot run in a container.

| Requirement | Preferred runtime |
|---|---|
| Linux CLI, build, or tests | Container |
| Linux GPU workload | Container with CDI/device mapping |
| Linux graphical desktop | Container first, VM when required |
| Windows | QEMU/KVM VM |
| Exclusive GPU passthrough | VM with validated VFIO |
| Hostile multi-tenant workload | Outside initial MVP; evaluate microVMs |

## Hardware inventory

`double-agent doctor` normalizes every device into a capability record:

```json
{
  "device_id": "gpu_0",
  "vendor": "intel",
  "model": "Arc A770",
  "memory_mb": 16384,
  "backends": ["level-zero", "opencl", "vulkan", "openvino"],
  "container_access": "validated",
  "vm_passthrough": "untested",
  "health": "ready",
  "evidence": { "probe": "sha256:...", "tested_at": "..." }
}
```

Capabilities are published only after an executable smoke test. Detection alone
is insufficient.

## NVIDIA

The container adapter uses the NVIDIA driver, Container Toolkit, and CDI when
available. It selects individual devices rather than exposing every GPU by
default. Validation includes driver/runtime compatibility, CUDA execution,
VRAM visibility, and an application-level sample.

## AMD

The adapter validates the exact GPU/kernel/ROCm matrix, `/dev/kfd`, DRI render
nodes, group permissions, and an application-level HIP or Vulkan workload.
Unsupported consumer devices are reported honestly instead of receiving a
generic vendor-level compatibility claim.

## Intel

The adapter validates DRI render nodes, kernel drivers, Level Zero, OpenCL,
OpenVINO or Vulkan as required by the template. The first target server defines
the initial tested matrix; additional devices are added through evidence.

## CPU fallback

Templates declare whether CPU fallback is allowed. The scheduler never silently
turns a GPU request into a much slower CPU job. It returns the resolved recipe
and estimated consequences for client approval when the difference is material.

## GPU allocation

- exclusive allocation is the safe default for memory-heavy workloads;
- shared access requires vendor/runtime support and an explicit policy;
- VM passthrough locks the whole device and its IOMMU group;
- MIG, SR-IOV, and vendor partitioning are capabilities, not assumptions;
- temperature, memory pressure, and reset failures affect scheduling health.

## Template model

A template is a signed, versioned declaration—not an arbitrary shell script.

```yaml
apiVersion: double-agent.dev/v1alpha1
kind: Template
metadata:
  name: blender
  version: 0.1.0
spec:
  runtime: container
  image: registry.example/double-agent/blender@sha256:...
  capabilities:
    - blender.headless
    - gpu.vulkan
  resources:
    cpu: { min: 4, default: 8 }
    memoryMiB: { min: 8192, default: 16384 }
    gpu: { count: 1, optional: true }
  network:
    egress: restricted
  lifecycle:
    defaultTTLSeconds: 3600
    maxTTLSeconds: 14400
  outputs:
    - "**/*.blend"
    - "**/*.glb"
    - "renders/**/*"
```

Templates include image digests, licenses, source URLs, supported architectures,
runtime constraints, health checks, output patterns, network policy, lifecycle,
and a compatibility test suite.

## Initial template catalog

| Template | Purpose | Runtime |
|---|---|---|
| `dev-linux` | Builds and automated tests | Container |
| `image-generation` | Image workflows | GPU container |
| `blender` | Headless and interactive 3D work | GPU container/VM |
| `desktop-linux` | Temporary desktop | Container/VM |
| `browser-worker` | Isolated browser automation | Container |
| `windows-test` | Windows application validation | VM |

## Models and licenses

Model downloads require an explicit catalog entry containing source, digest,
license, redistribution status, expected size, compatible engines, and optional
acceptance steps. User-supplied assets and credentials remain separate from
rebuildable caches.

## Windows and macOS

Double Agent does not redistribute Windows media. Administrators provide valid
installation media, base images, and licenses; overlays are ephemeral. Generic
Linux servers do not host macOS guests. A future physical Mac worker may use
Apple's supported virtualization APIs subject to applicable licenses.

## Reproducibility

Each completed job stores a recipe containing template version, image digest,
model digest, hardware backend, driver/runtime versions, command inputs, seeds,
environment policy, and artifact hashes. Secrets are represented only by opaque
identifiers and are never included in the recipe.
