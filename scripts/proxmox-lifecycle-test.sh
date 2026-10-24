#!/usr/bin/env bash
# Disposable Proxmox lifecycle test for the Double Agent control plane.
#
# The script starts a temporary node with the Proxmox runtime enabled, then
# drives one complete lifecycle through the authenticated API: clone from the
# configured template, guest-agent exec, artifact export, destroy. It runs the
# lifecycle twice, because repeatability is the acceptance gate.
#
# Safety rules (see docs/PROXMOX_TEST_PLAN.md):
#   - the script never talks to the Proxmox API itself; every change goes
#     through the control plane, which only deletes guests it created;
#   - the template must be immutable and in the allow list of the
#     configuration;
#   - no other guest is touched.
#
# Required environment:
#   DOUBLE_AGENT_PROXMOX_ENDPOINT        e.g. https://192.168.1.79:8006
#   DOUBLE_AGENT_PROXMOX_NODE            e.g. pve
#   DOUBLE_AGENT_PROXMOX_STORAGE         e.g. local-lvm
#   DOUBLE_AGENT_PROXMOX_TEMPLATE_VMID   e.g. 301
#   DOUBLE_AGENT_ADMIN_PASSWORD          password of the initial administrator
#   DOUBLE_AGENT_PROXMOX_PASSWORD        Proxmox password, or:
#   DOUBLE_AGENT_PROXMOX_TOKEN_ID + DOUBLE_AGENT_PROXMOX_TOKEN_SECRET
#
# Optional environment:
#   DOUBLE_AGENT_PROXMOX_USERNAME        user of the password credential
#                                        (default root@pam)
#   DOUBLE_AGENT_PROXMOX_SMOKE_VMID      running disposable guest (da-) for the
#                                        live guest-agent/acceleration doctor
#                                        check; skipped when unset
#   DOUBLE_AGENT_PROXMOX_BRIDGE          bridge for the clones (default vmbr0)
#   DOUBLE_AGENT_PROXMOX_INSECURE        "true" to skip certificate
#                                        verification (trusted LAN only)
set -uo pipefail

need() { eval "val=\${$1:-}"; if [ -z "$val" ]; then echo "proxmox-test: $1 is required" >&2; exit 2; fi; }
need DOUBLE_AGENT_PROXMOX_ENDPOINT
need DOUBLE_AGENT_PROXMOX_NODE
need DOUBLE_AGENT_PROXMOX_STORAGE
need DOUBLE_AGENT_PROXMOX_TEMPLATE_VMID
need DOUBLE_AGENT_ADMIN_PASSWORD
if [ -z "${DOUBLE_AGENT_PROXMOX_PASSWORD:-}" ] && [ -z "${DOUBLE_AGENT_PROXMOX_TOKEN_ID:-}" ]; then
  echo "proxmox-test: DOUBLE_AGENT_PROXMOX_PASSWORD or DOUBLE_AGENT_PROXMOX_TOKEN_ID with DOUBLE_AGENT_PROXMOX_TOKEN_SECRET is required" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/build/double-agentd"
if [ ! -x "$BIN" ]; then
  echo "proxmox-test: build the daemon first: go build -o build/double-agentd ./cmd/double-agentd" >&2
  exit 2
fi

BASE="https://127.0.0.1:7444"
WORK="$(mktemp -d)"
COOKIES="$WORK/cookies.txt"
DAEMON_PID=""
PASS=0
FAIL=0
ok() { echo "  PASS  $1"; PASS=$((PASS + 1)); }
no() { echo "  FAIL  $1"; echo "        ${2:-}" | head -6; FAIL=$((FAIL + 1)); }
section() { echo; echo "== $1"; }

