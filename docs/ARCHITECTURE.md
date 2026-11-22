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
  It also says what it is for (`description`, `owner`, `lifecycle`) and may
  restrict its values with `constraints`, a JSON Schema subset (`Schema` in
  `pkg/schema.go`) that `ParseChamber` checks on writes and reads ignore
  ([design/constraints.md](design/constraints.md)).
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
rule. Rules in `pausedTargeting` (a flag whose targeting is turned off) are
never evaluated, so the flag serves its default to every context.

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
  (`storage.Layered`): the chamber, each parent chamber it consults, which
  of them serves each flag, which flags a parent seals (`sealedBy`), and the
  definitions those seals hide (`hidden`); the UI uses it to show where each
  flag comes from and which flags override a parent's.
- **Subtree reads** — `GET ?subtree=true` (`http/subtree.go`) returns the
  inherited view of the path and of every chamber below it, root first and
  parents before children, so a service can serve many tenants or locations
  from one read. `storage.ListTree` lists the paths below the root (refused
  with 413 beyond `HandlerConfig.MaxSubtreeChambers`, default 10,000, before
  anything is written); `storage.ReadTree` then reads each chamber once,
  keeping only those above the root and along the path being walked, and
  merges each view in memory with `storage.BuildView` (the same
  `Lineage.Merge` a plain read uses). On a backend that is a **`Scanner`**
  (`postgres`, `etcd`, `boltdb`), the listing is one `ScanKeys` and the
  chambers below the root come from one `Scan`, so a subtree costs a few
  round trips however many chambers it holds (a 500-chamber subtree on
  localhost Postgres: 4 storage calls and about 10 ms, against 1,004 calls
  and 96 ms read path by path); other backends list each level and read each
  chamber. The response is written as it is read, so a failure partway through
  ends the body early and the client retries. When the request's
  `Accept-Encoding` allows gzip, the body is gzipped (`http/compress.go`; see
  Real-time updates for how). A view that cannot be built is
  an entry with an `error` rather than a failed response. A missing root is
  never a 404: its entry is the view built from its ancestors. Reading needs
  `CanRead` on the root only. The wire format is in
  [design/subtree.md](design/subtree.md).
- **Sealed flags** — a PUT or PATCH that adds a flag a parent seals, or
  changes the chamber's stored definition of one, is refused with 400 naming
  the parent (`writeGuard.check` in `http/handler.go`, from
  `storage.ReadAncestry`). A definition stored before the seal may stay
  as it is, so the chamber stays editable. The seals are read before the
  write rather than inside its `Update`: `Lineage.Merge` is what enforces
  them, so a seal that lands during a write still hides what it stores.
- **Constraints** — a flag's `constraints` bind its definitions below the
  chamber too (`http/constraints.go`). A PUT or PATCH that adds or changes a
  flag is refused with 400 when its type or values break the constraints of
  any parent's definition (read with the seals, in one pass:
  `storage.ReadAncestry`). A write that adds or changes constraints (judged by
  JSON value, so reordering is no change) is checked against the chambers
  above first, then reads the chambers below (`checkBelow`: `ListTree`, then
  `ReadLayers`) and is refused, naming them, when any serves a definition
  they refuse; definitions a seal hides are skipped. Within the write's
  `Update` the server checks that the constraints it changes are those it
  scanned for, and answers 409 otherwise. See [design/constraints.md](design/constraints.md).
- **Safe editing** — `GET ?inherit=false` returns an `ETag` of the chamber as
  stored at its path (the inherited view carries none); writes honor `If-Match` / `If-None-Match: *` and return `412` on
  mismatch, checked under a per-path lock (`http/meta.go`). Every write is
  appended to history with the authenticated caller as the actor (or, when
  auth is off, the self-reported `X-Realm-Actor` header).
- **Auth** (`auth/`, `http/auth.go`) — optional. `withPrincipal` identifies
  each request's caller (an API key bearer token, a UI session cookie, or
  `auth.Anonymous` when auth is off); handlers check `CanRead` / `CanWrite`
  on the chamber path and `IsAdmin` for keys and the catalog. A signed-in
  user's role can differ by path: grants with `paths` raise it on the
  chambers beneath them (`Principal.RoleAt`). See `docs/design/auth.md`.
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

**Path templates.** Instead of one path, the SDK can take a template whose
segments may be placeholders named after attributes
(`WithPathTemplate("/prod/tenants/{tenantId}/{locationId}/")`, parsed in
`pkg/template.go`; `pathTemplate` in the TypeScript `ServerClient`). It then
reads, or watches, the subtree at the template's root (its literal prefix,
`/prod/tenants/`) and holds a map of views by path (`pkg/subtree.go`) in place
of the single `ChamberEntry`. Each request gets the view of the deepest path
the snapshot holds a view for, filling placeholders in order from the
evaluation context: a location without a chamber gets its tenant's view, and a
tenant without one the root's. `NewContextWithEvaluation` resolves once and
pins that view to the context, so flag reads stay allocation-free;
`Realm.ChamberPath(ctx)` (`chamberPath(context)` in TypeScript) reports which
path a request landed on, since a mistyped attribute falls back silently. The
Go SDK retries a server that answers a subtree read with something else (one
that predates subtrees) after minutes, as after a 404. Both SDKs run the resolution
cases in `sdk/conformance/subtree.json`.

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

