# @steviebps/realm-sdk

Feature flags for TypeScript and JavaScript, backed by
[realm](https://github.com/steviebps/realm): an open-source, self-hosted
feature-flag and configuration server.

```ts
if (realm.bool('new-checkout', { key: user.id, attributes: { tenantId: user.tenantId } }, false)) {
  // ship the new checkout to the tenants you choose, then to everyone
}
```

- **Change behavior without a deploy.** Turn features on and off, change
  limits and copy, and roll back in seconds from the realm web UI or API.
- **Release gradually.** Target users by any attribute (tenant, location,
  environment, app version, …), roll out to a sticky percentage, or schedule
  a rule for a window of time.
- **Updates arrive in real time.** Clients hold flags in memory and receive
  changes over server-sent events, so reading a flag never waits on the
  network.
- **Keep working when realm doesn't.** If the server is unreachable, clients
  keep the last values they had and retry in the background; a flag that
  can't be read returns the fallback you passed. Getters never throw.
- **Runs anywhere.** No runtime dependencies. Node 18+, Bun, Deno, browsers,
  and edge runtimes, plus hooks for React.

## Contents

- [How realm works](#how-realm-works)
- [Install](#install)
- [Quick start](#quick-start)
- [Choose a client](#choose-a-client)
- [Read flags on a server: `ServerClient`](#read-flags-on-a-server-serverclient)
- [Read flags in a browser: `BrowserClient`](#read-flags-in-a-browser-browserclient)
- [React](#react)
- [How clients stay current](#how-clients-stay-current)
- [API reference](#api-reference)
- [Learn more](#learn-more)
- [Contributing](#contributing)

## How realm works

A few terms appear throughout this guide:

- **Flag:** a named, typed value (`boolean`, `string`, `number`, or `custom`
  JSON) with a default and, optionally, **targeting rules** that serve other
  values to some contexts.
- **Chamber:** a group of flags at a path, such as `/prod/checkout/`. A
  chamber inherits the flags of the chambers above it (`/prod/`, then `/`), and
  can override them. Each client reads one chamber, or every chamber below a
  path.
- **Evaluation context:** who or what a flag is evaluated for: a `key` (a
  user or device id, say) and string `attributes` such as
  `{ tenantId: 'acme', location: 'us-east' }`. Targeting rules match these
  attributes, and percentage rollouts bucket on them, so a user keeps the
  same value from one request to the next.
- **Fallback:** the value you pass to every getter. It is returned when the
  flag is missing, has another type, or hasn't loaded yet.
- **API key:** a token from your realm server, scoped to a path and to one of
  `evaluate`, `read`, or `write` access. Servers that don't enable
  authentication need none.

For example, this flag is `true` for the `acme` tenant, `true` for 20% of the
users of app version 2.0 or later, and `false` for everyone else:

```json
{
  "type": "boolean",
  "value": false,
  "targeting": [
    { "clauses": [{ "attribute": "tenantId", "op": "in", "values": ["acme"] }], "value": true },
    {
      "clauses": [{ "attribute": "version", "op": "semverGte", "values": ["2.0.0"] }],
      "value": true,
      "rollout": { "percentage": 20 }
    }
  ]
}
```

You manage flags in the realm web UI, with the `realm` CLI, or over its HTTP
API; this SDK only reads them. See the
[realm README](https://github.com/steviebps/realm#readme) to run a server.

## Install

```bash
npm install @steviebps/realm-sdk
```

- **ESM only:** use `import`, not `require`.
- **Runtimes:** anything with `fetch` and streaming responses: Node 18+, Bun,
  Deno, modern browsers, and edge runtimes.
- **React (optional):** `@steviebps/realm-sdk/react` needs React 18 or later,
  an optional peer dependency installed only if you use it.
- **Versions:** the SDK is released with every realm release, at the same
  version as the server.

## Quick start

This starts a realm server with in-memory storage, creates a flag, and reads
it from Node.

1. Start a server in dev mode (no authentication; data is lost when it stops):

   ```bash
   docker run --rm -p 8080:8080 ghcr.io/steviebps/realm server --dev
   ```

2. Create the chamber `/prod/checkout/` with one flag, on for the `acme`
   tenant:

   ```bash
   curl -X POST http://localhost:8080/v1/chambers/prod/checkout/ \
     -H 'Content-Type: application/json' \
     -d '{"rules":{"new-checkout":{"type":"boolean","value":false,
          "targeting":[{"clauses":[{"attribute":"tenantId","op":"in","values":["acme"]}],"value":true}]}}}'
   ```

3. Read it from Node, in `flags.mjs`:

   ```ts
   import { ServerClient } from '@steviebps/realm-sdk';

   const realm = new ServerClient({ address: 'http://localhost:8080', path: '/prod/checkout/' });
   await realm.start();

   console.log(realm.bool('new-checkout', { key: 'user-1', attributes: { tenantId: 'acme' } }, false)); // true
   console.log(realm.bool('new-checkout', { key: 'user-2', attributes: { tenantId: 'globex' } }, false)); // false

   realm.stop();
   ```

   ```bash
   node flags.mjs
   ```

4. Open the web UI at <http://localhost:8080/ui/>, change the flag, and run
   the script again.

## Choose a client

The package has two clients. They share their options and getters, but
differ in where flags are evaluated, and so in which API key they need.

| | `ServerClient` | `BrowserClient` |
|---|---|---|
| **Use it in** | backend services: Node, Bun, Deno, edge functions | browsers, mobile web views, any code you ship to users |
| **API key** | `read` (keep it secret) | `evaluate` (safe to ship in front-end code) |
| **Evaluates flags** | locally, in your process: no request per read | on the server, for one context at a time |
| **Sees targeting rules** | yes | no: only the values for its context |
| **Contexts** | any context, passed to each getter | one context, set with `context` or `identify` |

Rule of thumb: if the code runs on a machine you control, use a
`ServerClient`. If it runs on a user's device, use a `BrowserClient`.

## Read flags on a server: `ServerClient`

A `ServerClient` downloads a chamber once, keeps it current, and evaluates
flags in memory, so you can read flags on every request without adding
latency. Make one client when your service starts and share it.

```ts
import { ServerClient } from '@steviebps/realm-sdk';

const realm = new ServerClient({
  address: 'https://realm.example.com',
  token: process.env.REALM_TOKEN, // a read key
  path: '/prod/checkout/',
  attributes: { environment: 'prod', service: 'checkout' }, // defaults for every context
  version: process.env.APP_VERSION, // the "version" attribute, for semver rules
});

await realm.start(); // resolves once the chamber has loaded
```

Pass a context to each getter, with the attributes your rules target:

```ts
const context = { key: user.id, attributes: { tenantId: user.tenantId } };

realm.bool('new-checkout', context, false);
realm.string('banner', context, '');
realm.number('max-items', context, 50);
realm.json<{ limit: number }>('quota', context, { limit: 10 }); // a copy you may change
realm.allFlags(context); // every flag's value, for example to bootstrap a page
```

Attributes in the context win over the process-wide `attributes` and
`version`, which fill in for the ones a context doesn't set.

When your service shuts down, call `realm.stop()` to close the stream.

### Serve many tenants from one client: `pathTemplate`

A service that serves many tenants or locations can give each one its own
chamber, and read all of them with one client. Pass `pathTemplate` instead of
`path`:

```ts
const realm = new ServerClient({
  address: 'https://realm.example.com',
  token: process.env.REALM_TOKEN, // a read key for /prod/tenants/
  pathTemplate: '/prod/tenants/{tenantId}/{locationId}/',
});
await realm.start(); // reads and watches every chamber below /prod/tenants/

const context = { key: user.id, attributes: { tenantId: 't-42', locationId: 'loc-7' } };
realm.bool('new-checkout', context, false); // evaluated against /prod/tenants/t-42/loc-7/
realm.chamberPath(context); // which chamber the context got
```

The client reads every chamber below the template's literal prefix (here
`/prod/tenants/`). For each read, it fills the placeholders from the
context's attributes (`{key}` is the context's key, and process-wide
`attributes` fill in for missing ones) and uses the deepest of those paths
that has a chamber:

| Chambers that exist | Context gets |
|---|---|
| `/prod/tenants/t-42/loc-7/` | `/prod/tenants/t-42/loc-7/` |
| `/prod/tenants/t-42/` only | `/prod/tenants/t-42/`: the location has none of its own |
| neither | `/prod/tenants/`: the tenant has none either |

Resolution also stops at a placeholder whose value is missing or empty,
contains `/` or `..`, or is `.`. Because a mistyped attribute falls back
silently as well, use `chamberPath(context)` to check which chamber a context
got.

A placeholder is a whole path segment, `{name}`, where the name uses letters,
digits, `_`, `-`, and `.`; other segments are literal. The constructor throws
a `TypeError` for an invalid template, or for one given together with `path`.
`subscribe` listeners run when any chamber in the subtree changes.

## Read flags in a browser: `BrowserClient`

A `BrowserClient` asks the server to evaluate every flag for one context and
holds the results. Your targeting rules (which tenants, which percentages)
never reach the browser.

```ts
import { BrowserClient } from '@steviebps/realm-sdk';

const flags = new BrowserClient({
  address: 'https://realm.example.com',
  token: 'realm_…', // an evaluate key: safe in front-end code
  path: '/prod/web/',
  context: { key: user.id, attributes: { tenantId: user.tenantId } },
});

await flags.start(); // resolves once the first evaluation has arrived

flags.bool('new-checkout', false);
flags.string('banner', '');

const unsubscribe = flags.subscribe(() => render()); // runs when any value changes
```

When the user changes, such as on sign-in, re-evaluate for the new context:

```ts
await flags.identify({ key: otherUser.id, attributes: { tenantId: otherUser.tenantId } });
```

Until the new context's values arrive, getters return values stored for that
context (see [Start offline](#start-offline)) or their fallbacks, never the
previous user's values.

> [!IMPORTANT]
> Use only an `evaluate` key in a browser. The server refuses `read` and
> `write` keys on browser requests (`start` rejects with a 403), so shipping
> one by mistake fails in development instead of exposing your rules.

### Start offline

By default a `BrowserClient` starts empty, so an app that loads without a
network (a PWA reloaded offline, say) serves every flag's fallback until the
server answers. Two options give it values to start from:

```ts
import { BrowserClient, localStorageAdapter } from '@steviebps/realm-sdk';

const flags = new BrowserClient({
  address: 'https://realm.example.com',
  token: 'realm_…',
  path: '/prod/web/',
  context: { key: device.id, attributes: { location: device.location } },
  storage: localStorageAdapter(), // remember each context's latest values
  bootstrap: { 'new-checkout': { type: 'boolean', value: false } }, // values for `context`
});

flags.source(); // 'cache' if this context was evaluated here before, else 'bootstrap'
void flags.start().catch(() => {}); // offline: keeps serving those values and retries
```

**`storage`** saves the latest evaluated values (never the rules) for each
context, and reads them back for that context only:

- Entries are keyed by server, chamber, and a hash of the context, so one user
  on a shared device never sees values stored for another, and attributes
  stay out of the keys.
- Only the ten most recently used contexts are kept for each chamber, so
  contexts with volatile attributes can't fill the storage your app shares.
- `localStorageAdapter()` uses `localStorage` (or another Web Storage area
  you pass, such as `sessionStorage`). Where there is none, such as in server
  rendering, it stores nothing.
- For another store, such as IndexedDB, implement `StorageAdapter`:
  `getItem`, `setItem`, and optionally `removeItem`, each synchronous or
  returning a promise. Values from an asynchronous store apply when they
  arrive, unless the server answered first or the context changed.
- Storage failures go to `onError`; the client carries on without the cache.

**`bootstrap`** is a set of evaluated values for the `context` option, in the
shape `allFlags` returns, for example rendered by a `ServerClient` or shipped
with the app. It is never stored, and never served after `identify` switches
to another context. When storage holds values for the context, those win.

`source()` tells you where the current values came from:

| `source()` | Values are | `isReady()` |
|---|---|---|
| `undefined` | none yet: getters return their fallbacks | `false` |
| `'bootstrap'` | from the `bootstrap` option | `false` |
| `'cache'` | from `storage`: an earlier evaluation of this context | `false` |
| `'network'` | the server's evaluation for the current context | `true` |

`subscribe` listeners run when the source changes, even if no value does.

## React

`@steviebps/realm-sdk/react` connects a `BrowserClient` to React 18 or later.
Wrap your app in a `RealmProvider`, then read flags with hooks. A component
renders again when a flag it reads changes on the server.

```tsx
import { BrowserClient } from '@steviebps/realm-sdk';
import { RealmProvider, useBoolFlag, useRealmStatus } from '@steviebps/realm-sdk/react';

const flags = new BrowserClient({
  address: 'https://realm.example.com',
  token: 'realm_…', // an evaluate key
  path: '/prod/web/',
});

export function App({ user }: { user: User }) {
  return (
    <RealmProvider client={flags} context={{ key: user.id, attributes: { tenantId: user.tenantId } }}>
      <Checkout />
    </RealmProvider>
  );
}

function Checkout() {
  const newCheckout = useBoolFlag('new-checkout', false);
  const { ready } = useRealmStatus();
  if (!ready) return <Spinner />; // optional: hooks return their fallbacks until then
  return newCheckout ? <NewCheckout /> : <LegacyCheckout />;
}
```

The provider starts the client when it mounts and stops it when it unmounts.
When `context` changes by value, such as when a user signs in, every flag is
evaluated again for the new context; an inline object with the same value
does not trigger that.

| Hook | Returns |
|---|---|
| `useBoolFlag(flag, fallback)` | a boolean flag's value |
| `useStringFlag(flag, fallback)` | a string flag's value |
| `useNumberFlag(flag, fallback)` | a number flag's value |
| `useJsonFlag<T>(flag, fallback)` | a custom (JSON) flag's value: the component's own copy, the same object until the value changes |
| `useAllFlags()` | every flag's value (read only) |
| `useRealmStatus()` | `{ ready, source, error }`: whether the evaluation for the current context has arrived, where the values came from, and why starting failed, if it did |
| `useRealmClient()` | the provider's `BrowserClient` |

Until the provider's context has values, hooks return their fallbacks, or
the `bootstrap` or stored values for that context if you set those options;
`ready` stays `false` until the server answers. Hooks throw outside a
`RealmProvider`. During server rendering (`renderToString` and friends), flag
hooks return their fallbacks.

## How clients stay current

**Starting.** `start()` resolves once the first load succeeds. If it fails,
`start()` rejects, but the client keeps retrying in the background and
serves values once the server is reachable. You don't need to restart it.
Calling `start()` again waits for a load in flight, then resolves if values
have arrived and otherwise rejects with the latest failure. `isReady()` tells
you whether the client has values from the server.

**Updates.** By default each client keeps a server-sent event stream open and
applies changes as they are made. With `streaming: false` it polls instead,
every 15 minutes (`ServerClient`) or 5 minutes (`BrowserClient`). If the
stream can't deliver events, because a proxy buffers or cuts it, say, the
client also polls until the stream recovers. A stream that goes silent for 75
seconds (the server sends a heartbeat every 30) is reconnected.

**Failures.** When a refresh fails, the client keeps its last known values,
calls `onError` (by default `console.warn`, once per distinct failure), and
retries:

- with exponential backoff from 1 to 30 seconds, with jitter, so clients that
  fail together don't retry together;
- after a 401 or 403 (a refused key) or a 404 (no such chamber), waiting up to
  5 minutes, since retrying sooner wouldn't help;
- honoring `Retry-After` on a 429 or 503, up to 5 minutes;
- in a browser, within a second of the `online` event.

A response that isn't one realm sends, such as a gateway's error page, counts
as a failed refresh too.

**Reading.** Getters never throw. A missing flag, a flag of another type, or a
flag of a type this SDK version doesn't know returns your fallback. Context
keys and attribute values are strings; from JavaScript, numbers and booleans
are read as strings and other values are ignored.

**Revisions.** `revision()` returns the server's storage revision of the
chamber a client uses: the highest among the chamber and the parents it
inherits from, so a write to any of them changes it. Use it to tag logs and
spans with the version of the flags that served a request. It is `undefined`
until the server's data arrives, and when the server's storage doesn't track
revisions (only `postgres`, `etcd`, and `boltdb` do). A revision identifies a
version; it doesn't order versions, since deleting a parent chamber can lower
it.

## API reference

### Options

Both clients take these options:

| Option | Default | Description |
|---|---|---|
| `address` | required | The realm server, such as `https://realm.example.com`. |
| `token` | none | An API key, sent as a bearer token. |
| `path` | `/` | The chamber to read. Flags inherited from parent chambers are included. |
| `streaming` | `true` | Receive changes over a server-sent event stream; `false` polls instead. |
| `pollingInterval` | 15 min (server), 5 min (browser) | Milliseconds between polls when not streaming, or while the stream keeps failing. Each wait varies by up to a tenth either way. A value that isn't positive means the default. |
| `requestTimeout` | 10 s | Milliseconds to wait for a response (or a stream's headers) before retrying; `0` waits forever. |
| `onError` | `console.warn` | Called when a refresh fails. A failure that repeats the previous one is reported once, until a refresh succeeds. An `onError` that throws is logged with `console.error`. |
| `fetch` | global `fetch` | A custom `fetch` implementation. |

`ServerClient` only:

| Option | Default | Description |
|---|---|---|
| `attributes` | none | Process-wide attributes, such as `environment`, used when a context doesn't set them. |
| `version` | none | Your application's version, as the `version` attribute. |
| `pathTemplate` | none | Instead of `path`: picks a chamber below a path for each context. See [`pathTemplate`](#serve-many-tenants-from-one-client-pathtemplate). |

`BrowserClient` only:

| Option | Default | Description |
|---|---|---|
| `context` | `{}` | Who flags are evaluated for; change it with `identify`. |
| `storage` | none | Where to keep each context's latest values. See [Start offline](#start-offline). |
| `bootstrap` | none | Evaluated values for `context` to serve until the server answers. |

### `ServerClient`

| Method | Description |
|---|---|
| `start()` | Loads the chamber and starts keeping it current. |
| `stop()` | Stops refreshing and closes the stream. |
| `isReady()` | Whether the client has data from the server. |
| `bool(flag, context, fallback)` | A boolean flag's value for `context`. |
| `string(flag, context, fallback)` | A string flag's value. |
| `number(flag, context, fallback)` | A number flag's value. |
| `json<T>(flag, context, fallback)` | A custom (JSON) flag's value, as a copy. |
| `allFlags(context)` | Every flag's value for `context`. |
| `subscribe(listener)` | Calls `listener` when the data changes; returns a function that unsubscribes. |
| `revision(context?)` | The server's revision of the chamber (the context's chamber, with `pathTemplate`). |
| `chamberPath(context?)` | The chamber a context is evaluated against. |

### `BrowserClient`

| Method | Description |
|---|---|
| `start()` | Evaluates for the current context and starts keeping values current. |
| `stop()` | Stops refreshing and closes the stream. |
| `identify(context)` | Switches to a new context and evaluates for it. |
| `isReady()` | Whether the values are the server's evaluation for the current context. |
| `source()` | Where the current values came from: `'bootstrap'`, `'cache'`, `'network'`, or `undefined`. |
| `bool(flag, fallback)` | A boolean flag's value. |
| `string(flag, fallback)` | A string flag's value. |
| `number(flag, fallback)` | A number flag's value. |
| `json<T>(flag, fallback)` | A custom (JSON) flag's value, as a copy. |
| `allFlags()` | Every flag's value. The object is shared: treat it as read only. |
| `subscribe(listener)` | Calls `listener` when a value or the source changes; returns a function that unsubscribes. |
| `revision()` | The server's revision of the chamber the values came from. |

## Learn more

- [realm README](https://github.com/steviebps/realm#readme): run a server,
  manage flags, and set up authentication and API keys.
- [Targeting and rollouts](https://github.com/steviebps/realm/blob/master/docs/design/targeting.md):
  operators, rollouts, and schedules.
- [Server-side evaluation](https://github.com/steviebps/realm/blob/master/docs/design/evaluation.md):
  the endpoint `BrowserClient` uses, which you can also call from other
  languages.
- [Subtrees and path templates](https://github.com/steviebps/realm/blob/master/docs/design/subtree.md).
- [Issues](https://github.com/steviebps/realm/issues): report a bug or ask a
  question.

## Contributing

The SDK lives in
[`sdk/typescript`](https://github.com/steviebps/realm/tree/master/sdk/typescript)
of the realm repository.

```bash
npm ci
npm run typecheck
npm test
npm run build
```

Local evaluation is a port of the Go SDK's. The cases in
[`sdk/conformance/evaluation.json`](https://github.com/steviebps/realm/blob/master/sdk/conformance/evaluation.json)
run against both implementations; add one whenever evaluation changes.
[`sdk/conformance/subtree.json`](https://github.com/steviebps/realm/blob/master/sdk/conformance/subtree.json)
does the same for path templates. Releases are described in
[RELEASING.md](https://github.com/steviebps/realm/blob/master/docs/RELEASING.md).

## License

[Apache-2.0](https://github.com/steviebps/realm/blob/master/sdk/typescript/LICENSE)
