# Design: attribute targeting, S3 storage, and safe editing (phase 1)

Status: **implemented** · Supersedes the `overrides` and `rollout` fields.

## Goals

- Target a flag at specific **tenants, locations, environments, services,
  application versions, or builds** — by attributes the application supplies,
  without realm knowing anything about the application's domain.
- Progressively roll a value out to a **percentage** of any population
  (users, devices, locations…), with sticky assignment.
- Let **non-engineers** edit targeting safely: human-readable pickers, a
  confirmed diff before save, no lost updates, and a revertable history.
- Keep every SDK in sync within **seconds** (existing SSE streaming).
- Run cheaply as a **single instance on S3**, with seams for Postgres and
  multi-replica fanout later.

## Non-goals (phase 2+)

API keys and OIDC login · TypeScript SDK and the server-side evaluation
endpoint for browsers · Postgres storage and cross-replica fanout · multi-way
(A/B/n) variant splits · kill switch (since built: see
[Turning targeting off](#turning-targeting-off)).

## Data model

A chamber maps flag keys to **flags**. A flag has a type, a default value, and
an ordered list of targeting **rules**. `overrides` and `rollout` are
**removed** (breaking change; realm is pre-1.0).

```json
{
  "rules": {
    "checkout.newFlow": {
      "type": "boolean",
      "value": false,
      "targeting": [
        {
          "id": "pilot-locations",
          "description": "Pilot stores in production",
          "clauses": [
            { "attribute": "environment", "op": "in", "values": ["prod"] },
            { "attribute": "locationId", "op": "in", "values": ["7f3c…", "a91e…"] }
          ],
          "value": true
        },
        {
          "id": "new-builds",
          "clauses": [
            { "attribute": "version", "op": "semverGte", "values": ["v2.4.0"] }
          ],
          "value": true,
          "rollout": { "percentage": 25, "bucketBy": "locationId" }
        }
      ]
    }
  }
}
```

### Evaluation

```
for rule in flag.targeting (top to bottom):
    if rule.schedule is set and now is outside it: continue
    if every clause matches the context:          # clauses are ANDed; none = match all
        if rule.rollout is nil:                    return rule.value
        if bucket(seed, ctx[bucketBy]) < percentage: return rule.value
        # not bucketed in → continue to the next rule
return flag.value                                  # fallthrough
```

- A clause matches when the context attribute is present and the operator
  holds for **any** of `values`. A missing attribute never matches (`notIn`
  included), so a context that omits an attribute cannot accidentally land in
  a targeted rule.
- Every value (`flag.value`, `rule.value`) is type-checked against
  `flag.type` in `UnmarshalJSON`, as today.

### Rule ids and exposure

A rule's `id` is optional and unique within its flag. The Go SDK and the
TypeScript `ServerClient` (given a `meter`) count each value a getter returns,
and the evaluation endpoint each flag it serves, as the OpenTelemetry counter
`realm.flag.evaluations`, with the attributes `flag`, `path`, and `rule`: the id of the rule that served
it, its position (`#2`) when it has none, or `default` when no rule applied
(or targeting is turned off). Recording the rule rather than the value keeps
the number of series bounded for string and `custom` flags, and lets an
application compare a rollout's cohort with the rest by joining on the rule
id. A position changes when rules are reordered, so the UI gives each rule it
adds a generated id (`r-k3x9q2`), and writes refuse the ids `default` and
those starting with `#`, which would collide with those names. Reads accept
them, as they accept every stored chamber.

The counts are process-wide atomic counters (`internal/evaluations`), read
when the meter provider collects. Each view the Go SDK holds finds a flag's
counters on the flag's first read from it (`pkg/evaluations.go`), so every
read after that adds one atomic increment and no allocation. Only a value
served counts: a getter returning the caller's default (a missing flag, or one
of another type) counts nothing, and so do the TypeScript `allFlags` and
`initialState`, whose values reach browsers that the evaluation endpoint
counts. An evaluation stream counts each flag of its first event, then a flag
again only when an event serves it by another rule, so a tab held open counts
by the cohorts it joins rather than by how often chambers are written.
`sdk/conformance/evaluation.json` names the rule that serves each flag, so
every implementation reports the same rule.

### Operators

| op | Meaning | Typical use |
|---|---|---|
| `in` / `notIn` | exact string equality | `tenantId`, `locationId`, `environment`, `service`, `region` |
| `startsWith` | string prefix | `buildSha` (matches short and full SHAs) |
| `semverGte` / `semverLte` | semver comparison (inclusive), one value | `version` ranges = two clauses |

Values are strings. Numeric and regex operators can be added later without a
schema change.

### Context attributes

`EvaluationContext{Key, Attributes}` becomes the single input. `Key` is exposed
to clauses and bucketing as the attribute `key`. Attributes come from two
places, merged per evaluation (request wins):

1. **Static, per process** — `WithAttributes(map[string]string)` on the SDK,
   for deployment facts: `service`, `environment`, `region`, `buildSha`.
   `WithVersion(v)` remains as sugar for the `version` attribute.
2. **Per request** — `NewContextWithEvaluation(ctx, ec)`, for subject facts:
   `tenantId`, `locationId`, user `key`.

Attribute names are conventions, not enforced. The docs recommend the names
above so the UI's pickers and catalog line up across applications.

### Rollouts

`rollout: {percentage, bucketBy?, seed?}` on a rule. `bucketBy` defaults to
`key`; setting it to `locationId` rolls out by **whole location**, so every
device and user in one location gets the same answer. `seed` defaults to the
flag key; the bucketing algorithm is unchanged (sticky across releases).

### Visitors who aren't signed in

A context without the `bucketBy` attribute is never in the rollout, so a
visitor who isn't signed in, with no user ID for a `key`, would always get
the next rule or the default. Bucketing needs a value that stays the same
from one evaluation to the next, so such a visitor gets a **visitor ID**: a
random UUID kept in a first-party cookie, `realm_visitor`, which a server
rendering the page and the browser running it both read. By convention the
context carries it as:

| Visitor | `key` | `anonymous` | `visitorId` |
|---|---|---|---|
| not signed in | the visitor ID | `"true"` | the visitor ID |
| signed in | the user ID | `"false"` | the visitor ID |

Which population a rule's rollout counts is then the rule's choice:

- `rollout: {percentage: 20}` (bucketing on `key`): 20% of users and 20% of
  visitors who aren't signed in. A visitor's bucket can change at sign-in,
  when their key changes.
- a clause `anonymous in ["true"]` with that rollout: 20% of visitors who
  aren't signed in; once signed in they go on to the next rule.
- `rollout: {percentage: 20, bucketBy: "visitorId"}`: 20% of browsers,
  kept through sign-in; one user can differ between devices, and contexts
  without a `visitorId` (backend jobs) are never in it.

Nothing in the server or the evaluation changes: a visitor ID is a key like
any other. The TypeScript SDK reads or creates the cookie: `serverVisitorId`
from a request's `Cookie` header (returning a `Set-Cookie` value for a new
ID), `browserVisitorId` from `document.cookie`. Another language can share
it: the value is a lower-case version 4 UUID (anything else is replaced),
written `Path=/; Max-Age=34560000; SameSite=Lax`, plus `Secure` over HTTPS,
and not `HttpOnly`, so the browser can read it.