**Revisions.** Chamber reads and evaluations carry `X-Realm-Revision`
(`api.RevisionHeader`), and each stream event carries it as its `id`, when the
storage tracks revisions (`StorageEntry.Version`; transactional backends do,
`cacheable` does not). For the inherited view `InheritableStorage.Get` reports
`Lineage.Revision`: the highest `Version` among the chamber and the ancestors
it merges, so any layer's write changes it. Deleting an ancestor takes its
revision away, so the view's revision can go down: it names a version rather
than ordering versions. Streams send only changed payloads, so an event's id is
the first revision that produced it. The Go SDK keeps the revision on its
`ChamberEntry` (`Realm.Revision(ctx)` reads the pinned snapshot's); the
TypeScript clients expose `revision()`. Nothing yet resumes a stream from
`Last-Event-ID` or rejects stale data.

Where the broker hears about writes depends on the backend. A `Watchable`
backend (`postgres`, `etcd`) reports every committed change, including other replicas'
writes, and that feed alone drives the broker, so every replica pushes every
change. With other backends the handler notifies the broker after its own
writes, which only reaches watchers on the same replica; run one replica with
those backends.

**Subtree watches.** `GET /v1/chambers/<root>?subtree=true&watch=true`
(`subtrees.stream` in `http/subtree.go`) sends a `chamber` event for each
view a subtree read returns, then `synced`; after that, a `chamber` event
for each view a change alters and `deleted` for each chamber deleted (see
[design/subtree.md](design/subtree.md)). The stream subscribes with
`broker.SubscribeTree` before listing the subtree, so no change falls between
the listing and the subscription. A tree subscription is woken by changes at,
above, or below its root, and records the changed paths in a dirty set,
which the stream takes after each signal: a change at or above the root may
alter every view, and a change below it alters the views at and below that
path (or creates or deletes one). Signals still coalesce, so a burst of writes
costs one pass. Past `maxDirtyPaths` (1,024) paths between two takes, the
subscription overflows and the stream **resyncs**: it lists the subtree
again, sends every view, then `deleted` for each path it had sent that the
new listing lacks, then `synced`. A recursive delete notifies each deleted
path, as a `Watchable` change feed announces each key, so streams learn
which chambers went. Each stream keeps a hash of the last data it sent for
each path and sends only views that differ, so a write that leaves a view
unchanged sends nothing; the events from one change are buffered and flushed
together. A subtree that grows past `MaxSubtreeChambers` ends its stream, and
the reconnect is refused with 413. Subtree streams count toward
`MaxStreams`.

**Compression.** Subtree reads and watches are gzipped when the request's
`Accept-Encoding` allows it (q-values honoured), with `Content-Encoding: gzip`;
every subtree response, errors included, carries `Vary: Accept-Encoding`, and
errors sent before the body are never compressed. Merged chambers repeat each
other, so a `/prod/` write fanning out to 500 views costs a stream about
19 KB instead of 1.2 MB, for about 1.2 ms of CPU per stream at
`flate.BestSpeed`. Every flush a subtree response makes (the read's every 64
entries, a stream's batch of events, a heartbeat) is a deflate sync flush, so
events arrive as promptly as uncompressed ones, and a stream that ends closes
its gzip body cleanly. A stream borrows a compressor from a pool for one batch
and returns it at the flush (a compressor holds about half a megabyte), so an
idle stream holds none; batches therefore do not reference earlier ones, which
costs little, as deflate looks back only 32 KB. Since every stream compresses
its own copy of each event, `disableSubtreeCompression` in the server config
(`HandlerConfig.DisableSubtreeCompression`) turns it off. Single-chamber reads
and watches, `inherit=layers`, and evaluation streams (public, and shaped by
the caller's context) are never compressed. Both SDKs' HTTP stacks ask for
gzip and decompress as they read (the Go client must not set
`Accept-Encoding` itself, or Go's transport stops decompressing), and the Go
SDK's size bounds apply to the decompressed bytes.

A write high in the tree wakes every subtree stream below it at once, and can
change thousands of views. Rather than each stream reading them, the streams
on a replica share a **per-change read cache** (`sharedViews`): for the
broker's current generation, each stored chamber is read at most once
(`storage.ReadLayer`) and each view is merged from those reads once
(`storage.BuildView`) and marshaled once, then handed to every stream that
asks. A snapshot on a `Scanner` backend first fills the cache with one `Scan`
of the root (`storage.ScanLayers`), shared by the streams of that root, so a
stream's first load (and every resync and reconnect) is a range read rather
than a read per chamber. Like `sharedReads`, a stream only shares what was read after the latest
change announced before it asked, so it never receives a view read before the
change that woke it. Shared reads are detached from the stream that started
them and bounded by the request timeout; failures are not shared (the next
caller reads again), and the cache is dropped 30 seconds after the latest
change (`sharedViewsTTL`). The Go SDK's `subtreeStream` (`pkg/subtree.go`)
stages the snapshot until the first `synced` and then swaps it in whole;
after every `synced`, it drops the paths that stream did not send, which is
how it learns of deletions made while it was disconnected. A view sent with
an `error` keeps the last good view of its path. Neither SDK
resumes from `Last-Event-ID` yet.

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
- **`Scanner`** — `Scan(ctx, prefix)` and `ScanKeys(ctx, prefix)` yield every
  entry (or key) at or below a path in byte order of key, in one range read,
  streamed where the backend allows (`postgres`: one statement over two index
  ranges, skipping `/_realm/` at `/`; `etcd`: pages at one revision;
  `boltdb`: one read transaction, copied out so a slow reader cannot stall
  writers). Subtree reads use it (see Request flow). `cacheable` does not pass
  it on, so reads through a cache keep going through the cache.
- **`HealthReporter`** — `Health()` reports trouble beyond failing reads
  (`postgres`, `etcd`: a change feed that has not caught up for 30 seconds), which
  fails `/readyz`.

All read-modify-write sequences go through `storage.Update` (or
`UpdateJSON`): a chamber write with its If-Match check and history entry, the
catalog, and API keys. `Update` holds a process-wide lock on the keys, so
writers in one process take turns rather than conflict. On a `Transactional`
backend it also version-checks the keys it read and retries with jittered
backoff on conflict with another process, so replicas need no shared lock.
On other backends the process-wide lock is all there is, which is why those
backends are single-replica. A backend can also implement
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
  wins, whole, unless an ancestor seals it (`"sealed": true`): then the
  farthest ancestor that seals it wins, and nearer definitions are hidden. A missing ancestor (including `/`) contributes nothing. This
  hierarchy is the current stand-in for "environments / zones". `Get` is built
  from `Layers` (the chamber plus each consulted ancestor, nearest first, in
  `lineage.go`) and `Lineage.Merge`, so the layers view and the merged view
  cannot disagree. It still reads every layer on each `Get`, but parses and
  encodes only what changed (`inheritable_cache.go`): it keeps each chamber
  as last parsed, reused only for the same stored bytes, and each merged view
  as last encoded, reused only while it was merged from those same parsed
  chambers. Nothing needs invalidating, so writes through other wrappers or
  replicas are seen at once; both caches are capped at 8 MiB and forget
  entries at random. It implements `Unwrap()` so the server can reach the raw
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
| Per-flag targeting off (serve the default, keep the rules) | ✅ Done | `PausedTargeting` in `pkg/flag.go`; Pause/Resume in `http/realm-ui` |
| Flag descriptions, owners, lifecycles, and value constraints (bind the chambers below) | ✅ Done | `pkg/schema.go`, `pkg/flag.go`, `http/constraints.go`, `http/realm-ui` ([design](design/constraints.md)) |
| Recurring schedules (weekly windows in a time zone) | ⬜ Next | `pkg/targeting.go` |
| Optimistic concurrency, change history, attribute catalog | ✅ Done | `http/meta.go`, `http/meta_handlers.go` |
| S3 storage | ✅ Done | `pkg/storage/s3.go` |
| Postgres storage + multi-replica safety | ✅ Done | `pkg/storage/postgres.go`, `storage.Update` in `pkg/storage/txn.go` |
| etcd storage (multi-replica) | ✅ Done | `pkg/storage/etcd.go`; shared contract tests in `pkg/storage/conformance_test.go` |
| **Real-time streaming ("immediately sourced", SSE)** | ✅ Done | `http/broker.go`, `streamChamber` in `http/handler.go`, `Realm.stream` in `pkg/realm.go` |
| Cross-replica stream fanout (multi-instance) | ✅ Done | `Watchable` storage feeds the broker (`http/handler.go`) |
| Subtree reads/watches; a chamber per request from a path template | ✅ Done | `http/subtree.go`, `pkg/storage/subtree.go`, `WithPathTemplate`, `pathTemplate` in `sdk/typescript` ([design](design/subtree.md)) |
| Revisions on reads, stream event ids, and in the SDKs | ✅ Done | `Lineage.Revision`, `api.RevisionHeader`, `Realm.Revision`, `revision()` in `sdk/typescript` |
| Revisioned streams (resume with `Last-Event-ID`, SDKs reject stale data) | ⬜ Next | builds on the event ids above |
| SDK API keys + OIDC/GitHub sign-in for the UI, roles | ✅ Done | `auth/`, `http/auth.go` |
| Per-path roles for signed-in users | ✅ Done | `Grant.Paths`, `Config.RolesFor`, `Principal.RoleAt` in `auth/`; `http/realm-ui/src/utils/access.ts` |
| TypeScript SDK + server-side evaluation endpoint | ✅ Done | `sdk/typescript`, `http/evaluate.go`, shared cases in `sdk/conformance` |
| Audit log of flag changes | ✅ Done | per-chamber history with the authenticated actor (`http/meta.go`) |
| Multi-language SDKs | ⬜ Next | new clients against `/v1/chambers` |
| UI editing of targeting, rollouts, diff confirmation, history/restore | ✅ Done | `http/realm-ui` |
