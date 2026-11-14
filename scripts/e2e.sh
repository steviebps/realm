#!/usr/bin/env bash
# End-to-end checks against a real realm server: the CLI, the HTTP API,
# subtree reads and watches, the Go SDK, the TypeScript SDK, graceful
# shutdown, and a server bootstrapped by a config-declared admin key.
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
watcher=

cleanup() {
  [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  [ -n "$watcher" ] && kill "$watcher" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

status() { # method url [body]; sends $tok as a bearer key when set
  curl -s -o /dev/null -w '%{http_code}' ${tok:+-H "Authorization: Bearer $tok"} -X "$1" "$2" ${3:+-d "$3"}
}

wait_ready() { # addr; the server is $pid
  for _ in $(seq 1 50); do
    [ "$(status GET "$1/readyz")" = 200 ] && return 0
    kill -0 "$pid" 2>/dev/null || fail "server exited during startup"
    sleep 0.1
  done
  fail "server never became ready"
}

wait_until() { # tenths description command...; fails unless command succeeds within that many tenths of a second
  local n=$1 what=$2
  shift 2
  for _ in $(seq 1 "$n"); do
    "$@" && return 0
    sleep 0.1
  done
  fail "timed out waiting for $what"
}

paths() { # prints the "path" of each subtree entry or event on stdin, in order
  grep -o '"path":"[^"]*"' | cut -d'"' -f4
}

entry() { # path; prints each subtree entry or event data on stdin for path
  sed 's/{"path":/\
&/g' | grep -F "{\"path\":\"$1\""
}

events() { # file; prints "<event> <path>" for each event of a stream saved in file, in order
  awk '/^event: /{e=$2} /^data: /{p=""; if (match($0, /"path":"[^"]*"/)) p=" " substr($0, RSTART+8, RLENGTH-9); print e p}' "$1"
}

wait_exit() { # pid tenths; false if pid still runs after that many tenths of a second
  for _ in $(seq 1 "$2"); do
    kill -0 "$1" 2>/dev/null || return 0
    sleep 0.1
  done
  ! kill -0 "$1" 2>/dev/null
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
"$bin" client put /e2e-layers/child/ --address "$addr" >/dev/null
layers=$(curl -s "$addr/v1/chambers/e2e-layers/child/?inherit=layers")
echo "$layers" | grep -Eq '"status": ?"missing"' && echo "$layers" | grep -Eq '"sources": ?\{\}' ||
  fail "inherit=layers: $layers"

echo "== subtree reads and watches"
# /e2e-tree/tenants/ holds no chamber: its view is built from /e2e-tree/.
tree_root='{"rules":{"beta":{"type":"boolean","value":false},"msg":{"type":"string","value":"root"}}}'
tenant='{"rules":{"msg":{"type":"string","value":"tenant"}}}'
[ "$(status POST "$addr/v1/chambers/e2e-tree/" "$tree_root")" = 201 ] || fail "put /e2e-tree/"
[ "$(status POST "$addr/v1/chambers/e2e-tree/tenants/t-1/" "$tenant")" = 201 ] || fail "put /e2e-tree/tenants/t-1/"
[ "$(status POST "$addr/v1/chambers/e2e-tree/tenants/t-1/loc-1/" '{"rules":{"limit":{"type":"number","value":5}}}')" = 201 ] ||
  fail "put /e2e-tree/tenants/t-1/loc-1/"

tree=$(curl -s "$addr/v1/chambers/e2e-tree/tenants/?subtree=true")
got=$(paths <<<"$tree" | tr '\n' ' ')
[ "$got" = "/e2e-tree/tenants/ /e2e-tree/tenants/t-1/ /e2e-tree/tenants/t-1/loc-1/ " ] ||
  fail "subtree read: want the root, then t-1, then loc-1: $tree"
entry /e2e-tree/tenants/ <<<"$tree" | grep -q '"value":"root"' || fail "subtree root not built from /e2e-tree/: $tree"
entry /e2e-tree/tenants/t-1/ <<<"$tree" | grep '"beta"' | grep -q '"value":"tenant"' ||
  fail "t-1 lacks its own or its inherited flags: $tree"
entry /e2e-tree/tenants/t-1/loc-1/ <<<"$tree" | grep '"limit"' | grep '"beta"' | grep -q '"value":"tenant"' ||
  fail "loc-1 lacks its own or its inherited flags: $tree"
# a missing root is never a 404: its view comes from its ancestors
curl -s "$addr/v1/chambers/e2e-tree/none/?subtree=true" | entry /e2e-tree/none/ | grep -q '"value":"root"' ||
  fail "subtree read of a root without a chamber"
[ "$(status GET "$addr/v1/chambers/e2e-tree/tenants/?subtree=true&inherit=layers")" = 400 ] ||
  fail "subtree=true&inherit=layers not refused"
[ "$(status GET "$addr/v1/chambers/e2e-tree/tenants/?subtree=true&inherit=false")" = 400 ] ||
  fail "subtree=true&inherit=false not refused"

sse="$work/tree.sse"
curl -sN "$addr/v1/chambers/e2e-tree/tenants/?subtree=true&watch=true" >"$sse" &
watcher=$!
wait_until 50 "the subtree stream's synced event" grep -q '^event: synced' "$sse"
want="chamber /e2e-tree/tenants/
chamber /e2e-tree/tenants/t-1/
chamber /e2e-tree/tenants/t-1/loc-1/
synced"
[ "$(events "$sse")" = "$want" ] || fail "subtree snapshot: $(cat "$sse")"
grep -q '^data: {"chambers":3}' "$sse" || fail "synced does not count 3 chambers: $(cat "$sse")"

loc_events() { # n; whether the stream has sent at least n chamber events for loc-1
  [ "$(events "$sse" | grep -c '^chamber /e2e-tree/tenants/t-1/loc-1/$')" -ge "$1" ]
}
# a parent's change reaches every view below it
[ "$(status POST "$addr/v1/chambers/e2e-tree/" "${tree_root/false/true}")" = 201 ] || fail "update /e2e-tree/"
wait_until 50 "the parent's change on the subtree stream" loc_events 2
# a write that leaves views unchanged sends nothing; the leaf's change sends the leaf alone
[ "$(status POST "$addr/v1/chambers/e2e-tree/tenants/t-1/" "$tenant")" = 201 ] || fail "rewrite /e2e-tree/tenants/t-1/"
[ "$(status POST "$addr/v1/chambers/e2e-tree/tenants/t-1/loc-1/" '{"rules":{"limit":{"type":"number","value":6}}}')" = 201 ] ||
  fail "update /e2e-tree/tenants/t-1/loc-1/"
wait_until 50 "the leaf's change on the subtree stream" loc_events 3
[ "$(status DELETE "$addr/v1/chambers/e2e-tree/tenants/t-1/loc-1/")" = 204 ] || fail "delete /e2e-tree/tenants/t-1/loc-1/"
wait_until 50 "the leaf's deletion on the subtree stream" grep -q '^event: deleted' "$sse"
want="$want
chamber /e2e-tree/tenants/
chamber /e2e-tree/tenants/t-1/
chamber /e2e-tree/tenants/t-1/loc-1/
chamber /e2e-tree/tenants/t-1/loc-1/
deleted /e2e-tree/tenants/t-1/loc-1/"
[ "$(events "$sse")" = "$want" ] || fail "subtree stream: $(events "$sse")"
data=$(grep '^data: ' "$sse")
entry /e2e-tree/tenants/t-1/ <<<"$data" | tail -1 | grep -Eq '"beta":\{[^}]*"value":true' ||
  fail "t-1 was not sent the parent's change: $data"
entry /e2e-tree/tenants/t-1/loc-1/ <<<"$data" | grep '"limit"' | tail -1 | grep -q '"value":6' || fail "loc-1 was not sent its change: $data"
kill "$watcher"
wait "$watcher" 2>/dev/null || true
watcher=

echo "== Go SDK"
(cd "$root" && REALM_E2E_ADDRESS="$addr" go test -count=1 -run '^TestE2E' ./pkg/)

echo "== TypeScript SDK"
(cd "$root/sdk/typescript" && REALM_E2E_ADDRESS="$addr" npx vitest run test/e2e.test.ts)

echo "== graceful shutdown with an open stream"
curl -sN "$addr/v1/chambers/e2e/?watch=true" >/dev/null &
stream=$!
sleep 0.5
kill -TERM "$pid"
wait_exit "$pid" 100 || {
  kill -9 "$pid"
  fail "server still running 10s after SIGTERM with a stream open"
}
pid=
wait "$stream" 2>/dev/null || true
grep -q '"message":"shutting down"' "$log" || fail "no shutdown log line"

echo "== admin key"
auth_port=$((port + 1))
auth_addr="http://127.0.0.1:${auth_port}"
export REALM_ADDRESS=$auth_addr
# The config refers to the key by environment variable, as deployments do.
cat >"$work/auth.json" <<EOF
{"server":{"storage":"bigcache","options":{},"port":"${auth_port}","inheritable":true,
  "auth":{"adminKey":"\${REALM_ADMIN_KEY}"}}}
EOF

# An unset key must stop the server, not quietly turn auth off.
REALM_ADMIN_KEY= "$bin" server --config "$work/auth.json" >"$work/bad.log" 2>&1 &
badpid=$!
wait_exit "$badpid" 50 || {
  kill "$badpid"
  fail "server started with an empty admin key"
}
if wait "$badpid"; then fail "server exited 0 with an empty admin key"; fi
grep -q 'adminKey' "$work/bad.log" || fail "empty admin key refused without naming adminKey: $(cat "$work/bad.log")"

admin=$(openssl rand -hex 32)
REALM_ADMIN_KEY=$admin "$bin" server --config "$work/auth.json" >>"$log" 2>&1 &
pid=$!
wait_ready "$auth_addr"

cli() { # token client-args...
  REALM_TOKEN=$1 "$bin" client "${@:2}" --notraces
}
put_as() { # token path
  cli "$1" put "$2" -f "$work/chamber.json" >/dev/null 2>&1
}

[ "$(status GET "$auth_addr/v1/chambers/")" = 401 ] || fail "no credentials not refused"
[ "$(tok=${admin}x status GET "$auth_addr/v1/keys")" = 401 ] || fail "wrong admin key not refused"
put_as "$admin" /staging/ || fail "admin key could not write a chamber"

# A key's scope covers the subtree below it, and nothing above it.
put_as "$admin" /e2e-tree/tenants/t-1/ && put_as "$admin" /e2e-tree/tenants/t-1/loc-1/ ||
  fail "admin key could not write the subtree"
reader=$(cli "$admin" keys create --name e2e-tree --access read --path /e2e-tree/tenants/t-1/ 2>/dev/null) ||
  fail "admin key could not create a read key"
curl -s -H "Authorization: Bearer $reader" "$auth_addr/v1/chambers/e2e-tree/tenants/t-1/?subtree=true" | paths |
  grep -qx /e2e-tree/tenants/t-1/loc-1/ || fail "scoped key could not read the subtree at its scope"
[ "$(tok=$reader status GET "$auth_addr/v1/chambers/e2e-tree/tenants/?subtree=true")" = 403 ] ||
  fail "scoped key read the subtree above its scope"
[ "$(tok=$reader status GET "$auth_addr/v1/chambers/e2e-tree/tenants/?subtree=true&watch=true")" = 403 ] ||
  fail "scoped key watched the subtree above its scope"

token=$(cli "$admin" keys create --name e2e --access write --path /prod/ 2>"$work/create.err") ||
  fail "admin key could not create a key: $(cat "$work/create.err")"
case $token in realm_*_*) ;; *) fail "create printed '$token', want a realm_ token" ;; esac
id=$(sed -n 's/.*(\([0-9a-f]*\)).*/\1/p' "$work/create.err")
[ -n "$id" ] || fail "create named no key ID: $(cat "$work/create.err")"
list=$(cli "$admin" keys list)
grep -Eq "^$id .* write +/prod/ .* key:admin" <<<"$list" ||
  fail "minted key not listed as write on /prod/ by key:admin: $list"

put_as "$token" /prod/app/ || fail "minted key could not write in its scope"
cli "$token" get /prod/app/ | grep -q '"beta"' || fail "minted key could not read in its scope"
if put_as "$token" /staging/; then fail "minted key wrote outside its scope"; fi
if cli "$token" keys list >/dev/null 2>&1; then fail "minted key managed keys"; fi
curl -s -H "Authorization: Bearer $admin" "$auth_addr/v1/history/prod/app/" | grep -q "\"key:e2e ($id)\"" ||
  fail "history does not attribute the write to key:e2e ($id)"

cli "$admin" keys revoke "$id" 2>/dev/null || fail "admin key could not revoke $id"
[ "$(tok=$token status GET "$auth_addr/v1/chambers/prod/app/")" = 401 ] || fail "revoked key still works"
[ "$(tok=$admin status GET "$auth_addr/v1/keys")" = 200 ] || fail "admin key stopped working"

kill -TERM "$pid"
wait "$pid" 2>/dev/null || true
pid=
if grep -qF -e "$admin" -e "${token#realm_*_}" -e "${reader#realm_*_}" "$log"; then fail "a secret was written to the server log"; fi

echo "PASS"
