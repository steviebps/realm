# Architecture

`realm` is a feature-flag / configuration platform. A single Go binary is a
**server**, a **CLI**, and an importable **Go SDK**. This document explains the
data model, how a value is resolved, how a request flows through the system, and
where the project sits relative to its "open-source LaunchDarkly" goal.

## Data model

Flags are stored as JSON and modeled by a few nested types in `pkg/`:

```
Chamber                        // a namespace: map[flagKey] -> Flag   (json "rules")
└── Flag                       // one flag: { type, value } + ordered targeting
    └── Targeting []TargetingRule
        ├── Clauses []Clause   // ANDed: { attribute, op, values }
        ├── Value              // served when the rule applies
        ├── Rollout *Rollout   // optional: { percentage, bucketBy, seed }
        └── Schedule *Schedule // optional: { start, end } (RFC 3339)
```

- **`Rule`** (`pkg/rule.go`) — a typed value: `type` and `value`.
  `UnmarshalJSON` strictly validates the value against the declared type
  (`assertType`). It is embedded by `Flag`.
- **`Flag`** (`pkg/flag.go`) — a default value plus ordered `targeting` rules.
  `UnmarshalJSON` type-checks every rule value against the flag type and
  rejects the removed `overrides`/`rollout` fields with migration guidance.
- **`TargetingRule` / `Clause` / `Rollout` / `Schedule`** (`pkg/targeting.go`)
  — a rule applies while its schedule (if any) is in effect, when all its
  clauses match the evaluation context and, if it has a rollout, the context
  buckets in. Operators: `in`, `notIn`, `startsWith`,
  `semverGte`, `semverLte`.
- **`Chamber`** (`pkg/chamber.go`) — `map[string]*Flag` plus `InheritFrom`
  (fill missing flags from a parent) and `OverwriteFrom` (PATCH merges).
  **`ChamberEntry`** is the immutable read view the SDK evaluates against; it
  carries the process-wide attributes from `WithAttributes`/`WithVersion`.

The design rationale is in [design/targeting.md](design/targeting.md).

### Value resolution

`Flag.ValueFor(flagKey, ec)` (`pkg/flag.go`):

```
for rule in flag.targeting (in order):
    if rule.schedule includes now and all clauses match
        and rule.rollout includes ec: return rule.value
return flag.value
```

A clause on an attribute the context does not carry never matches (including
`notIn`). A rule whose rollout excludes the context falls through to the next
rule.

An **`EvaluationContext`** (`pkg/context.go`) carries `Key` (exposed as the
`key` attribute) and `Attributes`. Lookups fall back to the process-wide
attributes configured on the SDK, so deployment facts (`environment`,
`service`, `version`, `buildSha`) are set once while subject facts
(`tenantId`, `locationId`) travel per request via
`Realm.NewContextWithEvaluation`. Evaluation is allocation-free (~30 ns for a
flag with a 200-value clause and a rollout rule).

### Deterministic bucketing

`bucket(seed, value)` (`pkg/targeting.go`) hashes `"<seed>.<value>"` with
SHA-1, takes the leading 60 bits, and normalizes to `[0,100)` — the
LaunchDarkly-style algorithm. The bucketed value is the rollout's `bucketBy`
attribute (default `key`), so bucketing by `locationId` rolls out to whole
locations at once. `seed` defaults to the flag key.

## Request flow

```
realm client … ┐
Go SDK (Realm)  ┼─► client.HttpClient ─► HTTP /v1/chambers/<path> ─► http.Handler ─► Storage
                ┘        (OTel headers)         (GET/PUT/PATCH/DELETE/LIST)
```

- **`client/http.go`** — the `HttpClient` used by both the CLI and the SDK;
  injects OpenTelemetry trace-propagation headers.
- **`http/handler.go` + `http/agent.go`** — map HTTP methods to operations
  (`GET`→get/list, `POST`→put, `PATCH`→patch-merge via `OverwriteFrom`,
  `DELETE`→delete). `GET ?inherit=false` returns the chamber as stored,
  without inherited flags (editors use this).
- **Safe editing** — `GET ?inherit=false` returns an `ETag` of the chamber as
  stored at its path (the inherited view carries none); writes honor `If-Match` / `If-None-Match: *` and return `412` on
  mismatch, checked under a per-path lock (`http/meta.go`). Every write is
  appended to history with the authenticated caller as the actor (or, when
  auth is off, the self-reported `X-Realm-Actor` header).
- **Auth** (`auth/`, `http/auth.go`) — optional. `withPrincipal` identifies
  each request's caller (an API key bearer token, a UI session cookie, or
  `auth.Anonymous` when auth is off); handlers check `CanRead` / `CanWrite`
  on the chamber path and `IsAdmin` for keys and the catalog. See
  `docs/design/auth.md`.
