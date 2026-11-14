# Subtree reads and watches

Status: being built (issue #144). This document is the contract the server,
the Go SDK, and the TypeScript SDK are built against.

A service that serves many tenants needs the chamber of every location it
may be asked about. Rather than one SDK client and one stream per chamber, it
reads and watches every chamber below a path at once, and picks the chamber
for each request from the evaluation context.

## Reading a subtree

```text
GET /v1/chambers/<root>?subtree=true
```

The response holds the inherited view (the same `rules` a plain `GET` of
that path returns) of the root and of every chamber below it:

```json
{"data": {"chambers": [
  {"path": "/prod/tenants/", "rules": {}, "revision": 41},
  {"path": "/prod/tenants/t-42/", "rules": {"…": {}}, "revision": 57},
  {"path": "/prod/tenants/t-42/loc-7/", "error": "could not parse parent chamber /prod/tenants/t-42/: …"}
]}}
```

- **Paths** are canonical: they start and end with `/`.
- **Order:** parents come before children. Siblings are sorted by path.
- **The root comes first, always.** When no chamber exists at the root, its
  entry is the view built from the root's ancestors alone. With no chamber
  above it either, its `rules` is `{}`. A subtree read therefore never
  answers `404` for a missing root.
- **Below the root**, there is one entry for each path that holds a chamber.
  A path that only leads to chambers below it has no entry.
- **`revision`** is the view's revision, as `X-Realm-Revision` reports for a
  plain read: the highest storage revision among the chamber and the parents
  its view merges. It is omitted when the storage does not track revisions.
  The response carries no revision header.
- **A view that cannot be built** (this chamber or one above it cannot be
  parsed) has an entry with `path` and `error` instead of `rules`, and the
  response carries on. Clients keep any view they already hold for that path.
- **Written as read.** The server writes entries as it reads them, without
  holding the whole subtree in memory. A failure partway through ends the
  body early, and the client sees JSON that does not parse, which it treats
  as a failed load and retries.
- **Limits.** `HandlerConfig.MaxSubtreeChambers` (default 10,000) caps the
  number of chambers. A larger subtree is refused with `413` before anything
  is written.
- **Combinations.** `subtree=true` cannot be combined with `inherit=false` or
  `inherit=layers` (`400`).
- **Auth.** The caller needs read access to the root (`CanRead(root)`). A
  key's scope covers every path below its own. At `/`, realm's reserved
  `/_realm/` paths are left out, as they are from listings.

## Watching a subtree

```text
GET /v1/chambers/<root>?subtree=true&watch=true
```

The response is a server-sent event stream:

```text
event: chamber
id: 57
data: {"path":"/prod/tenants/t-42/","rules":{…}}

event: deleted
data: {"path":"/prod/tenants/t-42/loc-9/"}

event: synced
data: {"chambers":512}
```

- **On connect**, the server sends one `chamber` event for each entry a
  subtree read would return (the root first, then parents before children),
  then `synced`. `chambers` counts the views the snapshot held, including
  any it could not build.
- **After `synced`**, the server sends a `chamber` event for each view that
  changed, and a `deleted` event for each chamber that was deleted. A write
  that leaves a view unchanged sends nothing for it. The root is never
  `deleted`: when its chamber is deleted, its view becomes the one built from
  its ancestors.
- **`id`** is the view's revision, absent when the storage does not track
  revisions.
- **A view that cannot be built** sends nothing. Clients keep the last view
  they received for that path, as single-chamber streams do.
- **Resync.** When the server cannot tell which views changed (too many
  changes at once, say), it sends the whole snapshot again, ending with
  `synced`.
- **Reconnects.** A stream may end at any time (streams have a maximum
  lifetime). The client reconnects, and after the new stream's `synced`,
  drops every path that stream did not send. That is how it learns of
  deletions made while it was disconnected. Resuming from `Last-Event-ID` is
  not supported yet.
- Heartbeats (`: ping`), the stream cap (`503` with `Retry-After`), and
  stream lifetimes work as for single-chamber watches.
- Clients ignore events with other names, which newer servers may send.

### Cost of a write high in the tree

A write to `/prod/` can change the view of every chamber below it. The
server keeps that cheap:

1. Each stored chamber is read at most once per change, and every affected
   view is merged from those reads in memory. All streams on a replica share
   the reads.
2. Each stream remembers what it last sent for each path, and sends only
   views that changed.
3. The events from one change are written together and flushed once.

## Picking a chamber from a path template

The SDKs read a subtree when given a path template instead of a path:

```text
/prod/tenants/{tenantId}/{locationId}/
```

### Templates

- A template starts with `/`. A missing trailing `/` is added.
- Each segment between slashes is either literal text without braces, or a
  placeholder that is the whole segment: `{name}`, where `name` is one or
  more of `A-Z a-z 0-9 _ - .`.
- Empty segments (`//`), partial placeholders (`x{a}`), and empty names
  (`{}`) are invalid. The SDK refuses an invalid template when it is
  constructed.
- A template without placeholders is valid, and always resolves to its root.
- The **root** of a template is its literal prefix up to the first
  placeholder, for example `/prod/tenants/`. The SDK reads and watches the
  subtree at the root.

### Resolution

For an evaluation context:

1. Start with the root as the only candidate.
2. Take the remaining segments in order:
   - A literal segment is appended to the path so far.
   - A placeholder is filled with the attribute of that name, looked up as
     targeting clauses look it up. `key` is the context's key, and the
     context's attributes take precedence over process-wide ones (including
     `version`). Resolution stops at a placeholder whose value is missing or
     empty, contains `/`, equals `.`, or contains `..`.

   Each path built this way, after each segment, is another candidate.
3. The chamber is the view of the **deepest candidate the snapshot holds a
   view for**. A candidate need not have a chamber for a deeper one to be
   used.
4. If the snapshot holds no view for any candidate (before the first load,
   say), there is no chamber. Getters return the caller's default with the
   SDK's usual "no chamber" error.

So a new location with no chamber yet gets its tenant's view, and a tenant
with no chamber gets the root's view. Because a mistyped attribute falls back
silently too, the SDKs report which path a context resolved to
(`Realm.ChamberPath(ctx)` in Go, `chamberPath(context)` in TypeScript).

`sdk/conformance/subtree.json` holds resolution cases that both SDKs run.

### Go SDK

- **`WithPathTemplate(template)`** cannot be combined with `WithPath`.
  Polling reads the subtree, and `WithStreaming(true)` watches it.
- **`NewContextWithEvaluation`** resolves the template once and pins the
  chosen view to the context, so flag getters stay allocation-free.
- **`NewContext`** (no evaluation context) pins the root's view.
- **A getter called with a context that pins nothing** resolves from the
  context's evaluation context on every call.
- **`Revision(ctx)`** is the pinned view's revision.

### TypeScript SDK

- **`ServerClient`** takes a `pathTemplate` option instead of `path`.
- Getters resolve the template from the context passed to them.
- `revision(context)` and `chamberPath(context)` report the revision and the
  path of the view that context resolves to.

## Not covered yet

- Resuming a stream from `Last-Event-ID`.
- Range reads (`Scan`) for backends that can list a prefix in one query.
  Until then, a subtree read lists each level of the tree.
- Compressing streams.
- Subtrees in the evaluation endpoint, or for `evaluate` keys.