- **Sticky as long as the cookie.** Cleared cookies, private windows, and
  other devices get new IDs, so a percentage of visitors is a percentage of
  visitor IDs. Safari keeps cookies written by script for at most seven
  days, so a server should set the cookie where it can.
- **Not access control.** The browser chooses its context, visitor ID
  included, so a visitor can pick their bucket; a rollout decides what
  visitors see, never what they may access.

### Schedules

`schedule: {start?, end?}` on a rule limits it to a window of time: it applies
from `start` (inclusive) until `end` (exclusive). Outside the window the rule
is skipped, exactly like a rule whose clauses do not match, so the next rule or
the default decides. For example, `true` for three hours on Wednesday
afternoon:

```json
{ "value": true, "schedule": { "start": "2026-11-04T14:00:00-05:00", "end": "2026-11-04T17:00:00-05:00" } }
```

- Bounds are RFC 3339 timestamps with an offset, so a window is the same span
  of instants on every host; the offset is kept as written. Either bound may
  be omitted for an open-ended window ("on from 2pm", "until Friday"), but not
  both. `end` must be after `start`, and bounds are limited to millisecond
  precision so every SDK compares them exactly.
- SDKs that evaluate locally (Go, the TypeScript `ServerClient`) compare
  against their own clock on every evaluation, so the value changes on time
  without a refresh; hosts switch over as closely as their clocks agree.
  Evaluating a whole chamber uses one instant for every flag.
- Evaluation streams (`POST /v1/evaluate/<path>?watch=true`) re-evaluate at
  the next schedule bound as well as after writes, so browsers see the change
  on time too. One-shot evaluations and polling clients see it at their next
  request.
- Windows are one-off. Recurring windows (every Wednesday 2–5pm) would need a
  time zone and its daylight-saving rules; they are not supported yet.

### Turning targeting off

A per-flag switch serves the default to every context and keeps the rules for
later. Turning targeting off moves the flag's rules from `targeting` to
`pausedTargeting`; turning it on moves them back:

```json
"checkout.newFlow": {
  "type": "boolean",
  "value": false,
  "pausedTargeting": [ { "id": "pilots", "clauses": [ … ], "value": true } ]
}
```

- Evaluation never reads `pausedTargeting`, in any SDK or on the server, so
  schedules in paused rules set no evaluation stream bounds either.
- A plain `"targetingOff": true` would not work with older SDKs: reads are
  lenient, so they would ignore it and go on applying the rules. Moving the
  rules works with every SDK. Older SDKs ignore `pausedTargeting`, see no
  `targeting`, and serve the default.
- Paused rules are validated as `targeting` rules are, so a paused rule can be
  turned back on unchanged. Writes are refused when a flag has rules in both
  lists. When stored data has both anyway, `targeting` is served, as older
  SDKs serve it.
- Rollouts are deterministic, so turning targeting back on serves the feature
  to the same contexts as before.
- The switch is part of one chamber's definition. Chambers below that override
  the flag are not affected; sealing the flag is what makes one definition
  serve everywhere below.

## Attribute catalog

So the UI can show names instead of opaque IDs, realm keeps a global,
application-supplied catalog:

```
GET    /v1/catalog                      → list attributes
GET    /v1/catalog/<attribute>          → { "values": { "<id>": "<label>" } }
PUT    /v1/catalog/<attribute>          → replace
PATCH  /v1/catalog/<attribute>          → upsert labels (null deletes one)
```

The application keeps it current — for example by calling `PATCH` when a
location is created or renamed. The UI uses it for search-as-you-type pickers
and shows labels in rules and diffs. Unknown IDs still work; they render raw.
The catalog is stored as one object under the reserved storage prefix
`_realm/`, which the chamber APIs reject as a path and hide from listings.
Metadata always goes to the raw storage beneath the `inheritable` wrapper.

## Safe editing

- **Optimistic concurrency.** `GET /v1/chambers/<path>?inherit=false` returns an `ETag`
  (content hash of the chamber *as stored at that path*, not its inherited
  view, so editing a parent never invalidates a child's ETag).
  `POST`/`PATCH`/`DELETE` honor `If-Match` (and `If-None-Match: *` for
  create-only) and return `412 Precondition Failed` with the current `ETag` on
  mismatch. The check runs under a per-path lock in the server, which is
  correct for a single instance. Multi-instance safety arrives with the
  Postgres backend (transactional compare-and-set); storage-level conditional
  writes do not compose with the `cacheable`/`inheritable` wrappers.
- **History.** Every successful write appends
  `{revision, time, actor, operation, etag, chamber}` under
  `_realm/history/<escaped path>/` (the most recent 200 are kept).
  `GET /v1/history/<path>` lists revisions; `GET …?revision=N` returns one.
  Revert is a normal write of an old revision (with `If-Match`), so it is
  itself recorded. `actor` comes from an `X-Realm-Actor` header until OIDC
  lands in phase 2.
- **Confirm before save (UI).** The editor computes a per-flag diff (rules
  added/removed/reordered, clause and value changes, with catalog labels) and
  requires confirmation. A 412 reloads and shows what changed underneath.

## S3 storage backend

New `s3` backend mirroring `gcs`: config `bucket`, `region`, `prefix`, and
optional `endpoint` + `forcePathStyle` (MinIO/LocalStack for tests and local
dev). Recommended
deployment: `cacheable` with `bigcache` in front of `s3`, single replica.
Flag evaluation never touches storage — SDKs evaluate an in-memory snapshot —
so S3 latency affects admin writes and cold start only.

## Go SDK changes

- `WithAttributes(map[string]string) RealmOption` (static attributes).
- `EvaluationContext.Attributes` becomes `map[string]string`.
- `ChamberEntry` no longer binds a version; it evaluates against the merged
  context.
- Removed: `Override`, `Rollout` (as a flag field), `ValueAtVersion`, and the
  version-only `*Value` helpers.

## UI

- Flag editor: default value, ordered rules (drag to reorder), clause builder
  with an attribute dropdown (from the catalog plus recently used names),
  operator dropdown, and value pickers that search the catalog.
- Rollout slider per rule with a "bucket by" selector.
- Optional schedule per rule: start and end pickers in the editor's time zone.
- Save → diff confirmation → `If-Match` write; history drawer with revert.

## Build order

1. Rules engine in `pkg/` (types, validation, evaluation, benchmarks); remove
   overrides/rollout; SDK `WithAttributes`.
2. ETag/`If-Match` + history + catalog endpoints in `http/`.
3. `s3` storage backend (+ conditional writes), tested against MinIO.
4. UI: rule editor, catalog pickers, diff confirmation, history/revert.
5. Docs: `ARCHITECTURE.md`, `CLAUDE.md`, an end-to-end example.