- **Metadata endpoints** (`http/meta_handlers.go`) — `GET /v1/history/<path>`
  (and `?revision=N`) and `/v1/catalog[/<attribute>]`, the application-supplied
  map of attribute values to human-readable labels used by the UI. Metadata is
  stored under the reserved `/_realm/` prefix in the raw storage beneath the
  `inheritable` wrapper; chamber APIs reject and hide that prefix. Cross-cutting: gzip, per-request timeout, OTel spans/metrics,
  `no-store` cache header, `X-Realm-Hostname`.
- **Persistence note:** PUT/PATCH unmarshal the body into a `realm.Chamber` and
  re-marshal it before storing. Any json-tagged field on the domain types
  (including `Rollout`) therefore round-trips through the server automatically —
  new rule capabilities need **no** handler changes.

The SDK (`pkg/realm.go`) fetches the chamber at its configured path on `Start`
and then keeps it current, swapping the immutable `ChamberEntry` snapshot under a
mutex. Reads (`Bool`/`String`/`Float64`/`CustomValue`) pull the snapshot (and any
`EvaluationContext`) from the request context. Two refresh modes:

- **Polling (default)** — refetch on a ticker (`DefaultPollingInterval = 15m`).
- **Streaming (`WithStreaming(true)`)** — hold an SSE connection open and apply
  changes as the server pushes them ("immediately sourced"). The SDK reconnects
  with capped exponential backoff and **falls back to polling** if the server
  does not return an event stream (detected via `Content-Type`).

### Real-time updates (SSE)

`GET /v1/chambers/<path>?watch=true` returns a `text/event-stream` that emits an
`event: chamber` with the current chamber immediately, then again on every
change, plus periodic heartbeat comments. On the server, an in-process **broker**
(`http/broker.go`) is notified of each write (`Put`/`Patch`/`Delete`) and
wakes watchers whose path is at or below the changed path (prefix match), so an
inherited parent change refreshes child watchers. The watch request bypasses the
per-request timeout; the endpoint reuses the same storage `Get` as a normal read,
so inheritance and validation are identical. Each server caps its open streams
(`HandlerConfig.MaxStreams`, 503 beyond it) and ends each stream after about 20
minutes so clients reconnect and are re-authenticated; shutdown cancels every
stream (`http.Server.BaseContext`). The Go SDK reopens a stream that receives
nothing (not even a heartbeat) for 75 seconds, retries with jittered backoff,
and falls back to polling only when the server answers a watch with a plain
chamber.

Where the broker hears about writes depends on the backend. A `Watchable`
backend (`postgres`) reports every committed change, including other replicas'
writes, and that feed alone drives the broker, so every replica pushes every
change. With other backends the handler notifies the broker after its own
writes, which only reaches watchers on the same replica; run one replica with
those backends.

## Storage

`pkg/storage/storage.go` defines `Storage{Get,Put,Delete,List,Close}` over
`StorageEntry{Key, Value, Version}`. Backends self-register in the `StorageOptions` map:

| Backend | File | Notes |
|---|---|---|
| `file` | `file.go` | JSON files on disk |
| `boltdb` | `boltdb.go` | embedded bbolt key/value store |
| `bigcache` | `bigcache.go` | in-memory (dev / cache tier) |
| `gcs` | `gcs.go` | Google Cloud Storage |
| `s3` | `s3.go` | Amazon S3 or S3-compatible (`bucket`, `region`, `prefix`, `endpoint`, `force_path_style`) |
| `postgres` | `postgres.go` | PostgreSQL (`url`, `max_conns`); `Transactional` and `Watchable`, safe for many replicas |

### Capabilities and multi-replica safety

Backends can implement two optional interfaces (`pkg/storage/txn.go`):

- **`Transactional`** — `Txn(checks, ops)` applies several puts and deletes
  atomically if each checked key is still at the version read. Every write gets
  the next value of one storage-wide revision, which is each entry's `Version`.
- **`Watchable`** — `Watch(ctx)` reports every committed change (revision and
  key) in order, from any server sharing the storage.

All read-modify-write sequences go through `storage.Update` (or
`UpdateJSON`): a chamber write with its If-Match check and history entry, the
catalog, and API keys. On a `Transactional` backend, `Update` version-checks
the keys it read and retries with jittered backoff on conflict, so replicas
need no shared lock. On other backends it holds a process-wide lock instead,
which is why those backends are single-replica. A backend can also implement
**`Locker`** to extend that lock to other processes: `file` takes an exclusive
lock file (`.realm-lock`, `flock` or `LockFileEx`) for the update, so a server
and `realm keys` sharing the directory cannot lose each other's writes. `file`
also writes each entry to a temporary file and renames it into place, so
readers never see a partial entry. `boltdb` gives up after 1 second when
another process holds the database's lock.

