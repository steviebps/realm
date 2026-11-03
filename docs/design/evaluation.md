# Server-side evaluation and the TypeScript SDK

The Go SDK downloads a chamber and evaluates flags locally. That is the right
model for backend services, but not for browsers: a chamber holds every
targeting rule, including tenant lists, rollout seeds, and unreleased flags.
realm therefore offers two ways to read flags:

| Client | Credential | Gets | Evaluates |
|---|---|---|---|
| Backend services (Go SDK, TypeScript `ServerClient`) | `read` key | the whole chamber | locally |
| Browsers and other untrusted clients (TypeScript `BrowserClient`) | `evaluate` key | only the values for one context | on the server |

## Evaluation endpoint

```text
POST /v1/evaluate/<path>
{"key": "user-123", "attributes": {"tenantId": "acme", "version": "2.4.0"}}
```

The server reads the chamber at `<path>`, including rules inherited from its
parents, and evaluates every flag for the context with the same code the Go
SDK uses (`Chamber.Evaluate`). The response holds only results:

```json
{"data": {"flags": {"new-checkout": {"type": "boolean", "value": true}}}}
```

`key` and `attributes` are both optional. The body is limited to 64 KiB. A
chamber that does not exist returns `404`.

### Streaming

`POST /v1/evaluate/<path>?watch=true` with the same body opens a server-sent
event stream. The server sends an `event: flags` with the current results at
once, then again whenever a write changes the results for that context.
Writes that do not change this context's values send nothing. Comment
heartbeats (`: ping`) arrive every 30 seconds.

The stream is a `POST` because the context travels in the body. Browsers'
`EventSource` can only `GET` and cannot send an `Authorization` header, so the
TypeScript SDK reads the stream with `fetch`.

### Who may evaluate

`Principal.CanEvaluate(path)` allows:

- API keys of any access level whose path covers `<path>`;
- signed-in users with any role;
- everyone, when authentication is off.

## Evaluate keys

A key's `access` may now be `evaluate`, besides `read` and `write`. An
evaluate key can call `/v1/evaluate/` for chambers at or beneath its path and
nothing else: it cannot read chambers, their history, or the catalog. It is
meant to be shipped in front-end code, so treat it as public. It reveals the
values a context would receive, never the rules behind them.

`read` and `write` keys may call `/v1/evaluate/` too, so backends in other
languages can evaluate without porting the rules engine, but never from a
browser: a key request that carries an `Origin` header (which browsers send
with every `POST`) is refused with 403 unless it is an evaluate key. That
does not protect a key that has already leaked, since other clients need not
send `Origin`; it makes putting a secret key in front-end code fail the first
time it runs in development, before it ships. Other routes need no such check:
they send no CORS headers, so a page on another origin cannot read them.

## Browsers on other origins

Responses under `/v1/evaluate/` carry CORS headers so pages on any origin can
call them with a bearer token: `Access-Control-Allow-Origin: *`, allowing the
`Authorization` and `Content-Type` headers and the `POST` method. Credentials
(cookies) are never allowed cross-origin, so a signed-in session cannot be
used from another site. Preflight `OPTIONS` requests need no credentials. No
other route sends CORS headers.

## TypeScript SDK

`sdk/typescript` is one package with no runtime dependencies. It runs
anywhere with `fetch` and streaming response bodies: Node 18+, Bun, Deno,
browsers, and edge runtimes. It is not published to npm yet; depend on it
through a workspace or `file:` link.

```ts
import { ServerClient, BrowserClient } from 'realm-sdk';

// Backend: a read key, evaluates locally, like the Go SDK.
const realm = new ServerClient({
  address: 'https://realm.example.com',
  token: process.env.REALM_TOKEN,
  path: '/prod/checkout/',
  attributes: { environment: 'prod' }, // process-wide defaults
  version: '2.4.0',
});
await realm.start();
realm.bool('new-checkout', { key: userId, attributes: { tenantId } }, false);

// Browser: an evaluate key, one context at a time.
const flags = new BrowserClient({
  address: 'https://realm.example.com',
  token: 'realm_…', // an evaluate key
  path: '/prod/checkout/',
  context: { key: userId, attributes: { tenantId } },
});
await flags.start();
flags.bool('new-checkout', false);
flags.subscribe(() => render());
await flags.identify({ key: otherUser }); // re-evaluates for a new context
```

`realm-sdk/react` wraps a `BrowserClient` for React 18+: a `RealmProvider`
that starts the client and re-identifies when its `context` prop changes by
value, and hooks (`useBoolFlag`, `useJsonFlag`, `useRealmStatus`, …) built
on `useSyncExternalStore`. React is an optional peer dependency, so the main
entry point keeps no runtime dependencies.

Both clients stream by default and reconnect with capped exponential backoff
(1 s to 30 s), keeping the last known values meanwhile. `streaming: false`
switches to polling every `pollingInterval` milliseconds. Getters never
throw: a missing flag or a value of the wrong type returns the default.

### Matching the Go SDK

`ServerClient` evaluates with a TypeScript port of `pkg/targeting.go`: the
same operators, Go's `x/mod/semver` rules (including `v1` shorthand and
prerelease ordering), and the same bucketing (SHA-1 of `<seed>.<value>`,
first 60 bits, scaled to [0, 100)). SHA-1 is implemented in the package so
evaluation stays synchronous everywhere.

`sdk/conformance/evaluation.json` holds shared cases: chambers, contexts, and
the expected value for each flag, plus raw bucket values. The Go tests
(`pkg/conformance_test.go`) and the TypeScript tests run the same file, so the
two implementations cannot silently disagree. Add a case there whenever
evaluation changes.

## Not yet built

- Publishing to npm.
- A per-key allowlist of flags for evaluate keys (they see every flag's value
  beneath their path).
- Cross-replica fanout: like chamber streams, evaluation streams only see
  writes made on the same replica.
