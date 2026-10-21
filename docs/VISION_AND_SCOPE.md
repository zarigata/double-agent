# Vision and scope

## Problem

Development agents usually run on a user's computer or inside a restricted
sandbox. When they need a GPU, a graphical desktop, another operating system,
large toolchains, or several parallel environments, the user must prepare the
infrastructure manually and consume local resources.

Double Agent provides a third option: a dedicated local server that publishes
its proven capabilities to authorized agents. The client asks for an outcome,
does not need to understand every host detail, and receives exported artifacts.

## Vision

Double Agent should be to agent infrastructure what a hypervisor is to server
infrastructure: a control plane for creating, using, observing, and destroying
environments. Its primary interface is designed for AI agents through MCP, and
its catalog describes outcomes such as “render in Blender,” “generate an
image,” “open a desktop,” “run the test suite,” or “create a Windows VM.”

It does not replace Proxmox, Docker, Kubernetes, or libvirt. It orchestrates
them through drivers and adds:

- agent-readable discovery and documentation;
- reproducible recipes;
- asynchronous jobs and artifacts;
- hardware-aware scheduling;
- an optional internal AI;
- ephemeral lifecycle management;
- an operator-focused Web UI.

## Initial users

### Individual developer

Owns an idle workstation or server and wants Claude Code, Cursor, Codex, or
another client to use that hardware without overloading a laptop.

### Team on a LAN

Shares a build or GPU server. Each person receives a scoped key and only sees
their sessions, while administrators can observe the entire node.

### AI lab operator

Keeps heavy models and applications cached and wants to expose temporary
services without turning every tool into a permanent API.

## Priority use cases

1. **Image generation:** select compatible hardware, obtain an authorized
   model, run a workflow, and export images plus provenance metadata.
2. **Blender:** create assets by script or desktop, render them, and export
   `.blend`, `.glb`, `.fbx`, textures, and previews.
3. **Development and testing:** receive source code, install dependencies,
   start services, run tests, and return reports.
4. **Desktop and browser:** provide an isolated Linux desktop with Chromium for
   visual work or human takeover.
5. **Temporary Windows:** boot a user-licensed image for application tests or
   Windows automation.
6. **Parallel work:** create independent instances while enforcing quotas.

## Two interface levels

Intent tools are optimized for outcomes, such as `image.generate`,
`blender.create_asset`, `software.test`, and `browser.perform_task`.
Infrastructure tools expose explicit control over instances, commands, files,
logs, desktops, and destruction. Both levels must coexist.

## Expected user journey

1. Install Double Agent and create the first administrator.
2. Review the host inventory and proven capabilities in the Web UI.
3. Configure optional LLM providers and resource policies.
4. Generate a scoped client key.
5. Discover and pair with the server on the LAN.
6. Let the agent read documentation resources and call MCP tools.
7. Observe jobs, instances, quotas, and artifacts in the Web UI.
8. Download results while ephemeral instances are destroyed automatically.

## Measurable MVP goals

- fresh installation to Web UI within 15 minutes, excluding drivers and models;
- warm CPU container creation within 10 seconds;
- mDNS visibility within 5 seconds on the same subnet;
- no raw secret in logs or MCP responses;
- reliable TTL cleanup and reboot reconciliation;
- validated execution on at least one NVIDIA, AMD, and Intel device;
- artifact transfer beyond practical MCP payload limits;
- control-plane recovery independent of ephemeral instances.

## Initial non-goals

- operating a public cloud;
- claiming hostile multi-tenant isolation equivalent to a cloud provider;
- acquiring Windows, application, or model licenses for the user;
- retaining full instance disks indefinitely;
- requiring an internal LLM;
- installing Kubernetes on a single node without a demonstrated need;
- running macOS guests on generic non-Apple hardware.

## Positioning

Xiryn inspired the concept of a disposable Linux workstation per agent. Double
Agent shifts the center toward shared infrastructure: MCP, hardware inventory,
multiple runtimes, jobs, recipes, artifacts, and optional delegation. It must
remain useful even when it runs no model of its own.

## Language policy

The product must work for an international audience. English is canonical for
machine-facing contracts and primary documentation. Human-facing surfaces are
localizable; Brazilian Portuguese is the first supported translation. See
[Internationalization](I18N.md).
