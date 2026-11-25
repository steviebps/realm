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
flags, served the same everywhere an application runs, with progressive rollout.

**Honest current state:** flags are grouped into hierarchical **chambers** that
inherit from their parents; a parent can **seal** a flag so chambers below it
cannot override it. Each flag has a default value and ordered
**targeting rules** matched against context attributes (tenant, location,
environment, service, semver `version`, `buildSha`, …), each optionally
limited to a **deterministic percentage rollout** and/or a one-off **schedule**
(a time window). A flag's targeting can be **turned off** (its rules move to
`pausedTargeting`, which is never evaluated, so it serves its default). A
flag can carry a `description`, `owner`, and `lifecycle`
(`temporary` | `permanent`), and **constraints**: a JSON Schema subset its
values must satisfy on write, which also binds its definitions in the
chambers below (they may tighten, never loosen). The SDK stays current by
**polling** (default 15 min) or **real-time streaming** (SSE). The server
offers ETag optimistic concurrency, per-chamber change history, and an
attribute catalog (value → label) for the UI; storage includes S3,
Postgres, and etcd, the last two safe for many replicas (writes are
version-checked in the database, and every replica streams every change via
LISTEN/NOTIFY or an etcd watch). Optional
auth: path-scoped read/write **API keys** for SDKs and automation, an
optional **admin key** declared in the config (for headless installs; it
manages keys over `/v1/keys`), and optional OIDC / GitHub **sign-in** with
viewer / editor / admin roles for the UI, each granted on every chamber or
only beneath a grant's `paths` (per-path roles; admins are realm-wide).
Browsers use **server-side evaluation** (`POST /v1/evaluate/<path>`, with
public `evaluate` keys) through the **TypeScript SDK** (`sdk/typescript`),
which also evaluates locally for Node services, and renders React pages on
the server with each user's values (`ServerClient.initialState` → a
`BrowserClient`'s `initialState`), hydrating without a mismatch. Reads, evaluations, and
stream events carry the storage revision of the (inherited) chamber
(`X-Realm-Revision`, event `id`), which the SDKs expose (`Realm.Revision(ctx)`,
`revision()`). A multi-tenant service can read or watch every chamber below
a path at once (`?subtree=true`) and pick the chamber for each request from
a **path template** such as `/prod/tenants/{tenantId}/{locationId}/` (the
deepest path with a chamber wins); subtrees are gzipped, and read with one
range read on Postgres, etcd, and boltdb. Not yet built: revisioned
streams (resume with `Last-Event-ID`, SDKs rejecting stale data); subtrees in
the evaluation endpoint; with
backends other than Postgres and etcd, run one replica. See
`docs/ARCHITECTURE.md`, `docs/design/targeting.md`, `docs/design/auth.md`,
`docs/design/evaluation.md`, `docs/design/subtree.md`, and
`docs/design/constraints.md`.

**Open source:** keep consumer- or company-specific names out of code, docs,
examples, and tests; use generic terms (tenant, location, service).

## Domain vocabulary

Read `pkg/rule.go`, `pkg/flag.go`, `pkg/targeting.go`, `pkg/chamber.go`,
`pkg/schema.go`, `pkg/context.go`, `pkg/realm.go` — these define the whole
model.

- **Rule** — a typed value: a `type` (`string` | `number` | `boolean` |
  `custom`) and a `value`. `UnmarshalJSON` strictly validates the value against
  the type. Embedded by `Flag`.
- **Flag** — one flag: a `Rule` (its default) plus ordered `Targeting`.
  `ValueFor(flagKey, ec)` returns the value of the first rule that applies, else
  the default. `UnmarshalJSON` type-checks rule values and rejects the removed
  `overrides`/`rollout` fields. A flag of a type this version does not know
  decodes (kept raw, re-marshaled unchanged) but cannot be served: getters
  return the caller's default with an error, and `Chamber.Evaluate` omits it.
  `PausedTargeting` holds the rules while targeting is turned off: validated
  like `Targeting` but never evaluated, so older SDKs (which ignore the field)
  agree; `ParseChamber` refuses a flag with rules in both.
  `Sealed` keeps chambers below the flag's chamber from overriding it:
  `Lineage.Merge` serves the farthest sealing ancestor's definition and hides
  nearer ones, and the server refuses writes below it that add or change
  the flag (an unchanged definition stored before the seal may stay).
  `Description`, `Owner`, `Lifecycle` are labels. `Constraints` is kept raw
  (`json.RawMessage`, so newer keywords round-trip); `ParseConstraints`
  decodes it into a `Schema`, and `CheckValues` checks the default, targeting,
  and paused values. `ParseChamber` checks both; reads never do.
