# MCP API and LAN discovery

## Transport

The primary remote transport is MCP Streamable HTTP over HTTPS at `/mcp`.
Clients limited to `stdio` use the `double-agent mcp-bridge` command, which
pairs once and forwards requests securely. Supported protocol versions are
negotiated and tested explicitly.

## Discovery and pairing

The daemon advertises `_double-agent._tcp.local` through mDNS/DNS-SD with only
non-secret metadata: instance name, port, API generation, and TLS fingerprint
hint. Discovery never grants access.

Pairing flow:

1. the CLI discovers or receives a host/IP;
2. the user verifies the server fingerprint;
3. the Web UI or console shows a short-lived pairing code;
4. the server issues a scoped, revocable client credential;
5. the bridge pins the local CA or fingerprint;
6. all future traffic uses TLS and authorization policy.

## Tool design principles

- Keep tools small, stable, composable, and strongly typed.
- Return handles for jobs, instances, uploads, and artifacts.
- Use idempotency keys for creation and other retry-sensitive operations.
- Prefer artifacts over embedding large binary payloads.
- Represent secrets as opaque references, never raw values.
- Return machine-readable error codes plus actionable English messages.
- Require an explicit locale only for human-facing output.

## Implemented MCP tools (current control plane)

### Discovery and inventory

- `capabilities.get`
- `template.list`
- `template.get`
- `docs.search`

### Instances and execution

- `instance.create`
- `instance.get`
- `instance.exec`
- `instance.stop`
- `instance.destroy`
- `instance.extend_ttl`

### Files and artifacts

- `file.upload.begin`
- `file.upload.complete`
- `artifact.export`
- `artifact.list`
- `artifact.download`

### Jobs and desktops

- `job.get`
- `job.list`
- `job.cancel`
- `job.logs`
- `desktop.open`
- `desktop.close`

### Planned intent-level tools

- `image.generate`
- `blender.create_asset`
- `software.test`
- `browser.perform_task`
- `task.delegate`

The implemented server deliberately returns `capability_unavailable` for
desktop operations until a desktop runtime is validated. Intent-level tools
are not registered yet: clients must compose the current instance, command,
job, upload, and artifact tools instead of assuming these names exist.

## Example: create an instance

```json
{
  "template": "dev-linux",
  "resources": {
    "cpu": 4,
    "memory_mb": 8192,
    "gpu": { "vendor": "any", "count": 1, "required": false }
  },
  "ttl_seconds": 3600,
  "idempotency_key": "client-task-47"
}
```

```json
{
  "job_id": "job_01J...",
  "instance_id": "ins_01J...",
  "state": "preparing",
  "links": { "status": "double-agent://jobs/job_01J..." }
}
```

## Example: execute a command

```json
{
  "instance_id": "ins_01J...",
  "argv": ["npm", "test"],
  "cwd": "/workspace",
  "timeout_seconds": 900,
  "environment": { "CI": "true" },
  "secret_refs": ["secret/npm-read-token"]
}
```

Command arguments are arrays, not shell strings. The server does not interpolate
secrets into logs or tool responses.

## Example: delegate an outcome

```json
{
  "objective": "Create four concept images and one textured GLB asset.",
  "inputs": ["artifact://brief/scene.md"],
  "constraints": {
    "max_instances": 5,
    "max_duration_seconds": 7200,
    "allow_remote_llm": false
  },
  "output_contract": ["image/png", "model/gltf-binary", "application/json"]
}
```

## Resources

Agent-readable resources provide the wiki promised by the product:

```text
double-agent://docs/quickstart
double-agent://docs/security-model
double-agent://docs/errors
double-agent://capabilities/current
double-agent://templates/{template_id}
double-agent://jobs/{job_id}
double-agent://artifacts/{artifact_id}/metadata
```

English is canonical. A future `locale` capability may expose translated
human-facing documentation, but schemas, identifiers, states, and error codes
remain English for interoperability.

## Errors

Errors use stable codes such as `authentication_failed`, `permission_denied`,
`quota_exceeded`, `capability_unavailable`, `runtime_error`, `job_timeout`,
`artifact_expired`, and `human_action_required`. Responses include a retry hint,
correlation ID, and safe details without leaking host paths or secrets.

## Authorization scopes

Example scopes include `capabilities:read`, `instances:create`,
`instances:exec`, `instances:destroy`, `artifacts:read`, `artifacts:write`,
`jobs:read`, `jobs:cancel`, `delegate:run`, and administrative scopes. Policy
also evaluates ownership, quota, template, device, network, and time limits.

## Compatibility tests

Every certified client must pass discovery, tool listing, tool calls, resource
reads, long-running jobs, cancellation, reconnect, large artifact transfer,
protocol-version negotiation, and localized human-output fallback.
