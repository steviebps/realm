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
inherit from their parents. Each flag has a default value and ordered
**targeting rules** matched against context attributes (tenant, location,
environment, service, semver `version`, `buildSha`, …), each optionally
limited to a **deterministic percentage rollout**. The SDK stays current by
**polling** (default 15 min) or **real-time streaming** (SSE). The server
offers ETag optimistic concurrency, per-chamber change history, and an
attribute catalog (value → label) for the UI; storage includes S3. Not yet
built: API keys / OIDC, a TypeScript SDK and server-side evaluation endpoint,
Postgres storage, and cross-replica fanout (run one replica). See
`docs/ARCHITECTURE.md` and `docs/design/targeting.md`.

**Open source:** keep consumer- or company-specific names out of code, docs,
examples, and tests; use generic terms (tenant, location, service).

## Domain vocabulary

Read `pkg/rule.go`, `pkg/flag.go`, `pkg/targeting.go`, `pkg/chamber.go`,
`pkg/context.go`, `pkg/realm.go` — these define the whole model.

- **Rule** — a typed value: a `type` (`string` | `number` | `boolean` |
  `custom`) and a `value`. `UnmarshalJSON` strictly validates the value against
  the type. Embedded by `Flag`.
- **Flag** — one flag: a `Rule` (its default) plus ordered `Targeting`.
  `ValueFor(flagKey, ec)` returns the value of the first rule that applies, else
  the default. `UnmarshalJSON` type-checks rule values and rejects the removed
  `overrides`/`rollout` fields.
- **TargetingRule** — `Clauses` (ANDed), a `Value`, and an optional `Rollout`.
- **Clause** — `{attribute, op, values}`; ops `in`, `notIn`, `startsWith`,
  `semverGte`, `semverLte`. A missing attribute never matches.
- **Rollout** — `{percentage, bucketBy, seed}`: sticky percentage of matching
  contexts, bucketed on any attribute (default `key`). Not bucketed in → next rule.
- **EvaluationContext** — `Key` (the `key` attribute) and `Attributes`
  (`map[string]string`); falls back to process-wide attributes set with
  `WithAttributes` / `WithVersion`. Travels through the request context.
- **Chamber** — a namespace: `map[flagKey]*Flag` (json `rules`), with
  `InheritFrom` / `OverwriteFrom`. **ChamberEntry** is the immutable read view
  the SDK evaluates against, carrying the process-wide attributes.
- **Realm** — the SDK client object (`NewRealm(WithHttpClient, WithPath,
  WithVersion, WithAttributes, WithPollingInterval, WithStreaming)`;
  `Start`/`Stop`; `Bool`/`String`/`Float64`/`CustomValue`; `NewContext` /
  `NewContextWithEvaluation`). Refresh is either **polling** (default) or
  **streaming** (`WithStreaming(true)` → SSE `GET /v1/chambers/<path>?watch=true`,
  with polling fallback). The server pushes changes via an in-process **broker**
  (`http/broker.go`, notified after each write).
- **Metadata** — ETags, history (`/v1/history/<path>`), and the catalog
  (`/v1/catalog/<attribute>`) live in `http/meta.go` / `http/meta_handlers.go`,
  stored under the reserved `/_realm/` prefix in the raw storage beneath the
  `inheritable` wrapper.

## Architecture map

| Path | Role |
|---|---|
| `main.go` | Entry point → `cmd.Execute()` |
| `cmd/` | Cobra CLI: `root.go`, `server.go`, `config.go`, `client*.go` |
| `pkg/` | Core domain + SDK (package `realm`) |
| `pkg/storage/` | `Storage` interface + backends (`file`, `boltdb`, `bigcache`, `gcs`, `s3`) and wrappers (`cacheable`, `inheritable`); backends self-register in `StorageOptions` |
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
- **Domain types validate in `UnmarshalJSON`.** Values that depend on the
  flag's type (like `TargetingRule.Value`) are type-checked by the owning
  `Flag` via `Rule.assertType`; other types validate their own invariants. Give them json tags so they round-trip through the server
  (the handler just unmarshals → re-marshals; see below).
- **Storage backends self-register** in `pkg/storage/storage.go`'s
  `StorageOptions` map via a `StorageCreator` factory. `ValidatePath` rejects
  `..`.
- **Keep exported godoc comments** — the codebase has good doc discipline; match
  it.

## Recipes

- **Add a targeting operator:** add an `Operator` constant in
  `pkg/targeting.go`, validate its values in `Clause.validate`, match it in
  `Clause.Matches`, add table cases to `pkg/targeting_test.go`, and add a label
  in `http/realm-ui/src/utils/describe.ts`. No server change is needed —
  `http/handler.go` persists chambers by unmarshaling into `realm.Chamber` and
  re-marshaling, so tagged fields round-trip automatically.
- **Add a storage backend:** implement `storage.Storage`, register it in
  `StorageOptions` (and `SourcableStorageOptions` if it can be a cache source).
- **Add a CLI subcommand:** add a `*cobra.Command` in `cmd/`, register it in an
  `init()` with `rootCmd.AddCommand(...)`.

## Testing norms

- Standard library `testing` only (no testify/mock). **Table-driven** is the
  norm; use `t.Run(name, …)` for subtests.
- Run with `-race` before finishing (`make test-race`) — it is CI's gate.
- Add `RunParallel` benchmarks for hot evaluation paths (see `pkg/rule_test.go`,
  `pkg/targeting_test.go`); evaluation is allocation-free — keep it that way.
- The S3 integration test runs only when `REALM_S3_TEST_ENDPOINT` and
  `REALM_S3_TEST_BUCKET` are set (see `pkg/storage/s3_test.go`).
- Test data is inlined as literals; there is no `testdata/` fixture convention.

## Verifying changes

For anything touching evaluation or the server, don't stop at unit tests — drive
it end to end: `make run`, `PUT` a chamber via `realm client put` or curl, `GET`
it back, and exercise the SDK path. See the verification section of any change
plan and `docs/ARCHITECTURE.md`.