The `postgres` backend keeps every entry in one table (`realm_entries`: key,
value as bytes, revision), so its keys are the same as every other backend's.
A one-row counter (`realm_revision`) issues revisions; taking it locks it
until commit, so revisions commit in order. Each write also records its keys
in `realm_changes` and sends `NOTIFY realm_changes`. A watcher holds a `LISTEN`
connection, reads `realm_changes` past the last revision it saw whenever it is
notified, every 5 seconds, and after reconnecting, and reports a change to `/`
(everything) when the records it needed were already pruned (10,000 revisions
are kept).

Two wrappers compose backends:

- **`cacheable`** (`cacheable.go`) — a write-through `cache` over a `source`
  (e.g. `bigcache` over `boltdb`). It refuses a `postgres` source: another
  replica's write would leave this replica's cache stale.
- **`inheritable`** (`inheritable.go`, enabled by `inheritable: true`) — on
  `Get`, merges a chamber with all of its ancestors along the path so child
  chambers inherit parent rules. This hierarchy is the current stand-in for
  "environments / zones". It implements `Unwrap()` so the server can reach the
  raw storage for ETags, history, and the catalog.

Recommended production setups: `postgres` for several replicas, or
`cacheable` (`bigcache` over `s3`) as a single replica. SDKs evaluate in
memory, so storage latency only affects writes and cold start.

Server config (`configs/realm.json`, parsed in `cmd/config.go`) selects the
storage type and options, TLS cert/key, port, and `inheritable`. Every command
finds the file the same way (`findConfig`): `--config`, then `$REALM_CONFIG`,
then `realm/realm.json` in the user config directory, then
`/etc/realm/realm.json` — never the working directory.

## Observability

OpenTelemetry is set up in `trace/otel.go` (traces + metrics, OTLP/stdout
exporters), configured by the standard `OTEL_*` environment variables, which
override the default `service.name` of `realm`. The server wraps its whole
handler chain in one `otelhttp` span per request (named by route, so refused
requests are traced too); probes and watch streams are not traced. Logging
goes through `helper/logging` — a zerolog `TracedLogger` accessed as
`logging.Ctx(ctx)` that writes JSON lines to standard error, so logs are
correlated with the active span. Handlers and commands wrap work in
`tracer.Start(ctx, "…")`; storage spans mark failures with an error status
(`recordError`). The SDK never installs providers or touches zerolog globals;
`WithLogger` routes its logs.

`GET /healthz` is liveness; `GET /readyz` is readiness (storage answers, not
draining).

## Path to LaunchDarkly parity

| Capability | Status | Where |
|---|---|---|
| Typed flags (bool/number/string/custom) | ✅ Done | `pkg/rule.go` |
| Version targeting (semver ranges, build SHAs) | ✅ Done | `pkg/targeting.go` |
| **Percentage rollout (deterministic, `bucketBy` any attribute)** | ✅ Done | `pkg/targeting.go` |
| Hierarchy / inheritance (proto "zones") | ✅ Done | `pkg/storage/inheritable.go` |
| Attribute targeting (ordered rules on `EvaluationContext.Attributes`) | ✅ Done | `pkg/targeting.go`, `pkg/flag.go` |
| Scheduled rules (one-off time windows) | ✅ Done | `Schedule` in `pkg/targeting.go`; evaluation streams re-send at bounds |
| Recurring schedules (weekly windows in a time zone) | ⬜ Next | `pkg/targeting.go` |
| Optimistic concurrency, change history, attribute catalog | ✅ Done | `http/meta.go`, `http/meta_handlers.go` |
| S3 storage | ✅ Done | `pkg/storage/s3.go` |
| Postgres storage + multi-replica safety | ✅ Done | `pkg/storage/postgres.go`, `storage.Update` in `pkg/storage/txn.go` |
| **Real-time streaming ("immediately sourced", SSE)** | ✅ Done | `http/broker.go`, `streamChamber` in `http/handler.go`, `Realm.stream` in `pkg/realm.go` |
| Cross-replica stream fanout (multi-instance) | ✅ Done | `Watchable` storage feeds the broker (`http/handler.go`) |
| Revisioned streams (resume with `Last-Event-ID`, SDKs reject stale data) | ⬜ Next | builds on `StorageEntry.Version` |
| SDK API keys + OIDC/GitHub sign-in for the UI, roles | ✅ Done | `auth/`, `http/auth.go` |
| TypeScript SDK + server-side evaluation endpoint | ✅ Done | `sdk/typescript`, `http/evaluate.go`, shared cases in `sdk/conformance` |
| Audit log of flag changes | ✅ Done | per-chamber history with the authenticated actor (`http/meta.go`) |
| Multi-language SDKs | ⬜ Next | new clients against `/v1/chambers` |
| UI editing of targeting, rollouts, diff confirmation, history/restore | ✅ Done | `http/realm-ui` |
