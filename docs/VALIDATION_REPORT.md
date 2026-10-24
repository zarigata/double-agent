# Validation report — 2026-10-22

Evidence from the CPU/Docker pilot work of 2026-10-22. No credential, token
or secret appears in this report or in the recorded output.

## Environment

| Item | Value |
|---|---|
| Host | development Mac, macOS (darwin 24.6.0, x86_64) — **not** a clean supported Linux host |
| Go | go1.25.1 darwin/amd64 (the version of `go.mod`), user-local install |
| Docker | Docker Desktop 29.6.2 (linuxkit 6.12 kernel) |
| Node | v22.23.2 for the Markdown and locale checks |
| Daemon | `double-agentd 0.1.0-dev`, temporary data directory, `127.0.0.1:17443`, TLS on, Docker runtime, `network: none` |

## Commands and outcomes

### 1. Test, documentation and locale validation

| Command | Outcome |
|---|---|
| `go vet ./...` | pass |
| `gofmt -l cmd internal` | no output (formatted) |
| `go test ./...` | pass, 19 packages with tests, 0 failures |
| `go test -race -count=1 ./...` | pass, 19 packages with tests, no race reports |
| `node scripts/check-docs.mjs` | pass, 26 Markdown files, no broken links |
| `node web/test-i18n.mjs` | pass, 2 locales |
| `node scripts/test-i18n-app.mjs` | pass, 2 locales, 286 keys in use, no unused/missing keys |

New tests added by this pass: configuration validation of an enabled Proxmox
runtime (11 refusal cases plus the accepted token form), the Proxmox doctor
checks (template, guest agent, KVM vs QEMU software emulation, including the
`systemd-detect-virt` fallback), and reconciliation after restart
(interrupted jobs, instances without a resource, alignment with the backend).

### 2. Docker CPU lifecycle, twice in a row

Command: `scripts/validate-node.sh https://127.0.0.1:17443 <admin-password>`

| Run | Result |
|---|---|
| pass 1 | **25 passed, 0 failed** |
| pass 2 | **25 passed, 0 failed** |

The script covers: CA publication, rejection of an untrusted TLS client,
health, login with CSRF, session and scoped API key authentication, scope
denial, template catalogue, MCP `initialize` with a bearer key (and rejection
without one), instance create → exec (with network-isolation check) →
artifact export → destroy, and verification that the instance is gone.

Fixes required to reach green — the script had never had a recorded run:
the CA file it verified against was not the one it downloaded; the login
request dropped the session cookie; state-changing calls omitted the CSRF
token; the key route was wrong (`/api/v1/keys` → `/api/v1/admin/keys`);
export used a nonexistent per-instance route and an absolute path; and the
MCP probe was unauthenticated.

### 3. TTL cleanup

An instance created with `ttl_seconds: 900` **before a daemon restart**
expired and was reaped at its original deadline:

- API state: `destroyed` at 16:02:46Z (expiry 16:02:18Z plus the one-minute
  maintenance tick, `reaped expired instances count=1` in the log);
- the Docker container was removed;
- the instance workspace directory was removed.

### 4. Reconciliation after restart

Demonstrated on the running node:

- **Instance survives**: an instance created before a restart stayed `ready`
  after the restart, its container kept running, exec still worked
  (`survived-restart`), and it was destroyed normally afterwards. The TTL
  clock also survived the restart (item 3).
- **Interrupted job**: with a custom template whose step sleeps, the daemon
  was killed while the job was `running`. After the restart the log shows
  `marked an interrupted job failed` and `recovered state after restart
  failed_jobs=1`; the job reads `failed / daemon_restarted / the daemon
  restarted while the job was running`, and its instance stayed `ready` (its
  container was still alive) until destroyed. Interrupting the fast builtin
  templates by hand was not possible — their creates finish in under a
  second — so the deterministic proof of every interrupted-state combination
  is the unit suite (`internal/orchestrator/reconcile_test.go`).

### 5. Resource leaks

After all runs, `docker ps -a --filter label=double-agent.instance` listed no
containers: every instance was destroyed explicitly or by the TTL reaper.

### 6. Proxmox — real-node run (2026-10-22, second pass)

Target: `pve` (192.168.1.79, Proxmox VE 9.2.21), template `301`
(`da-pilot-debian-template`, Debian 13 genericcloud, `kvm: 0` — the template
itself configures QEMU software emulation), storage `local-lvm`, bridge
`vmbr0`. Credentials supplied by the operator for the run and kept only in a
mode-600 file outside the repository.

**Configuration fail-fast (local, against the real endpoint).** Three
deliberately incomplete configs were each refused with the exact error naming
the missing value (token without secret; empty allow list; template outside
the allow list) — exit 1 in every case.

**Doctor against the real node.**

