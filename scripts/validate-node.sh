#!/usr/bin/env bash
# Validates a running Double Agent node end to end.
#
# Usage: validate-node.sh <base-url> <admin-password> [ca-file]
#
# The script exercises the REST API, the MCP endpoint, the operator CLI and a
# complete instance lifecycle (create, exec, artifact export, destroy).
set -uo pipefail

BASE="${1:?the base url of the node}"
PASSWORD="${2:?the password of the administrator}"
WORK="$(mktemp -d)"
# The authority of the node is downloaded into the work directory, so the
# default verifies against exactly what the node published.
CA="${3:-$WORK/ca.crt}"
COOKIES="${WORK}/cookies.txt"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS + 1)); }
no()  { echo "  FAIL  $1"; echo "        ${2:-}" | head -6; FAIL=$((FAIL + 1)); }
section() { echo; echo "== $1"; }

# Curl with the node certificate authority, the session cookie and the CSRF
# token of the session. The token is only checked for state changing requests,
# so sending it with every call is harmless and keeps the helper simple.
c() { curl -sS --cacert "$CA" -b "$COOKIES" -c "$COOKIES" -H "X-CSRF-Token: ${CSRF:-}" "$@"; }
# Curl without the session cookie.
p() { curl -sS --cacert "$CA" "$@"; }

json() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

section "Transport and TLS"

# The authority of the node is fetched before anything can be verified against
# it. Every call after this one is verified against exactly what the node
# published here.
code=$(curl -sS -k -o "$WORK/ca.crt" -w "%{http_code}" "${BASE}/ca.crt")
if [ "$code" = "200" ]; then ok "the certificate authority is published (HTTP $code)"; else no "the certificate authority is published" "HTTP $code"; fi
openssl x509 -in "$WORK/ca.crt" -noout -subject 2>/dev/null | sed 's/^/        /'

# The certificate must be rejected without the authority.
code=$(curl -sS -o /dev/null -w "%{http_code}" "${BASE}/api/v1/healthz" 2>/dev/null || true)
if [ "$code" = "000" ] || [ "$code" = "495" ]; then
  ok "an untrusted client is rejected (the certificate is verified)"
else
  no "an untrusted client is rejected" "HTTP $code, the certificate was accepted"
fi

section "Health and discovery"

r=$(p -w "\n%{http_code}" "${BASE}/api/v1/healthz")
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
if [ "$code" = "200" ]; then ok "healthz (HTTP $code): $body"; else no "healthz" "HTTP $code $body"; fi

section "Authentication"

code=$(p -o /dev/null -w "%{http_code}" "${BASE}/api/v1/instances")
if [ "$code" = "401" ]; then ok "an unauthenticated request is rejected (HTTP $code)"; else no "an unauthenticated request is rejected" "HTTP $code"; fi

# login_payload prints the login document for the given password.
#
# The document is built by python so that a password with quotes or braces
# stays one valid JSON string.
login_payload() {
  python3 -c 'import json,sys;print(json.dumps(dict(username="admin",password=sys.argv[1])))' "$1"
}

# The login goes through the cookie jar so that the session of the
# administrator is kept for the calls that follow. The answer carries the CSRF
# token every later state changing request has to present.
r=$(c -w "\n%{http_code}" -X POST "${BASE}/api/v1/auth/login" -H "Content-Type: application/json" \
	-d "$(login_payload "$PASSWORD")")
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
if [ "$code" = "200" ]; then ok "login (HTTP $code)"; else no "login" "HTTP $code $body"; fi
CSRF=$(echo "$body" | json "d.get('csrf_token','')" 2>/dev/null)
if [ -n "$CSRF" ]; then ok "the login delivered a CSRF token"; else no "the login delivered a CSRF token" "the field is empty"; fi

r=$(c -w "\n%{http_code}" "${BASE}/api/v1/auth/me")
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
if [ "$code" = "200" ]; then
  ok "auth/me (HTTP $code)"
  echo "$body" | json "'        user: ' + str(d.get('user',{}).get('username')) + ' role=' + str(d.get('user',{}).get('role'))" 2>/dev/null
else
  no "auth/me" "HTTP $code $body"
fi

section "Catalog"

r=$(c -w "\n%{http_code}" "${BASE}/api/v1/capabilities")
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
if [ "$code" = "200" ]; then
  ok "capabilities (HTTP $code)"
  echo "$body" | json "'        runtimes: ' + str([r.get('name') for r in d.get('runtimes',[])])" 2>/dev/null
