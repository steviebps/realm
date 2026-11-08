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
  without inherited flags (editors use this). `GET ?inherit=layers`
  (`http/layers.go`) returns the lineage the inherited view is built from
  (`storage.Layered`): the chamber, each parent chamber it consults, and which
  of them serves each flag; the UI uses it to show where each flag comes from
  and which flags override a parent's.
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
so inheritance and validation are identical. A write wakes every stream below
it at once, so the streams of a path share one read (`sharedReads`, a
`singleflight` keyed by path and the broker's generation, so a stream woken by
a change never gets a read begun before it). A read that fails (storage
unreachable) does not end the stream: it retries with backoff while the client
keeps its values. Each server caps its open streams
(`HandlerConfig.MaxStreams`, 503 beyond it), gives each event 30 seconds to be
written (a client that stops reading loses its stream), and ends each stream
after about 20 minutes so clients reconnect and are re-authenticated; shutdown
cancels every stream (`http.Server.BaseContext`). The Go SDK (`pkg/refresh.go`) reopens a stream that receives nothing (not even
a heartbeat) for 75 seconds and retries with jittered backoff, honoring
`Retry-After` and waiting minutes after 401, 403, or 404. While the stream is
down (three attempts in a row, or a minute, without a delivery), it also polls
at the polling interval, so a server at its stream cap or a proxy that buffers
streams cannot freeze its flags. It falls back to polling only when the server
answers a watch with a chamber (a server that predates streaming), and tries
the stream again every 5 minutes. It applies only `chamber` events, and
rejects a payload that is not a chamber (null, or no `rules`), keeping the
last snapshot.

Where the broker hears about writes depends on the backend. A `Watchable`
backend (`postgres`, `etcd`) reports every committed change, including other replicas'
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
| `boltdb` | `boltdb.go` | embedded bbolt key/value store; `Transactional` (one process) |
| `bigcache` | `bigcache.go` | in-memory (dev / cache tier) |
| `gcs` | `gcs.go` | Google Cloud Storage |
| `s3` | `s3.go` | Amazon S3 or S3-compatible (`bucket`, `region`, `prefix`, `endpoint`, `force_path_style`) |
| `postgres` | `postgres.go` | PostgreSQL (`url`, `max_conns`); `Transactional` and `Watchable`, safe for many replicas |
| `etcd` | `etcd.go` | etcd v3 (`endpoints`, `prefix`, auth and TLS options); `Transactional` and `Watchable`, safe for many replicas |

### Capabilities and multi-replica safety

Backends can implement optional interfaces (`pkg/storage/txn.go`,
`storage.go`):

- **`Transactional`** — `Txn(checks, ops)` applies several puts and deletes
  atomically if each checked key is still at the version read. Every write gets
  the next value of one storage-wide revision, which is each entry's `Version`.
- **`Watchable`** — `Watch(ctx)` reports every committed change (revision and
  key) in order, from any server sharing the storage.
- **`UpdateReader`** — `ReadForUpdate(ctx, key)` reads an entry as stored,
  bypassing any copy; `Update` reads through it, so a read-modify-write never
  starts from a stale cached copy (`cacheable` reads its source).
- **`KeyValidator`** — `ValidateKey(key)` refuses keys the backend cannot
  store (`file`: names too long for the file system, and characters some file
  systems would map onto other names). `Update` checks every op's key before
  applying any; the error wraps `ErrInvalidPath`, which the server answers
  with 400.
- **`HealthReporter`** — `Health()` reports trouble beyond failing reads
  (`postgres`, `etcd`: a change feed that has not caught up for 30 seconds), which
  fails `/readyz`.

All read-modify-write sequences go through `storage.Update` (or
`UpdateJSON`): a chamber write with its If-Match check and history entry, the
catalog, and API keys. On a `Transactional` backend, `Update` version-checks
the keys it read and retries with jittered backoff on conflict, so replicas
need no shared lock. On other backends it holds a process-wide lock instead,
which is why those backends are single-replica. A backend can also implement
**`Locker`** to extend that lock to other processes: `file` takes an exclusive
lock file (`.realm-lock`, `flock` or `LockFileEx`) for the update, so processes
sharing the directory cannot lose each other's writes. `file`
also writes each entry to a temporary file and renames it into place, so
readers never see a partial entry. `boltdb` gives up after 1 second when
another process holds the database's lock.

