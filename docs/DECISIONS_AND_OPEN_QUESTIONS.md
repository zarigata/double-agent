# Decisions and open questions

## Confirmed product decisions

| Topic | Decision |
|---|---|
| Product | New software, independent from FeverCode |
| Installation | One command on a server or workstation |
| Initial network | Local network only |
| Interfaces | Web UI, MCP, and CLI/bridge |
| Internal AI | Optional: executor, delegate, or hybrid mode |
| Agent power | Full administration inside isolated instances |
| Persistence | Disposable instances; retain small state and selected artifacts |
| MVP hardware | CPU plus NVIDIA, AMD, and Intel GPUs |
| Environments | Linux containers, Linux VMs, and Windows VMs |
| Initial templates | Images, Blender, desktop/browser, development/tests |
| Administration | First admin at install; client keys in Web UI |
| Language | English canonical; human-facing localization supported |
| Commercial model | Source-visible; resale/redistribution requires authorization |

## Provisional technical decisions

- Go daemon with an embedded TypeScript/React Web UI;
- SQLite metadata store in the MVP;
- Docker Engine as the first container backend;
- libvirt/QEMU/KVM for virtual machines;
- job-oriented internal API even on one node;
- MCP Streamable HTTP plus a local `stdio` bridge;
- mDNS `_double-agent._tcp.local` for physical-LAN discovery;
- TLS with a local CA and scoped tokens;
- separate persistent caches for OCI images and models;
- Kubernetes later as a driver, not an installer dependency;
- ICU-style message catalogs and English fallback for the Web UI.

## Technical boundaries

The NVIDIA/AMD/Intel MVP commitment applies first to Linux container workloads.
Whole-device VM passthrough is experimental and hardware-dependent. Windows
media and licenses are provided by the user. macOS guests are not supported on
generic Linux servers. “Full permission” means root/Administrator inside an
instance, never uncontrolled access to the host.

## Open questions

1. What domain will host releases, documentation, and the installer?
2. What are the exact Intel and AMD target-server specifications?
3. Which clients are certified first?
4. Which image workflow engine becomes the initial template?
5. Which models may be downloaded automatically under their licenses?
6. Which Windows editions are tested first?
7. What default artifact retention period is appropriate?
8. Does the first installer target only Ubuntu LTS or Debian as well?
9. Which hosted LLM adapters follow the OpenAI-compatible interface?
10. Which language follows English and Brazilian Portuguese?
11. Who owns and reviews translations for security-sensitive UI copy?

## Questions required before a public release

### Product license

“Open source but redistribution is prohibited” conflicts with the Open Source
Definition. The accurate term is **source-available**. Counsel must review the
final community/commercial licensing model. This document is not legal advice.

### Third-party licenses

Every template must declare OS/application/model licenses, redistribution
rights, source URLs, hashes, and any user-supplied media or key requirements.

### Multi-user threat model

Internet exposure or commercial hosting requires stronger tenant isolation,
OAuth, rate limiting, egress policy, durable audit, abuse controls, and an
external security review.

### Localization governance

Canonical source strings and API contracts remain English. Translations must be
versioned, reviewed, tested for key parity, and clearly marked when incomplete.
Security and destructive-action prompts require human review for every locale.

## Criteria for changing provisional decisions

Change Docker, SQLite, or the single-node design only when a measured requirement
is not met. Add Kubernetes when a real second-node/cluster use case exists. Add
microVMs when untrusted workloads justify their cost. Claim Windows GPU support
only after repeatable tests on target hardware.
