# Architecture

`realm` is a feature-flag / configuration platform. A single Go binary is a
**server**, a **CLI**, and an importable **Go SDK**. This document explains the
data model, how a value is resolved, how a request flows through the system, and
where the project sits relative to its "open-source LaunchDarkly" goal.

## Data model

Flags are stored as JSON and modeled by four nested types in `pkg/`:

```
Chamber                      // a namespace: map[ruleName] -> OverrideableRule
└── OverrideableRule         // one flag
    ├── Rule                 // { type, value }  (string | number | boolean | custom)
    ├── Overrides []Override // value scoped to an app semver range
    └── Rollout   *Rollout   // value served to a % of evaluation contexts
```

- **`Rule`** (`pkg/rule.go`) — a `type` and a `value`. `UnmarshalJSON` strictly
  validates the value against the declared type (`assertType`).
- **`Override`** (`pkg/override.go`) — embeds `*Rule` and adds
  `minimumVersion`/`maximumVersion`. Selected when the app version falls inside
  the (non-overlapping, validated) semver range.
- **`Rollout`** (`pkg/rollout.go`) — embeds `*Rule` and adds `percentage`
  (`[0,100]`) and an optional `seed`. Selected for the deterministic subset of
  evaluation contexts whose bucket falls under the percentage.
- **`Chamber`** (`pkg/chamber.go`) — a `map[string]*OverrideableRule` plus
  `InheritFrom` (fill missing rules from a parent) and `OverwriteFrom` (used for
  PATCH merges). **`ChamberEntry`** is the immutable, version-bound read view the
  SDK evaluates against.

### Value resolution order

`OverrideableRule.ValueFor(ruleKey, ec, version)` (`pkg/rule.go`) resolves a
value in two steps:

1. **Version override** — `ValueAtVersion(version)` picks the override whose
   semver range contains the app version, else the base value.
2. **Rollout** — if a `Rollout` is configured and the `EvaluationContext.Key`
   buckets under the rollout percentage, the rollout value is served instead.

```
value = base
      → overridden by matching version range
      → replaced by rollout value when the context buckets in
```

An **`EvaluationContext`** (`pkg/context.go`) carries the per-subject `Key`
(used for bucketing) and reserved `Attributes`. It travels through the request
context via `Realm.NewContextWithEvaluation`, so a single logical request
evaluates every rule consistently. An empty `Key` makes rollouts inert — callers
that don't opt in are unaffected.

### Deterministic bucketing

`bucket(seed, key)` (`pkg/rollout.go`) hashes `"<seed>.<key>"` with SHA-1, takes
the 15 leading hex digits (60 bits), and normalizes to `[0,100)` — the
LaunchDarkly-style algorithm. The same key always lands in the same bucket, so
rollout assignment is **sticky**. `seed` defaults to the rule key, so each flag
buckets independently; sharing a seed rolls flags out to the same population
together.

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
  `DELETE`→delete). Cross-cutting: gzip, per-request timeout, OTel spans/metrics,
  `no-store` cache header, `X-Realm-Hostname`.
- **Persistence note:** PUT/PATCH unmarshal the body into a `realm.Chamber` and
  re-marshal it before storing. Any json-tagged field on the domain types
  (including `Rollout`) therefore round-trips through the server automatically —
  new rule capabilities need **no** handler changes.

The SDK (`pkg/realm.go`) fetches the chamber at its configured path on `Start`
and then refreshes on a ticker (`DefaultPollingInterval = 15m`), swapping the
immutable `ChamberEntry` snapshot under a mutex. Reads (`Bool`/`String`/
`Float64`/`CustomValue`) pull the snapshot (and any `EvaluationContext`) from the
request context.

## Storage

`pkg/storage/storage.go` defines `Storage{Get,Put,Delete,List,Close}` over
`StorageEntry{Key, Value}`. Backends self-register in the `StorageOptions` map:

| Backend | File | Notes |
|---|---|---|
| `file` | `file.go` | JSON files on disk |
| `boltdb` | `boltdb.go` | embedded bbolt key/value store |
| `bigcache` | `bigcache.go` | in-memory (dev / cache tier) |
| `gcs` | `gcs.go` | Google Cloud Storage |

Two wrappers compose backends:

- **`cacheable`** (`cacheable.go`) — a write-through `cache` over a `source`
  (e.g. `bigcache` over `boltdb`).
- **`inheritable`** (`inheritable.go`, enabled by `inheritable: true`) — on
  `Get`, merges a chamber with all of its ancestors along the path so child
  chambers inherit parent rules. This hierarchy is the current stand-in for
  "environments / zones".

Server config (`configs/realm.json`, parsed in `cmd/config.go`) selects the
storage type and options, TLS cert/key, port, and `inheritable`.

## Observability

OpenTelemetry is set up in `trace/otel.go` (traces + metrics, OTLP/stdout
exporters). Logging goes through `helper/logging` — a zerolog `TracedLogger`
accessed as `logging.Ctx(ctx)`, so logs are correlated with the active span.
Handlers and commands wrap work in `tracer.Start(ctx, "…")`.

## Path to LaunchDarkly parity

| Capability | Status | Where |
|---|---|---|
| Typed flags (bool/number/string/custom) | ✅ Done | `pkg/rule.go` |
| Version-range overrides | ✅ Done | `pkg/override.go` |
| **Percentage rollout (deterministic bucketing)** | ✅ Done | `pkg/rollout.go` |
| Hierarchy / inheritance (proto "zones") | ✅ Done | `pkg/storage/inheritable.go` |
| Attribute/segment targeting (rules on `EvaluationContext.Attributes`) | ⬜ Next | seam exists in `pkg/context.go` |
| Real-time streaming ("immediately sourced", SSE) instead of polling | ⬜ Next | `pkg/realm.go` polling loop |
| Environment API-key auth | ⬜ Next | `http/handler.go` middleware |
| Audit log of flag changes | ⬜ Next | around `http/handler.go` write ops |
| Multi-language SDKs | ⬜ Next | new clients against `/v1/chambers` |
| UI editing of rollouts/targeting | ⬜ Next | `http/realm-ui` |

`EvaluationContext.Attributes` is deliberately in place but unused: it is the
seam attribute targeting will build on, so that feature can land without another
signature change to the read API.