`boltdb` is `Transactional` although only one process can open it: each
write commits in one bbolt transaction, so an `Update`'s ops (a chamber and
its history entry) land together or not at all, and every entry has a
`Version`. Values stay in the `chambers` bucket as before; revisions live
under the same keys in `versions`, and the counter in `meta`. Opening a
database gives every entry that has no revision (one written before
versions existed) a new one. `cacheable` over a `Transactional` source
(`boltdb`) is `Transactional` too: its `Txn` applies to the source and then
drops the written keys from the cache. Its reads carry no `Version`, since
the cache may answer (and a `boltdb` cache would report its own revisions);
`ReadForUpdate` reads the source, with versions, which is what `Update`
checks.

The `postgres` backend keeps every entry in one table (`realm_entries`: key,
value as bytes, revision), so its keys are the same as every other backend's.
A one-row counter (`realm_revision`) issues revisions; taking it locks it
until commit, so revisions commit in order. Each write also records its keys
in `realm_changes` and sends `NOTIFY realm_changes`. A watcher holds a `LISTEN`
connection, reads `realm_changes` past the last revision it saw whenever it is
notified, every 5 seconds, and after reconnecting, and reports a change to `/`
(everything) when the records it needed were already pruned (10,000 revisions
are kept) or when the revision went backwards (a restore or failover). Every
feed query runs under a 10-second timeout, after which the watcher
reconnects, and its connections use TCP keepalive. Sessions default to short
statement, lock, and idle-in-transaction timeouts, and transactions run at
READ COMMITTED; serialization failures and deadlocks are retried as
conflicts.

The `etcd` backend stores each entry at `<prefix><key>` (keys begin and end
with `/`) and uses etcd's own revisions: an entry's `Version` is its
`mod_revision`, so revisions increase but skip numbers taken by other keys in
the cluster. `Txn` compares each checked key's `mod_revision` (a missing key
compares as 0) and applies the ops, at most one per key (a later op wins), in
key order; a change over etcd's `--max-txn-ops` or `--max-request-bytes`
comes back as an error naming the limit and wrapping `storage.ErrTooLarge`,
which the server answers with 413. `List` reads keys only, a page at a time, skipping the
rest of a child's keys once it has seen the child. A watcher holds an etcd
watch on the prefix from the revision after the last it reported, with
`WithRequireLeader` (a member that loses its leader ends the watch rather
than going silent), and restarts it with backoff when it ends; it reports a
change to `/` when the revisions it needed were compacted or the cluster went
back to an earlier revision. Every 5 seconds it reads the cluster's revision:
the feed is current when nothing was committed past what etcd confirmed
delivering (an event, or a progress notification, which it requests when the
revision has moved). Health fails after 30 seconds without being current.

Two wrappers compose backends:

- **`cacheable`** (`cacheable.go`) — a `cache` over a `source` (e.g.
  `bigcache` over `boltdb`). Reads fill the cache; writes go to the source and
  then drop the entry from the cache, even when the write failed (it may have
  landed). A generation counter keeps a fill that read the source before a
  write from landing after it, and `Update` reads the source (`UpdateReader`).
  It refuses a `postgres` or `etcd` source: another replica's write would
  leave this replica's cache stale.
- **`inheritable`** (`inheritable.go`, enabled by `inheritable: true`) — on
  `Get`, merges a chamber with every ancestor along the path, up to and
  including the root chamber `/`, so child chambers inherit parent rules and
  flags set at `/` reach every chamber; the nearest definition of a flag
  wins, whole. A missing ancestor (including `/`) contributes nothing. This
  hierarchy is the current stand-in for "environments / zones". `Get` is built
  from `Layers` (the chamber plus each consulted ancestor, nearest first, in
  `lineage.go`) and `Lineage.Merge`, so the layers view and the merged view
  cannot disagree. It implements `Unwrap()` so the server can reach the raw
  storage for ETags, history, and the catalog.