- **Schema** (`pkg/schema.go`) — the JSON Schema subset of constraints:
  `type`, `enum`, `minimum`, `maximum`, `multipleOf`, `minLength`,
  `maxLength`, `properties`, `required`, `additionalProperties` (bool),
  `items`. `ParseSchema` refuses any other keyword and inconsistent schemas;
  `Validate` returns a `*ValueError` with the path of the refused part. The
  server (`http/constraints.go`) holds every definition to the constraints of
  each parent that defines the flag (`Lineage.Ancestry`), and refuses a
  change of constraints that a definition below breaks (`checkBelow`).
- **TargetingRule** — `Clauses` (ANDed), a `Value`, an optional `Rollout`, and
  an optional `Schedule`.
- **Clause** — `{attribute, op, values}`; ops `in`, `notIn`, `startsWith`,
  `semverGte`, `semverLte`. A missing attribute never matches, and neither
  does an operator this version does not know (the clause is kept raw and
  re-marshaled unchanged).
- **Rollout** — `{percentage, bucketBy, seed}`: sticky percentage of matching
  contexts, bucketed on any attribute (default `key`). Not bucketed in → next rule.
- **Schedule** — `{start, end}` RFC 3339 bounds (either optional): the rule
  applies from `start` until `end`, judged by the evaluating host's clock.
  `Chamber.Evaluate` also returns the next schedule bound, so evaluation
  streams know when to re-send.
- **EvaluationContext** — `Key` (the `key` attribute) and `Attributes`
  (`map[string]string`); falls back to process-wide attributes set with
  `WithAttributes` / `WithVersion`. Travels through the request context.
- **Chamber** — a namespace: `map[flagKey]*Flag` (json `rules`), with
  `InheritFrom` / `OverwriteFrom`. **ChamberEntry** is the immutable read view
  the SDK evaluates against, carrying the process-wide attributes.
- **Realm** — the SDK client object (`NewRealm(WithHttpClient, WithPath |
  WithPathTemplate, WithVersion, WithAttributes, WithPollingInterval,
  WithStreaming, WithLogger)`; `Start`/`Stop`;
  `Bool`/`String`/`Float64`/`CustomValue`; `NewContext` /
  `NewContextWithEvaluation`; `Revision` / `ChamberPath`). With
  `WithPathTemplate` it holds a view per path of the subtree at the
  template's root (`pkg/subtree.go`), and `NewContextWithEvaluation` pins the
  view the context resolves to (`pkg/template.go`): the deepest candidate
  path the snapshot holds; `ChamberPath(ctx)` says which. Refresh is either **polling** (default) or
  **streaming** (`WithStreaming(true)` → SSE `GET /v1/chambers/<path>?watch=true`),
  in `pkg/refresh.go`: streaming also polls while the stream is down, and
  falls back to polling when the server answers a watch with a chamber.
  Retries back off with jitter, honor `Retry-After`, and wait minutes after
  401/403/404; failures are logged rate-limited. The server pushes changes via
  an in-process **broker** (`http/broker.go`, notified after each write);
  subtree subscriptions also record which paths changed, and resync past
  1,024 of them.
- **Metadata** — ETags, history (`/v1/history/<path>`), and the catalog
  (`/v1/catalog/<attribute>`) live in `http/meta.go` / `http/meta_handlers.go`,
  stored under the reserved `/_realm/` prefix in the raw storage beneath the
  `inheritable` wrapper.