cleanup() {
  if [ -n "$DAEMON_PID" ]; then
    kill "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

c() { curl -sS --cacert "$WORK/ca.crt" -b "$COOKIES" -c "$COOKIES" -H "X-CSRF-Token: ${CSRF:-}" "$@"; }
json() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

section "Node"
cat > "$WORK/config.yaml" <<YAML
data_dir: $WORK/data
log_level: info
listen:
  address: 127.0.0.1
  port: 7444
  tls:
    enabled: true
runtime:
  default: proxmox
  proxmox:
    enabled: true
    endpoint: ${DOUBLE_AGENT_PROXMOX_ENDPOINT}
    username: ${DOUBLE_AGENT_PROXMOX_USERNAME:-root@pam}
    node: ${DOUBLE_AGENT_PROXMOX_NODE}
    storage: ${DOUBLE_AGENT_PROXMOX_STORAGE}
    template_vmid: ${DOUBLE_AGENT_PROXMOX_TEMPLATE_VMID}
    allowed_templates: [${DOUBLE_AGENT_PROXMOX_TEMPLATE_VMID}]
    bridge: ${DOUBLE_AGENT_PROXMOX_BRIDGE:-vmbr0}
    insecure: ${DOUBLE_AGENT_PROXMOX_INSECURE:-false}
discovery:
  enabled: false
YAML
# The credentials stay in the environment of the daemon; the file keeps only
# references and placement holders.

"$BIN" serve --config "$WORK/config.yaml" >"$WORK/daemon.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 60); do
  curl -sk -o /dev/null "$BASE/api/v1/healthz" && break
  sleep 1
done
curl -sk -o "$WORK/ca.crt" "$BASE/ca.crt"
r=$(c -w "\n%{http_code}" -X POST "$BASE/api/v1/auth/login" -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys;print(json.dumps(dict(username="admin",password=sys.argv[1])))' "$DOUBLE_AGENT_ADMIN_PASSWORD")")
code=$(echo "$r" | tail -1)
if [ "$code" = "200" ]; then ok "the node is up and the administrator can log in"; else no "login" "HTTP $code"; tail -20 "$WORK/daemon.log"; exit 1; fi
CSRF=$(echo "$r" | head -1 | json "d.get('csrf_token','')" 2>/dev/null)

# The doctor runs against the served node, so the certificate authority of the
# temporary data directory already exists. A FAIL on any line fails the run.
"$BIN" doctor --config "$WORK/config.yaml" --data-dir "$WORK/data" >"$WORK/doctor-before.txt" 2>&1
status=$?
sed 's/^/        /' "$WORK/doctor-before.txt"
if [ "$status" = "0" ]; then ok "doctor before the run"; else no "doctor before the run" "exit $status"; fi

if [ -n "${DOUBLE_AGENT_PROXMOX_SMOKE_VMID:-}" ]; then
  section "Doctor with a running guest"
  "$BIN" doctor --config "$WORK/config.yaml" --data-dir "$WORK/data" \
    --proxmox-vmid "$DOUBLE_AGENT_PROXMOX_SMOKE_VMID" >"$WORK/doctor-guest.txt" 2>&1
  status=$?
  sed 's/^/        /' "$WORK/doctor-guest.txt"
  if [ "$status" = "0" ]; then ok "doctor verified the guest agent of guest $DOUBLE_AGENT_PROXMOX_SMOKE_VMID"; else no "doctor with --proxmox-vmid" "exit $status"; fi
fi

# lifecycle runs the create → exec → export → destroy path once and reports
# the outcome under the given label.
lifecycle() {
  label="$1"
  section "Lifecycle: $label"

  # give_up destroys the instance of a failed step, so the guest of a failed
  # run does not linger on the node until its TTL runs out.
  give_up() {
    local inst="${INST:-}"
    if [ -z "$inst" ] && [ -n "${JOB:-}" ]; then
      inst=$(c "$BASE/api/v1/jobs/$JOB" | json "d.get('instance_id','')" 2>/dev/null)
    fi
    if [ -n "$inst" ]; then
      c -o /dev/null -X DELETE "$BASE/api/v1/instances/$inst"
      echo "        the failed instance $inst was destroyed"
    fi
    return 1
  }

  r=$(c -w "\n%{http_code}" -X POST "$BASE/api/v1/instances" -H "Content-Type: application/json" \
    -d '{"name":"lifecycle-'"$label"'","template_id":"proxmox-linux","ttl_seconds":900}')
  code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
  JOB=$(echo "$body" | json "d.get('job_id','')" 2>/dev/null)
  if [ "$code" = "202" ]; then ok "instance.create accepted (job=$JOB)"; else no "instance.create" "HTTP $code $body"; return 1; fi

  STATE=""
  for _ in $(seq 1 180); do
    sleep 2
    r=$(c "$BASE/api/v1/jobs/$JOB")
    STATE=$(echo "$r" | json "d.get('state','')" 2>/dev/null)
    case "$STATE" in completed|failed|canceled|expired) break ;; esac
  done
  if [ "$STATE" = "completed" ]; then ok "the clone started and the job completed"; else
    no "the job completed" "state=$STATE"; c "$BASE/api/v1/jobs/$JOB" | json "d.get('error_message','')" 2>/dev/null; give_up; return 1; fi

  INST=$(c "$BASE/api/v1/jobs/$JOB" | json "d.get('instance_id','')" 2>/dev/null)
  echo "        instance: $INST"

  # The first boot of a clone under software emulation can take many minutes
  # before the guest agent answers. The backend waits a bounded time inside
  # every exec and then refuses with capability_unavailable, so the exec is
  # retried here: twelve attempts of about two minutes each cover the slowest
  # first boot observed on the pilot host.
  UP=""
  for attempt in $(seq 1 12); do
    r=$(c -w "\n%{http_code}" -X POST "$BASE/api/v1/instances/$INST/exec" -H "Content-Type: application/json" \
      -d '{"argv":["uname","-sr"],"timeout_seconds":120}')
    code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
    if [ "$code" = "200" ]; then UP=1; break; fi
    if ! echo "$body" | grep -q capability_unavailable; then break; fi
    echo "        attempt $attempt: the guest agent is not up yet, waiting..."
    sleep 30
  done
  if [ -n "$UP" ]; then
    ok "guest-agent exec: $(echo "$body" | json "repr(d.get('stdout','').strip())" 2>/dev/null)"
  else
    no "guest-agent exec" "HTTP $code $body"; give_up; return 1; fi

  r=$(c -w "\n%{http_code}" -X POST "$BASE/api/v1/instances/$INST/exec" -H "Content-Type: application/json" \
    -d '{"argv":["/bin/sh","-c","mkdir -p /root/out && echo proxmox-artifact > /root/out/report.txt"],"timeout_seconds":60}')
  code=$(echo "$r" | tail -1)
  if [ "$code" = "200" ]; then ok "the artifact was written inside the guest"; else no "write the artifact" "HTTP $code"; give_up; return 1; fi

  # Export paths are relative to the workspace; the guest agent reads the
  # file out of the guest and the control plane records its digest.
  r=$(c -w "\n%{http_code}" -X POST "$BASE/api/v1/artifacts/export" -H "Content-Type: application/json" \
    -d "{\"instance_id\":\"$INST\",\"paths\":[\"root/out/report.txt\"]}")
  code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
  if [ "$code" = "200" ] || [ "$code" = "201" ] || [ "$code" = "202" ]; then
    ok "the artifact left the guest ($(echo "$body" | json "str(len(d.get('artifacts',[])))" 2>/dev/null) file)"
  else
    no "export the artifact" "HTTP $code $body"; give_up; return 1; fi

  r=$(c -w "\n%{http_code}" -X DELETE "$BASE/api/v1/instances/$INST")
  code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
  if [ "$code" = "200" ] || [ "$code" = "202" ] || [ "$code" = "204" ]; then ok "instance.destroy"; else no "instance.destroy" "HTTP $code $body"; return 1; fi

  STATE=""
  for _ in $(seq 1 60); do
    sleep 2
    r=$(c "$BASE/api/v1/instances/$INST")
    STATE=$(echo "$r" | json "d.get('state','')" 2>/dev/null)
    [ "$STATE" = "destroyed" ] && break
  done
  if [ "$STATE" = "destroyed" ]; then ok "the instance reached the destroyed state"; else no "the instance is destroyed" "state=$STATE"; return 1; fi
}

lifecycle "pass-1"
lifecycle "pass-2"

section "Doctor after the run"
"$BIN" doctor --config "$WORK/config.yaml" --data-dir "$WORK/data" >"$WORK/doctor-after.txt" 2>&1
status=$?
sed 's/^/        /' "$WORK/doctor-after.txt"
if [ "$status" = "0" ]; then ok "doctor after the run"; else no "doctor after the run" "exit $status"; fi

echo
echo "=== $PASS passed, $FAIL failed ==="
echo "sanitized daemon log (no credentials):"
grep -v -i 'password\|token\|secret\|ticket' "$WORK/daemon.log" | tail -20 | sed 's/^/        /'
[ "$FAIL" = "0" ]