Recommended production setups: `postgres` (or `etcd`) for several replicas, or
`cacheable` (`bigcache` over `s3`) as a single replica. SDKs evaluate in
memory, so storage latency only affects writes and cold start.

Server config (`configs/realm.json`, parsed in `cmd/config.go`) selects the
storage type and options, TLS cert/key, port, and `inheritable`. Every command
finds the file the same way (`findConfig`): `--config`, then `$REALM_CONFIG`,
then `realm/realm.json` in the user config directory, then
`/etc/realm/realm.json` — never the working directory.

## Observability

OpenTelemetry is set up in `trace/otel.go` (traces + metrics, OTLP/stdout
exporters). It is off unless `--traces` / `$REALM_TRACES` selects `otlp` or
`stdout` (`cmd/root.go`); the exporter is configured by the standard `OTEL_*`
environment variables, which override the default `service.name` of `realm`. The server wraps its whole
handler chain in one `otelhttp` span per request (named by route, so refused
requests are traced too); probes and watch streams are not traced. Logging
goes through `helper/logging` — a zerolog `TracedLogger` accessed as
`logging.Ctx(ctx)` that writes JSON lines to standard error, so logs are
correlated with the active span. Handlers and commands wrap work in
`tracer.Start(ctx, "…")`; storage spans mark failures with an error status
(`recordError`). The SDK never installs providers or touches zerolog globals;
`WithLogger` routes its logs. By default it logs at `info` and above, with a
`component=realm` field, and rate-limits failures: the first, the recovery,
and repeats at most every 5 minutes.

`GET /healthz` is liveness; `GET /readyz` is readiness (storage answers, not
draining).

The outermost handler, `withLifecycle` (`http/lifecycle.go`), runs around
everything else, tracing included. It sets per-request deadlines: the request
must arrive within the request timeout and the response be taken within 20
seconds more. `http.Server.ReadTimeout` and `WriteTimeout` stay unset, because
they would also cut off watch streams; streams lift the read deadline once
their request is read and bound each write instead. It also gives each request
an ID (`X-Request-Id`, reused from a proxy when safe) and a logger carrying it,
recovers panics into a 500 (or an aborted response, once one has begun), and
writes one access log line per request, whose level reflects what happened
(`logAccess`). Handlers report the caller and, through `noteFailure`, why a
request failed to it through `requestInfo`; only server errors (5xx) get a
log line of their own.
`http.Server.ErrorLog` routes net/http's own messages into the same log.

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
| etcd storage (multi-replica) | ✅ Done | `pkg/storage/etcd.go`; shared contract tests in `pkg/storage/conformance_test.go` |
| **Real-time streaming ("immediately sourced", SSE)** | ✅ Done | `http/broker.go`, `streamChamber` in `http/handler.go`, `Realm.stream` in `pkg/realm.go` |
| Cross-replica stream fanout (multi-instance) | ✅ Done | `Watchable` storage feeds the broker (`http/handler.go`) |
| Revisioned streams (resume with `Last-Event-ID`, SDKs reject stale data) | ⬜ Next | builds on `StorageEntry.Version` |
| SDK API keys + OIDC/GitHub sign-in for the UI, roles | ✅ Done | `auth/`, `http/auth.go` |
| TypeScript SDK + server-side evaluation endpoint | ✅ Done | `sdk/typescript`, `http/evaluate.go`, shared cases in `sdk/conformance` |
| Audit log of flag changes | ✅ Done | per-chamber history with the authenticated actor (`http/meta.go`) |
| Multi-language SDKs | ⬜ Next | new clients against `/v1/chambers` |
| UI editing of targeting, rollouts, diff confirmation, history/restore | ✅ Done | `http/realm-ui` |