| Check | Result |
|---|---|
| `runtime:proxmox` | OK — API and node reachable |
| `runtime:proxmox template` | OK — vmid 301 exists, is a template and is allowed |
| `runtime:proxmox guest agent` (static) | WARN — template does not set `agent=1`; clones do; proven live below |
| `runtime:proxmox guest agent` (`--proxmox-vmid` on a running clone) | OK — the agent answered a command |
| `runtime:proxmox acceleration` | WARN — **"QEMU software emulation without KVM"** (via `systemd-detect-virt`), matching the template's own `kvm: 0` |

**Lifecycle, twice in succession** (script
`scripts/proxmox-lifecycle-test.sh`): each pass cloned template 301 as a full
clone (`da-` name, `double-agent` description, `agent=1`, own MAC on `vmbr0`),
started it, ran `uname -sr` through the QEMU guest agent via the control
plane (`Linux 6.12.113+deb13-cloud-amd64`), wrote a file inside the guest, and
exported it through `artifact.export` (HTTP 201, sha256 digest recorded). Both
guests were destroyed and their disks removed; the node ended with only its
seven templates, `301` untouched and never started. The script labelled the
exports FAIL because its own acceptance list missed HTTP 201 — the operations
themselves succeeded; the list is fixed.

**Operational characteristic recorded:** the first boot of a Debian-13
genericcloud clone under QEMU software emulation takes roughly 18–25 minutes
before the guest agent answers on this host. The runtime now waits for the
agent inside every exec (bounded, 90 s per call) and refuses with
`capability_unavailable` while the guest is still booting; the test script
retries through that window.

**Bugs found by the real node and fixed in this pass** (all covered by new
unit tests):

- the orchestrator starts an instance twice and real Proxmox refuses to start
  a running guest — `Start` is now idempotent against the observed state;
- the guest list decodes `cpu` as an integer but the server answers with a
  fraction — the field is a float now;
- doctor read the certificate authority from the memory of a process that
  never served — it reads and parses the file on disk now;
- an unavailable guest agent surfaced as `500 internal_error` — it is
  `capability_unavailable` with a message that names the guest now.

**Not exercised on Proxmox:** restart reconciliation and TTL reaping against
the VM backend (proven on Docker end to end and by the unit suite; each
Proxmox attempt costs a ~20-minute first boot on this host), backup/restore,
and token revocation live.

**Cleanup after the run:** all guests the tests created were destroyed with
their disks (verified by a read-only guest-list call); the earlier smoke VM
302 no longer exists on the node — the live doctor check now targets any
running `da-` guest. Local Docker test images and containers were removed on
the development Mac.

## Fixes and additions this pass

- Configuration: an enabled Proxmox runtime without complete credentials,
  endpoint, node, storage, or an allow list that includes the template is
  refused at startup with a message naming the missing value.
- Doctor: `proxmox template` and `proxmox guest agent` checks; with
  `--proxmox-vmid <running da- guest>` the guest agent is proven live and the
  guest's virtualization is reported as KVM or QEMU software emulation.
- Reconciliation after restart (jobs and instances), unit tested and wired
  into daemon startup.
- `proxmox-linux` builtin template no longer pins guest id 205; it clones the
  configured `template_vmid`, so it matches the pilot allow list (`301`).
- The server certificate now covers loopback addresses, so a node bound to
  `127.0.0.1` verifies cleanly.
- An invalid orchestrator request (for example an absolute export path) is
  answered as `400 invalid_request` instead of `500 internal_error`.
- CI: `.github/workflows/ci.yml` — Go vet/test/build (with `-race`),
  docs and locale checks, the Docker lifecycle smoke test twice with a leak
  check, and a manual, self-hosted Proxmox job.
- `scripts/validate-node.sh` repaired (see item 2) and
  `scripts/proxmox-lifecycle-test.sh` added (later hardened by the real-node
  run: Proxmox username in the generated config, exec retries that cover the
  first-boot window, instance cleanup on a failed step).

## Limitations

- The Docker evidence comes from a development Mac, not a clean supported
  Linux host, and from Docker Desktop rather than a server Docker Engine.
- The Proxmox run used `root@pam` with `insecure: true` over the trusted LAN
  (the bootstrap-only posture of `deploy/double-agent.yaml`); the pilot docs
  still ask for a least-privilege API token and the Proxmox CA installed.
- The Proxmox test script's own export acceptance missed HTTP 201 during the
  recorded run (the operations succeeded; the list is fixed, a fully green
  script run is still pending on a host whose guests boot faster).
- The CI workflow has not run on GitHub yet; its first green run is the
  remaining CI evidence.
- Backup/restore and token revocation were not exercised live (existing unit
  tests cover them; the operations plan still asks for a live exercise).

## Remaining blockers before phase 4 work

1. One clean supported Linux host for the same Docker lifecycle and a clean
   install.
2. Push of the CI workflow and retention of its first green run.
3. A Proxmox API token with the least privilege the plan asks for, replacing
   the `root@pam` bootstrap credential.
