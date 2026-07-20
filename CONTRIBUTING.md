# Contributing to realm

Thanks for your interest in improving realm. This guide covers local setup, the
day-to-day workflow, and the conventions the codebase follows.

## Prerequisites

- **Go** 1.25+ (CI tests against 1.24.x–1.26.x).
- **Node** 20+ and npm — only needed to build the embedded web UI
  (`http/realm-ui`). Not required for backend-only work.
- **golangci-lint** v2.5+ — for `make lint`
  (https://golangci-lint.run/welcome/install/).

## Getting started

```bash
git clone https://github.com/steviebps/realm
cd realm
make build          # backend binary (UI not embedded)
make run            # start the server in dev mode on :8080
```

`make run` uses `--dev` (in-memory bigcache storage, no config file). To run
against real storage, write a config like `configs/realm.json` and start with:

```bash
./realm server --config ./configs/realm.json
```

Then use the CLI client against it:

```bash
./realm client put   --address http://localhost:8080 <path>   # create an empty chamber at a path
./realm client get   --address http://localhost:8080 <path>
./realm client list  --address http://localhost:8080 <path>
```

Rule content (including rollouts) is set by writing a chamber body to the API or
via the web UI. For example, with the server running:

```bash
curl -X POST http://localhost:8080/v1/chambers/root \
  -H 'Content-Type: application/json' \
  -d '{"rules":{"new-checkout":{"type":"boolean","value":false,
        "rollout":{"type":"boolean","value":true,"percentage":20}}}}'
```

To build with the web UI embedded (as releases do):

```bash
make build-ui       # builds http/realm-ui, then `go build -tags=ui`
```

## Development workflow

Common tasks are in the `Makefile` (`make help` lists them):

| Command | What it does |
|---|---|
| `make test` | `go test ./...` |
| `make test-race` | `go test -race ./...` — **the CI gate** |
| `make lint` | `golangci-lint run` (config in `.golangci.yml`) |
| `make fmt` | `gofmt -w -s .` |
| `make vet` | `go vet ./...` |
| `make bench` | run benchmarks |
| `make tidy` | `go mod tidy` |

**Before opening a PR**, make sure these pass:

```bash
make fmt
make test-race
make lint
```

## Code conventions

These mirror `CLAUDE.md`, which has more detail and recipes:

- **Context-first logging.** Use `logging.Ctx(ctx)` / `logger.InfoCtx(ctx)` —
  never the bare `log` package. Logs are trace-correlated.
- **Wrap meaningful work in an OpenTelemetry span** (`tracer.Start(ctx, "…")`).
- **SDK configuration uses functional options** (`WithX(...) RealmOption`).
- **Domain types validate in `UnmarshalJSON`.** New rule capabilities embed
  `*Rule` (like `Override`/`Rollout`), carry json tags so they round-trip
  through the server, and validate their invariants on unmarshal.
- **Storage backends self-register** in `pkg/storage/storage.go`'s
  `StorageOptions`.
- **Keep exported godoc comments** — match the existing doc discipline.

## Testing conventions

- Standard-library `testing` only; **table-driven** tests with `t.Run` subtests.
- Run `-race` locally; it is what CI enforces.
- Add `RunParallel` benchmarks for hot evaluation paths (see
  `pkg/rule_test.go`, `pkg/rollout_test.go`).
- For changes to evaluation or the server, verify end to end (run the server,
  `PUT`/`GET` a chamber, exercise the SDK), not just unit tests. See the
  verification guidance in `docs/ARCHITECTURE.md`.

## Pull requests

- Keep PRs focused; describe the change and how you verified it.
- Ensure `make test-race` and `make lint` are green.
- CI runs the test matrix (`.github/workflows/test.yml`), lint
  (`.github/workflows/lint.yml`), and CodeQL. Releases are tag-triggered via
  GoReleaser (`.github/workflows/go.yml`).

## Where to start

`docs/ARCHITECTURE.md` has a "Path to LaunchDarkly parity" table listing the
next capabilities (attribute targeting, real-time streaming, API-key auth, audit
log, more SDKs). Those are good first contributions, and the seams for several
already exist.
