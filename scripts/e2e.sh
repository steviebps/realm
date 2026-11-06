#!/usr/bin/env bash
# End-to-end checks against a real realm server: the CLI, the HTTP API, the
# Go SDK, the TypeScript SDK, and graceful shutdown.
#
#   go build -o realm . && (cd sdk/typescript && npm ci) && ./scripts/e2e.sh ./realm
#
# The server log is written to e2e-server.log.
set -euo pipefail

bin=${1:-./realm}
port=${REALM_E2E_PORT:-18090}
addr="http://127.0.0.1:${port}"
log=e2e-server.log
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
pid=

cleanup() {
  [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

status() { # method url [body]
  curl -s -o /dev/null -w '%{http_code}' -X "$1" "$2" ${3:+-d "$3"}
}

echo "== starting $bin server --dev on $port"
"$bin" server --dev --port "$port" >"$log" 2>&1 &
pid=$!
for _ in $(seq 1 50); do
  [ "$(status GET "$addr/readyz")" = 200 ] && break
  kill -0 "$pid" 2>/dev/null || fail "server exited during startup"
  sleep 0.1
done
[ "$(status GET "$addr/readyz")" = 200 ] || fail "server never became ready"
[ "$(status GET "$addr/healthz")" = 200 ] || fail "/healthz"

echo "== CLI"
cat >"$work/chamber.json" <<'EOF'
{"rules":{"beta":{"type":"boolean","value":false,"targeting":[{"value":true,"clauses":[{"attribute":"key","op":"in","values":["vip"]}]}]}}}
EOF
"$bin" client put /e2e/ -f "$work/chamber.json" --address "$addr" >/dev/null
if "$bin" client put /e2e/ --address "$addr" >/dev/null 2>&1; then
  fail "client put without --file replaced an existing chamber"
fi
"$bin" client get /e2e/ --address "$addr" | grep -q '"beta"' || fail "client get"

echo "== HTTP API"
[ "$(status POST "$addr/v1/chambers/e2e/" '{"rulez":{}}')" = 400 ] || fail "unknown field accepted"
code=$(status GET "$addr/v1/chambers/%252F_realm%252Fkeys?inherit=false")
[ "$code" = 400 ] || [ "$code" = 404 ] || fail "reserved path reachable: $code"
curl -s -X POST "$addr/v1/evaluate/e2e/" -d '{"key":"vip"}' | grep -Eq '"value": ?true' || fail "evaluate"

echo "== Go SDK"
(cd "$root" && REALM_E2E_ADDRESS="$addr" go test -count=1 -run '^TestE2E' ./pkg/)

echo "== TypeScript SDK"
(cd "$root/sdk/typescript" && REALM_E2E_ADDRESS="$addr" npx vitest run test/e2e.test.ts)

echo "== graceful shutdown with an open stream"
curl -sN "$addr/v1/chambers/e2e/?watch=true" >/dev/null &
stream=$!
sleep 0.5
kill -TERM "$pid"
for _ in $(seq 1 100); do
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.1
done
if kill -0 "$pid" 2>/dev/null; then
  kill -9 "$pid"
  fail "server still running 10s after SIGTERM with a stream open"
fi
pid=
wait "$stream" 2>/dev/null || true
grep -q '"message":"shutting down"' "$log" || fail "no shutdown log line"

echo "PASS"
