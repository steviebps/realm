#!/usr/bin/env bash
# End-to-end checks against a real realm server: the CLI, the HTTP API, the
# Go SDK, the TypeScript SDK, graceful shutdown, and a server bootstrapped by
# a config-declared admin key.
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

status_as() { # token method url [body]
  curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $1" -X "$2" "$3" ${4:+-d "$4"}
}

wait_ready() { # addr; the server is $pid
  for _ in $(seq 1 50); do
    [ "$(status GET "$1/readyz")" = 200 ] && return 0
    kill -0 "$pid" 2>/dev/null || fail "server exited during startup"
    sleep 0.1
  done
  fail "server never became ready"
}

echo "== starting $bin server --dev on $port"
"$bin" server --dev --port "$port" >"$log" 2>&1 &
pid=$!
wait_ready "$addr"
[ "$(status GET "$addr/healthz")" = 200 ] || fail "/healthz"

echo "== CLI"
cat >"$work/chamber.json" <<'EOF'
{"rules":{"beta":{"type":"boolean","value":false,"targeting":[{"value":true,"clauses":[{"attribute":"key","op":"in","values":["vip"]}]}]}}}
EOF
"$bin" client put /e2e/ -f "$work/chamber.json" --address "$addr" --notraces >/dev/null
if "$bin" client put /e2e/ --address "$addr" --notraces >/dev/null 2>&1; then
  fail "client put without --file replaced an existing chamber"
fi
"$bin" client get /e2e/ --address "$addr" --notraces | grep -q '"beta"' || fail "client get"

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

echo "== admin key"
auth_port=$((port + 1))
auth_addr="http://127.0.0.1:${auth_port}"
# The config refers to the key by environment variable, as deployments do.
cat >"$work/auth.json" <<EOF
{"server":{"storage":"bigcache","options":{},"port":"${auth_port}","inheritable":true,
  "auth":{"adminKey":"\${REALM_ADMIN_KEY}"}}}
EOF

# An unset or short key must stop the server, not quietly turn auth off.
for bad in "" short; do
  REALM_ADMIN_KEY=$bad "$bin" server --config "$work/auth.json" >"$work/bad.log" 2>&1 &
  badpid=$!
  for _ in $(seq 1 50); do
    kill -0 "$badpid" 2>/dev/null || break
    sleep 0.1
  done
  if kill -0 "$badpid" 2>/dev/null; then
    kill "$badpid"
    fail "server started with admin key '$bad'"
  fi
  if wait "$badpid"; then fail "server exited 0 with admin key '$bad'"; fi
  grep -q 'adminKey' "$work/bad.log" || fail "admin key '$bad' refused without naming adminKey: $(cat "$work/bad.log")"
done

admin=$(openssl rand -hex 32)
REALM_ADMIN_KEY=$admin "$bin" server --config "$work/auth.json" >>"$log" 2>&1 &
pid=$!
wait_ready "$auth_addr"

keys() { # token args...
  REALM_TOKEN=$1 "$bin" client keys "${@:2}" --address "$auth_addr" --notraces
}
put_as() { # token path
  REALM_TOKEN=$1 "$bin" client put "$2" -f "$work/chamber.json" --address "$auth_addr" --notraces >/dev/null 2>&1
}

[ "$(status GET "$auth_addr/v1/chambers/")" = 401 ] || fail "no credentials not refused"
[ "$(status_as "${admin}x" GET "$auth_addr/v1/keys")" = 401 ] || fail "wrong admin key not refused"
put_as "$admin" /staging/ || fail "admin key could not write a chamber"

token=$(keys "$admin" create --name e2e --access write --path /prod/ 2>"$work/create.err") ||
  fail "admin key could not create a key: $(cat "$work/create.err")"
case $token in realm_*_*) ;; *) fail "create printed '$token', want a realm_ token" ;; esac
id=$(sed -n 's/.*(\([0-9a-f]*\)).*/\1/p' "$work/create.err")
[ -n "$id" ] || fail "create named no key ID: $(cat "$work/create.err")"
keys "$admin" list | grep -E "^$id .* write +/prod/ .* key:admin" >/dev/null ||
  fail "minted key not listed as write on /prod/ by key:admin: $(keys "$admin" list)"

put_as "$token" /prod/app/ || fail "minted key could not write in its scope"
REALM_TOKEN=$token "$bin" client get /prod/app/ --address "$auth_addr" --notraces | grep -q '"beta"' ||
  fail "minted key could not read in its scope"
if put_as "$token" /staging/; then fail "minted key wrote outside its scope"; fi
if keys "$token" list >/dev/null 2>&1; then fail "minted key listed keys"; fi
[ "$(status_as "$token" POST "$auth_addr/v1/keys" '{"name":"x","access":"write"}')" = 403 ] ||
  fail "minted key created a key"
curl -s -H "Authorization: Bearer $admin" "$auth_addr/v1/history/prod/app/" | grep -q '"key:e2e"' ||
  fail "history does not attribute the write to key:e2e"

keys "$admin" revoke "$id" 2>/dev/null || fail "admin key could not revoke $id"
[ "$(status_as "$token" GET "$auth_addr/v1/chambers/prod/app/")" = 401 ] || fail "revoked key still works"
[ "$(status_as "$admin" GET "$auth_addr/v1/keys")" = 200 ] || fail "admin key stopped working"

kill -TERM "$pid"
wait "$pid" 2>/dev/null || true
pid=
if grep -qF -e "$admin" -e "${token#realm_*_}" "$log"; then fail "a secret was written to the server log"; fi

echo "PASS"
