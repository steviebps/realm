# CLAUDE.md

Guidance for Claude Code (and other AI agents) working in this repository.

## What realm is

`realm` is a feature-flag / configuration platform written in Go. A single
binary is three things at once:

- **Server** — stores flag definitions and serves them over HTTP at
  `/v1/chambers/…`, with an embedded React UI at `/ui/`.
- **CLI** — `realm server` and `realm client {get,put,list,delete}`.
- **Go SDK** — an importable library (`github.com/steviebps/realm/pkg`) that an
  application embeds to read flags, with a local snapshot refreshed by
  background polling.

**North star:** an open-source LaunchDarkly — immediately sourced feature
switches, synchronized across application zones, with progressive rollout.

**Honest current state:** flags are grouped into hierarchical **chambers** that
inherit from their parents; values can be overridden per **application version
range** and rolled out to a **deterministic percentage** of subjects; the SDK
stays current by **polling** (default 15 min) or **real-time streaming**
(opt-in SSE, "immediately sourced"). Not yet built: attribute-based targeting,
cross-replica stream fanout, API-key auth, and audit logging. See
`docs/ARCHITECTURE.md` for the full gap analysis.

## Domain vocabulary

Read `pkg/rule.go`, `pkg/override.go`, `pkg/rollout.go`, `pkg/chamber.go`,
`pkg/context.go`, `pkg/realm.go` — these define the whole model.

- **Rule** — one flag: a `type` (`string` | `number` | `boolean` | `custom`)
  and a `value`. `UnmarshalJSON` strictly validates the value against the type.
- **Override** — an alternate rule value scoped to an app **semver range**
  (`minimumVersion`..`maximumVersion`).
- **Rollout** — an alternate rule value served to a deterministic **percentage**
  of evaluation contexts, bucketed by a key (sticky assignment). Embeds `*Rule`
  like `Override` does.
- **OverrideableRule** — a `Rule` plus its `Overrides` and optional `Rollout`.
  `ValueFor(ruleKey, ec, version)` is the resolver: **version override first,
  then rollout**.
- **EvaluationContext** — per-evaluation targeting info (`Key` for bucketing,
  `Attributes` reserved for future targeting). Travels through the request
  context, not the snapshot.
- **Chamber** — a namespace: `map[ruleName]*OverrideableRule`, with
  `InheritFrom` / `OverwriteFrom`. **ChamberEntry** is the immutable,
  version-bound read view the SDK evaluates against.
- **Realm** — the SDK client object (`NewRealm(WithHttpClient, WithPath,
  WithVersion, WithPollingInterval, WithStreaming)`; `Start`/`Stop`; `Bool`/
  `String`/`Float64`/`CustomValue`; `NewContext` / `NewContextWithEvaluation`).
  Refresh is either **polling** (default) or **streaming** (`WithStreaming(true)`
  → SSE `GET /v1/chambers/<path>?watch=true`, with polling fallback). The server
  pushes changes via an in-process **broker** (`http/broker.go`, notified after
  each write). See `docs/ARCHITECTURE.md`.

## Architecture map

| Path | Role |
|---|---|
| `main.go` | Entry point → `cmd.Execute()` |
| `cmd/` | Cobra CLI: `root.go`, `server.go`, `config.go`, `client*.go` |
| `pkg/` | Core domain + SDK (package `realm`) |
| `pkg/storage/` | `Storage` interface + backends (`file`, `boltdb`, `bigcache`, `gcs`) and wrappers (`cacheable`, `inheritable`); backends self-register in `StorageOptions` |
| `client/http.go` | `HttpClient` used by both CLI and SDK |
| `http/` | Server handler (`handler.go`), request mapping (`agent.go`), UI embedding (`assets.go` behind the `ui` build tag; `assets_stub.go` fallback), `realm-ui/` React app |
| `api/response.go` | Shared HTTP response envelope |
| `trace/otel.go` | OpenTelemetry setup |
| `helper/logging/` | zerolog `TracedLogger` (context-aware, trace-correlated) |
| `utils/` | JSON read/write + path helpers |

## Common commands

Prefer the `Makefile` targets:

```bash
make build        # go build (no UI)
make build-ui     # build React UI, then go build -tags=ui (UI embedded)
make test         # go test ./...
make test-race    # go test -race ./...   (CI's gate)
make lint         # golangci-lint run
make fmt          # gofmt -w
make vet          # go vet ./...
make run          # go run . server --dev   (bigcache, port 8080)
make tidy         # go mod tidy
```

The server needs a config unless in dev mode: `realm server --config ./configs/realm.json`.

## Conventions to follow

- **Logging is context-first.** Use `logging.Ctx(ctx)` and
  `logger.InfoCtx(ctx)` / `ErrorCtx(ctx)` — never the bare `log` package or
  `fmt.Println`. Logs are trace-correlated.
- **Wrap meaningful work in an OTel span:** `ctx, span := tracer.Start(ctx, "…")`.
- **SDK config uses functional options** (`WithX(...) RealmOption`). Extend that
  pattern rather than adding constructor parameters.
- **Domain types validate in `UnmarshalJSON`.** New rule capabilities (like
  `Rollout`) embed `*Rule` so the value is type-checked, and validate their own
  invariants there. Give them json tags so they round-trip through the server
  (the handler just unmarshals → re-marshals; see below).
- **Storage backends self-register** in `pkg/storage/storage.go`'s
  `StorageOptions` map via a `StorageCreator` factory. `ValidatePath` rejects
  `..`.
- **Keep exported godoc comments** — the codebase has good doc discipline; match
  it.

## Recipes

- **Add a rule capability (like Rollout):** define the type in `pkg/` embedding
  `*Rule`; add a json-tagged field to `OverrideableRule`; parse + validate it in
  `OverrideableRule.UnmarshalJSON`; fold it into `ValueFor`. No server change is
  needed — `http/handler.go` persists chambers by unmarshaling into
  `realm.Chamber` and re-marshaling, so tagged fields round-trip automatically.
- **Add a storage backend:** implement `storage.Storage`, register it in
  `StorageOptions` (and `SourcableStorageOptions` if it can be a cache source).
- **Add a CLI subcommand:** add a `*cobra.Command` in `cmd/`, register it in an
  `init()` with `rootCmd.AddCommand(...)`.

## Testing norms

- Standard library `testing` only (no testify/mock). **Table-driven** is the
  norm; use `t.Run(name, …)` for subtests.
- Run with `-race` before finishing (`make test-race`) — it is CI's gate.
- Add `RunParallel` benchmarks for hot evaluation paths (see `pkg/rule_test.go`,
  `pkg/rollout_test.go`).
- Test data is inlined as literals; there is no `testdata/` fixture convention.

## Verifying changes

For anything touching evaluation or the server, don't stop at unit tests — drive
it end to end: `make run`, `PUT` a chamber via `realm client put` or curl, `GET`
it back, and exercise the SDK path. See the verification section of any change
plan and `docs/ARCHITECTURE.md`.