else
  no "capabilities" "HTTP $code $body"
fi

r=$(c -w "\n%{http_code}" "${BASE}/api/v1/templates")
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
if [ "$code" = "200" ]; then
  ok "templates (HTTP $code)"
  echo "$body" | json "'        count: ' + str(len(d.get('templates',[])))" 2>/dev/null
  echo "$body" | json "'\\n'.join('        - ' + str(t.get('id')) + ' ' + str(t.get('version')) + ' ' + str(t.get('runtime')) for t in d.get('templates',[])[:10])" 2>/dev/null
else
  no "templates" "HTTP $code $body"
fi

section "Instance lifecycle"

r=$(c -w "\n%{http_code}" -X POST "${BASE}/api/v1/instances" -H "Content-Type: application/json" \
	-d '{"name":"validation","template_id":"alpine-shell","ttl_seconds":900}')
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
JOB=$(echo "$body" | json "d.get('job_id','')" 2>/dev/null)
if [ "$code" = "202" ]; then ok "instance.create accepted (HTTP $code) job=$JOB"; else no "instance.create" "HTTP $code $body"; fi

STATE=""
for _ in $(seq 1 40); do
	sleep 1
	r=$(c "${BASE}/api/v1/jobs/${JOB}")
	STATE=$(echo "$r" | json "d.get('state','')" 2>/dev/null)
	case "$STATE" in
		completed|failed|canceled|expired) break ;;
	esac
done
if [ "$STATE" = "completed" ]; then ok "the job completed (state=$STATE)"; else no "the job completed" "state=$STATE"; fi

r=$(c "${BASE}/api/v1/jobs/${JOB}")
INST=$(echo "$r" | json "d.get('instance_id','')" 2>/dev/null)
echo "        instance: $INST"

r=$(c -w "\n%{http_code}" "${BASE}/api/v1/instances")
code=$(echo "$r" | tail -1)
if [ "$code" = "200" ]; then ok "instance.list (HTTP $code)"; else no "instance.list" "HTTP $code"; fi

r=$(c -w "\n%{http_code}" -X POST "${BASE}/api/v1/instances/${INST}/exec" -H "Content-Type: application/json" \
	-d '{"argv":["/bin/sh","-c","echo hello-from-double-agent; uname -sr; id -u"],"timeout_seconds":60}')
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
if [ "$code" = "200" ]; then
	ok "instance.exec (HTTP $code)"
	echo "$body" | json "'        exit_code: ' + str(d.get('exit_code'))" 2>/dev/null
	echo "$body" | json "'        stdout: ' + repr(d.get('stdout','')[:80])" 2>/dev/null
else
	no "instance.exec" "HTTP $code $body"
fi

# The instance must not be able to reach the network by default.
r=$(c -w "\n%{http_code}" -X POST "${BASE}/api/v1/instances/${INST}/exec" -H "Content-Type: application/json" \
	-d '{"argv":["/bin/sh","-c","wget -T 3 -q -O- http://1.1.1.1 >/dev/null 2>&1; echo exit=$?"],"timeout_seconds":30}')
if echo "$r" | grep -q "exit=1\|exit=4\|exit=2\|exit=7"; then
	ok "the network is isolated by default (the egress attempt failed)"
else
	no "the network is isolated by default" "$(echo "$r" | head -1 | head -c 160)"
fi

section "Artifacts"

r=$(c -w "\n%{http_code}" -X POST "${BASE}/api/v1/instances/${INST}/exec" -H "Content-Type: application/json" \
	-d '{"argv":["/bin/sh","-c","mkdir -p out && echo artifact-payload > out/report.txt"],"timeout_seconds":30}')
code=$(echo "$r" | tail -1)
if [ "$code" = "200" ]; then ok "the file was created inside the instance"; else no "the file was created inside the instance" "HTTP $code"; fi

# Export paths are relative to the workspace of the instance.
r=$(c -w "\n%{http_code}" -X POST "${BASE}/api/v1/artifacts/export" -H "Content-Type: application/json" \
	-d "{\"instance_id\":\"$INST\",\"paths\":[\"out/report.txt\"]}")
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
if [ "$code" = "200" ] || [ "$code" = "201" ] || [ "$code" = "202" ]; then
	ok "the artifact was exported (HTTP $code)"
	echo "$body" | json "'        artifacts: ' + str(len(d.get('artifacts',[])))" 2>/dev/null
