#!/bin/bash
# SessionStart hook: make the repo ready to build, test, and lint in
# Claude Code on the web sessions. Runs synchronously so dependencies are in
# place before the agent starts. Idempotent and safe to re-run.
set -uo pipefail

# Only run in the remote (Claude Code on the web) environment.
if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0

echo "[session-start] downloading Go modules..."
go mod download || echo "[session-start] warning: 'go mod download' failed; continuing"

# Warm the build cache so the first 'go build'/'go test' is fast.
echo "[session-start] warming build cache..."
go build ./... >/dev/null 2>&1 || echo "[session-start] warning: 'go build ./...' failed; continuing"

# Ensure golangci-lint is available for 'make lint'. Best-effort: a lint tool
# is not required for the session to function.
if ! command -v golangci-lint >/dev/null 2>&1; then
  echo "[session-start] installing golangci-lint..."
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0 \
    || echo "[session-start] warning: could not install golangci-lint; 'make lint' may be unavailable"
  # go install drops binaries in GOBIN or GOPATH/bin; make sure it's on PATH.
  GOBIN_DIR="$(go env GOBIN)"
  [ -z "$GOBIN_DIR" ] && GOBIN_DIR="$(go env GOPATH)/bin"
  if [ -n "${CLAUDE_ENV_FILE:-}" ] && [ -d "$GOBIN_DIR" ]; then
    echo "export PATH=\"$GOBIN_DIR:\$PATH\"" >> "$CLAUDE_ENV_FILE"
  fi
fi

# Best-effort UI build so binaries built with '-tags=ui' compile. The React
# build is slow and network-dependent, so failures never fail the session.
if [ -d http/realm-ui ]; then
  echo "[session-start] building web UI (best-effort)..."
  ( cd http/realm-ui && npm install && npm run build ) \
    || echo "[session-start] warning: UI build failed; backend-only work is unaffected"
fi

echo "[session-start] done"
exit 0