- **Principal** (`auth/`) — the caller of a request: an API key (`Access`
  evaluate/read/write, `Path` scope), the config's admin key (`KindAdminKey`,
  `RoleAdmin`), a signed-in user (`Role` on every chamber, plus the higher
  roles of `Paths`; `RoleAt(path)` combines them), or `auth.Anonymous`
  when the server has no `auth` config. Handlers read it with
  `auth.FromContext(ctx)` and check `CanEvaluate(path)` / `CanRead(path)` /
  `CanWrite(path)` / `CanReadCatalog()` / `IsAdmin()`. Keys are hashed under `/_realm/keys/`; sessions are encrypted
  cookies; roles are recomputed from config on every request
  (`Config.RolesFor`: each role in `AccessConfig` takes one `Grant` or a
  list, and a grant's `Paths` limit it). The UI works out each chamber's
  controls from `/v1/auth/me` in `http/realm-ui/src/utils/access.ts`.

## Architecture map

| Path | Role |
|---|---|
| `main.go` | Entry point → `cmd.Execute()` |
| `cmd/` | Cobra CLI: `root.go`, `server.go`, `config.go`, `client*.go` |
| `pkg/` | Core domain + SDK (package `realm`); refresh in `refresh.go`, path templates in `template.go` (parse, resolve), subtree reads and streams in `subtree.go` |
| `pkg/storage/` | `Storage` interface + backends (`file`, `boltdb`, `bigcache`, `gcs`, `s3`, `postgres`, `etcd`) and wrappers (`cacheable`, `inheritable`, which reuses unchanged chambers' parses and merged views via `inheritable_cache.go`); backends self-register in `StorageOptions`; optional `Transactional` / `Watchable` / `Locker` capabilities and `Update` in `txn.go` (callers ask `IsInheritable`, `CanScan`, `ChangeFeed`, `Health` rather than type-asserting: each capability is found along the wrapper chain, so wrapper order does not matter), and `Scanner` (one range read of a prefix: `postgres`, `etcd`, `boltdb`) in `scan.go`; `file_{unix,windows,other}.go` hold the file backend's per-platform locking; `subtree.go`: `ListTree` (the paths below a root, capped; one `ScanKeys` on a `Scanner`), `ReadTree` (each view, reading each chamber once, with one `Scan` when it can), `WalkTree`, `ScanLayers`, `ReadLayers` (`ScanLayers` or a read each), `BuildView` |
| `client/http.go` | `HttpClient` used by both CLI and SDK (sends `Token` as a bearer API key) |
| `auth/` | API keys, sessions, OIDC / GitHub providers, roles; `authtest/` is a fake OIDC issuer for tests |
| `sdk/typescript/` | TypeScript SDK: `ServerClient` (local evaluation, a port of `pkg/targeting.go`; `pathTemplate` via `src/template.ts` and `src/subtree.ts`), `BrowserClient` (server evaluation; `src/storage.ts` caches evaluated values per context for offline start), and `@steviebps/realm-sdk/react` (`src/react.ts`: provider + hooks over a `BrowserClient`; server rendering and hydration read the client's construction-time `initialState` / `bootstrap`, made by `ServerClient.initialState`) |
| `sdk/conformance/` | Cases the Go and TypeScript tests both run: `evaluation.json` (evaluation; `pkg/conformance_test.go`), `subtree.json` (path-template resolution; `pkg/template_test.go`, `test/subtree.test.ts`), `constraints.json` (constraint schemas and values; `pkg/schema_test.go`, `http/realm-ui/src/utils/constraints.test.ts`) |
| `http/` | Server handler (`handler.go`), the chamber write module every PUT/PATCH/DELETE goes through (`write.go`: `chamberWriter.Write`, no HTTP in its interface), the chamber read module every read, evaluation, and their streams go through (`read.go`: `chamberReader`, who may be told why a chamber cannot be parsed), constraints across chambers (`constraints.go`), request lifecycle: per-request deadlines, request IDs, access log, panic recovery (`lifecycle.go`), evaluation endpoint and CORS (`evaluate.go`), subtree reads and watches with the per-change shared view cache (`subtree.go`) and their gzip (`compress.go`: `Accept-Encoding` parsing, a pooled per-batch deflate writer with sync flushes), stream broker (`broker.go`), auth middleware and `/v1/keys` (`auth.go`), request mapping (`agent.go`), UI serving (`ui.go`; files embedded by `assets.go` behind the `ui` build tag, `assets_stub.go` fallback), `realm-ui/` React app |
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
make sdk-test     # typecheck, test, and build the TypeScript SDK
make e2e          # build, then scripts/e2e.sh against a real server
```

The server needs a config unless in dev mode: `realm server --config ./configs/realm.json`,
`$REALM_CONFIG`, or a default location (`cmd/config.go` `findConfig`). The CLI
reads `$REALM_ADDRESS` / `$REALM_TOKEN`.

## Conventions to follow

- **Logging is context-first.** Use `logging.Ctx(ctx)` and
  `logger.InfoCtx(ctx)` / `ErrorCtx(ctx)` — never the bare `log` package or
  `fmt.Println`. Logs are trace-correlated, and carry the request ID inside
  handlers. A handler reports why a request failed with `noteFailure`, which
  adds the reason to the request's access log line; only server errors (5xx)
  get an error line of their own, so client mistakes never read as errors.
- **Wrap meaningful work in an OTel span:** `ctx, span := tracer.Start(ctx, "…")`.
- **SDK config uses functional options** (`WithX(...) RealmOption`). Extend that
  pattern rather than adding constructor parameters.
- **Writes are strict, reads are lenient.** The server parses chamber writes
  with `realm.ParseChamber`, which rejects unknown fields, operators, and flag
  types at every level (add a new field to its `shape` tables in
  `pkg/chamber.go`); plain `json.Unmarshal`, which the SDKs and the server's
  reads use, accepts them so older SDKs survive newer servers: an unknown
  operator never matches, and an unknown-type flag cannot be served. Unknown
  operators and types round-trip byte for byte.
- **Paths are canonicalized once** (`utils.CanonicalPath`: request paths in
  `logicalPath`, `http/agent.go`; key and grant paths in `auth.scopePath`) and
  never unescaped twice; check `storage.IsReservedPath` on the canonical form.
- **Domain types validate in `UnmarshalJSON`.** Values that depend on the
  flag's type (like `TargetingRule.Value`) are type-checked by the owning
  `Flag` via `Rule.assertType`; other types validate their own invariants. Give them json tags so they round-trip through the server
  (the handler just unmarshals → re-marshals; see below).
- **Lineages are built in one place.** `storage.BuildLineage` reads a
  chamber and the ancestors it consults; the inherited view (`Merge`),
  `Ancestry` (seals and constraints above), and `Resolve` all come from a
  `Lineage`, so never walk parent paths by hand.
- **Chamber writes go through `chamberWriter.Write`** (`http/write.go`),
  never `storage.Update` on a chamber directly from a handler.
- **Read-modify-write goes through `storage.Update` / `UpdateJSON`** (never a
  `Get` then `Put`, or a mutex): it version-checks on `Transactional` backends,
  so it stays correct across replicas.
- **Storage backends self-register** in `pkg/storage/storage.go`'s
  `StorageOptions` map via a `StorageCreator` factory. `ValidatePath` rejects
  `..`.
- **Keep exported godoc comments** — the codebase has good doc discipline; match
  it.

## Recipes

- **Add a targeting operator:** add an `Operator` constant in
  `pkg/targeting.go`, list it in `Operator.known`, validate its values in
  `Clause.validate`, match it in `Clause.Matches`, add table cases to `pkg/targeting_test.go`, port it to
  `sdk/typescript/src/evaluate.ts`, add cases to
  `sdk/conformance/evaluation.json`, and add a label in
  `http/realm-ui/src/utils/describe.ts`. No server change is needed —
  `http/handler.go` persists chambers by unmarshaling into `realm.Chamber` and
  re-marshaling, so tagged fields round-trip automatically.
- **Add a constraint keyword:** parse and cross-check it in `parseSchema`
  and check it in `Schema.validate` (`pkg/schema.go`), list it in
  `schemaKeywords` and `typeKeywords`, port both to
  `http/realm-ui/src/utils/constraints.ts`, add cases to
  `sdk/conformance/constraints.json`, add it to `Schema` in
  `sdk/typescript/src/types.ts` and `http/realm-ui/src/models/response.ts`,
  and describe it in `docs/design/constraints.md`.
- **Add a storage backend:** implement `storage.Storage`, register it in
  `StorageOptions` (and `SourcableStorageOptions` if it can be a cache source),
  and add it to `backends` in `pkg/storage/backends_test.go`. A
  `Transactional` backend joins `transactionalBackends` in
  `pkg/storage/conformance_test.go`; one that several servers can share
  (`Transactional` and `Watchable`) joins `sharedBackends` there instead, so
  the shared contract tests hold it to what `Update` and the change feed need.
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
- Postgres tests (`pkg/storage/postgres*_test.go`) run only when
  `REALM_POSTGRES_TEST_URL` is set; each gets a throwaway schema from
  `pkg/storage/pgtest`. etcd tests (`pkg/storage/etcd_test.go`) run only when
  `REALM_ETCD_TEST_ENDPOINTS` (comma-separated client URLs) is set; each gets
  a throwaway key prefix from `pkg/storage/etcdtest`. The shared contract
  tests (`pkg/storage/conformance_test.go`) and the replica tests
  (`http/replicas_test.go`) run against whichever of the two is set.
- Test data is inlined as literals; there is no `testdata/` fixture convention.

## Verifying changes

For anything touching evaluation or the server, don't stop at unit tests — drive
it end to end: `make run`, `PUT` a chamber via `realm client put -f <file>` or curl, `GET`
it back, and exercise the SDK path. See the verification section of any change
plan and `docs/ARCHITECTURE.md`.
