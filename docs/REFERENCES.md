# References

Research baseline: October 21, 2026. Prefer primary documentation and pin
versions during implementation because runtimes, drivers, and MCP evolve.

## Inspiration

- [Xiryn repository and architecture](https://github.com/SouravBeraAkaSpeed/xiryn)
  — a Docker-based disposable Linux workstation per agent. Double Agent adopts
  the isolation/lifecycle idea while remaining an independent MCP infrastructure
  server that does not require its own agent.

## Model Context Protocol

- [MCP specification 2026-07-28](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/index.mdx)
- [Streamable HTTP](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/transports/streamable-http.mdx)
- [`server/discover`](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/server/discover.mdx)
- [Authorization server discovery](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/authorization/authorization-server-discovery.mdx)
- [MCP Server Cards and AI Catalog](https://github.com/modelcontextprotocol/ext-server-card/blob/main/docs/discovery.md)

The architecture uses remote Streamable HTTP, validates browser origins,
authenticates every connection, models long work with explicit job handles, and
uses mDNS for physical LAN discovery rather than as a replacement for Server Cards.

## GPU containers

### NVIDIA

- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/overview.html)
- [CDI support](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/cdi-support.html)
- [Installation guide](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html)

### AMD

- [ROCm installation on Linux](https://rocm.docs.amd.com/projects/install-on-linux/en/latest/)
- [ROCm in containers](https://rocm.docs.amd.com/projects/install-on-linux/en/latest/how-to/docker.html)

### Intel

- [Intel oneAPI containers](https://www.intel.com/content/www/us/en/docs/oneapi/installation-guide-linux/2025-0/run-a-sample-project-using-an-eclipse-container.html)
- [Intel GPU documentation](https://dgpu-docs.intel.com/)
- [OpenVINO GPU device](https://docs.openvino.ai/2025/openvino-workflow/running-inference/inference-devices-and-modes/gpu-device.html)

## Virtualization and operating systems

- [libvirt host-device assignment](https://libvirt.org/formatdomain.html#host-device-assignment)
- [Linux VFIO](https://docs.kernel.org/driver-api/vfio.html)
- [QEMU documentation](https://www.qemu.org/docs/master/)
- [Apple Virtualization framework](https://developer.apple.com/documentation/virtualization)
- [Apple software license agreements](https://www.apple.com/legal/sla/)
- [Microsoft Windows licensing](https://www.microsoft.com/licensing/docs/view/Windows-Desktop)

## Product licensing

- [Open Source Definition](https://opensource.org/osd)
- [Open Source Initiative FAQ](https://opensource.org/faq)

The Open Source Definition requires free redistribution and does not permit a
general ban on commercial use. Code that can be viewed but not redistributed
without authorization should be called source-available.

## Implementation references still to select

- Docker Engine API and selected Go client;
- libvirt API and bindings;
- OCI and Container Device Interface specifications;
- mDNS/DNS-SD RFC 6762 and RFC 6763;
- current Argon2 password-hashing guidance;
- OAuth requirements for the selected MCP version;
- SPDX/CycloneDX for SBOM;
- Sigstore/SLSA for release provenance;
- ICU MessageFormat or FormatJS for production localization;
- license terms for every distributed model and application.