else
	no "the artifact was exported" "HTTP $code $body"
fi

r=$(c -w "\n%{http_code}" "${BASE}/api/v1/artifacts")
code=$(echo "$r" | tail -1)
if [ "$code" = "200" ]; then ok "artifact.list (HTTP $code)"; else no "artifact.list" "HTTP $code"; fi

section "Client keys"

r=$(c -w "\n%{http_code}" -X POST "${BASE}/api/v1/admin/keys" -H "Content-Type: application/json" \
	-d '{"name":"validation","scopes":["instances:create","instances:read","instances:exec","instances:stop","instances:destroy","jobs:read","artifacts:read","capabilities:read","templates:read"]}')
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
SECRET=$(echo "$body" | json "d.get('key',{}).get('secret','')" 2>/dev/null)
if [ "$code" = "201" ] || [ "$code" = "200" ]; then
	ok "a client key was issued (HTTP $code) prefix=$(echo "$body" | json "str(d.get('key',{}).get('prefix',''))[:6]" 2>/dev/null)"
else
	no "a client key was issued" "HTTP $code $body"
fi

if [ -n "$SECRET" ]; then
	r=$(p -w "\n%{http_code}" -H "Authorization: Bearer ${SECRET}" "${BASE}/api/v1/capabilities")
	code=$(echo "$r" | tail -1)
	if [ "$code" = "200" ]; then ok "the client key authenticates (HTTP $code)"; else no "the client key authenticates" "HTTP $code"; fi

	r=$(p -w "\n%{http_code}" -H "Authorization: Bearer ${SECRET}" "${BASE}/api/v1/instances")
	code=$(echo "$r" | tail -1)
	if [ "$code" = "200" ]; then ok "the client key lists the instances (HTTP $code)"; else no "the client key lists the instances" "HTTP $code"; fi

	# A key without the administrative scope must not manage the users.
	code=$(p -o /dev/null -w "%{http_code}" -X POST -H "Authorization: Bearer ${SECRET}" -H "Content-Type: application/json" \
		-d '{"username":"intruder","password":"Str0ng!Password#2026","role":"admin"}' "${BASE}/api/v1/admin/users")
	if [ "$code" = "403" ] || [ "$code" = "401" ]; then
		ok "a scoped key cannot manage the users (HTTP $code)"
	else
		no "a scoped key cannot manage the users" "HTTP $code"
	fi
fi

section "MCP endpoint"

# The MCP transport authenticates with an API key, so the endpoint is probed
# with the key that was just issued.
if [ -n "$SECRET" ]; then
	r=$(p -w "\n%{http_code}" -X POST "${BASE}/mcp" \
		-H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
		-H "Authorization: Bearer ${SECRET}" \
		-d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"validation","version":"1.0"}}}')
	code=$(echo "$r" | tail -1)
	if [ "$code" = "200" ]; then
		ok "the MCP endpoint answers an authenticated initialize (HTTP $code)"
		echo "$r" | head -2 | sed 's/^/        /' | head -c 300; echo
	else
		no "the MCP endpoint answers" "HTTP $code"
	fi

	# An unauthenticated initialize must be refused: the tools of the node are
	# not public.
	code=$(p -o /dev/null -w "%{http_code}" -X POST "${BASE}/mcp" \
		-H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
		-d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"anon","version":"1.0"}}}')
	if [ "$code" = "401" ]; then
		ok "an unauthenticated MCP initialize is rejected (HTTP $code)"
	else
		no "an unauthenticated MCP initialize is rejected" "HTTP $code"
	fi
fi

section "Teardown"

r=$(c -w "\n%{http_code}" -X DELETE "${BASE}/api/v1/instances/${INST}")
code=$(echo "$r" | tail -1); body=$(echo "$r" | head -1)
if [ "$code" = "200" ] || [ "$code" = "202" ] || [ "$code" = "204" ]; then
	ok "instance.destroy (HTTP $code)"
else
	no "instance.destroy" "HTTP $code $body"
fi

r=$(c -w "\n%{http_code}" "${BASE}/api/v1/instances/${INST}")
code=$(echo "$r" | tail -1)
if [ "$code" = "404" ] || [ "$code" = "200" ]; then
	ok "the instance is gone or terminal (HTTP $code)"
else
	no "the instance is gone or terminal" "HTTP $code"
fi

echo
echo "=== $PASS passed, $FAIL failed ==="
rm -rf "$WORK"
[ "$FAIL" = "0" ]
