# realm TypeScript SDK

Read realm feature flags from TypeScript or JavaScript. One package with no
runtime dependencies, for any runtime with `fetch` and streaming responses:
Node 18+, Bun, Deno, browsers, and edge runtimes.

| Client | Use in | Key | How it works |
|---|---|---|---|
| `ServerClient` | backend services | `read` | downloads the chamber and evaluates locally, like the Go SDK |
| `BrowserClient` | browsers, untrusted code | `evaluate` | the server evaluates for one context; rules never reach the client |

`@steviebps/realm-sdk/react` adds a provider and hooks for a `BrowserClient`; React is an
optional peer dependency, needed only if you import it.

```bash
npm install @steviebps/realm-sdk
```

It is published from this repository with every realm release, at the same
version as the server (see [Releasing](../../docs/RELEASING.md)). The package is
ESM only: use `import`, not `require`.

## ServerClient

```ts
import { ServerClient } from '@steviebps/realm-sdk';

const realm = new ServerClient({
  address: 'https://realm.example.com',
  token: process.env.REALM_TOKEN, // a read key
  path: '/prod/checkout/',
  attributes: { environment: 'prod' }, // used when a context doesn't set them
  version: process.env.APP_VERSION, // the "version" attribute
});
await realm.start(); // rejects if the first load fails, then keeps retrying
realm.isReady(); // whether the client has a chamber from the server

const context = { key: user.id, attributes: { tenantId: user.tenantId } };
if (realm.bool('new-checkout', context, false)) {
  // …
}
realm.string('banner', context, '');
realm.number('max-items', context, 50);
realm.json<{ limit: number }>('quota', context, { limit: 10 }); // a copy
realm.allFlags(context); // every flag's value, for example to bootstrap a page

realm.stop();
```

## BrowserClient

```ts
import { BrowserClient } from '@steviebps/realm-sdk';

const flags = new BrowserClient({
  address: 'https://realm.example.com',
  token: 'realm_…', // an evaluate key: safe to ship in front-end code
  path: '/prod/checkout/',
  context: { key: user.id, attributes: { tenantId: user.tenantId } },
});
await flags.start(); // rejects if the first evaluation fails, then keeps retrying

flags.bool('new-checkout', false);
flags.isReady(); // whether the values are the evaluation for the current context
const unsubscribe = flags.subscribe(() => render()); // called when any value changes
await flags.identify({ key: otherUser.id }); // re-evaluates for a new context
```

Use only an `evaluate` key in a browser. The server refuses `read` and
`write` keys on browser requests (`start` rejects with a 403), so shipping one
by mistake fails in development instead of exposing your rules.

### Starting offline

By default a `BrowserClient` starts empty, so an app that loads without a
network (a PWA reloaded offline, say) gets every flag's fallback until the
server answers. Two options give it values to start from:

```ts
import { BrowserClient, localStorageAdapter } from '@steviebps/realm-sdk';

const flags = new BrowserClient({
  address: 'https://realm.example.com',
  token: 'realm_…',
  path: '/prod/web/',
  context: { key: device.id, attributes: { location: device.location } },
  storage: localStorageAdapter(), // keep each context's latest values
  bootstrap: { 'new-checkout': { type: 'boolean', value: false } }, // values for `context`
});
flags.source(); // 'cache' if this context was evaluated here before, else 'bootstrap'
void flags.start().catch(() => {}); // offline: keeps the stored values and retries
```

- **`storage`** keeps the latest values the server evaluated (never the
  rules) for each context, and writes them whenever they change. At
  construction and on `identify`, the client reads back the values stored for
  that context only. Entries are keyed by server, chamber, and a hash of the
  context (which keeps attributes out of the key), so one user on a shared
  device never sees values stored for another. Only the ten most recently
  used contexts' values are kept for each chamber, so contexts with volatile
  attributes cannot fill the storage your app shares; a short list of them
  is stored next to the values. `localStorageAdapter()` uses
  `localStorage` (or a Web Storage area you pass) and stores nothing where
  there is none, such as in server rendering. To use another store, implement
  `StorageAdapter`: `getItem`, `setItem`, and optionally `removeItem`,
  synchronous or returning promises (without `removeItem`, the values of
  contexts no longer kept are overwritten with an empty string). Values from
  an asynchronous store apply when they arrive, unless the server answered
  first or the context changed. Storage failures go to `onError`; the client
  carries on without the cache.
- **`bootstrap`** is a set of evaluated values (as `allFlags` returns them,
  from a `ServerClient` render or shipped with the app) for the `context`
  option. It is never stored and never served after `identify` switches to
  another context. When storage holds values for the context, those win.

`source()` reports where the current values came from: `'bootstrap'`,
`'cache'`, `'network'`, or `undefined` when there are none and getters return
their fallbacks. `isReady()` is true only for `'network'`: the server's
evaluation for the current context. Listeners are called when the source
changes, even if no value does.

## React

`@steviebps/realm-sdk/react` reads a `BrowserClient` from React 18 or later. Wrap the app
in a `RealmProvider`, then read flags with hooks; a component renders again
when a flag it reads changes.

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
When `context` changes by value, every flag is evaluated again for the new
context; an inline object that keeps the same value does not.

| Hook | Returns |
|---|---|
| `useBoolFlag(flag, fallback)` | a boolean flag's value |
| `useStringFlag(flag, fallback)` | a string flag's value |
| `useNumberFlag(flag, fallback)` | a number flag's value |
| `useJsonFlag<T>(flag, fallback)` | a custom (JSON) flag's value: the component's own copy, the same object until the value changes |
| `useAllFlags()` | every flag's value (read only) |
| `useRealmStatus()` | `{ ready, source, error }`: whether the evaluation for the current context arrived, where the values came from (`'bootstrap'`, `'cache'`, `'network'`, or `undefined`), or why starting failed |
| `useRealmClient()` | the provider's `BrowserClient` |

Until the provider's `context` reaches the client, and whenever the client
holds values for another context, hooks return their fallbacks. With the
`bootstrap` or `storage` options, they return those values for the
provider's context before the server answers; `ready` stays false until it
does.

Hooks throw outside a `RealmProvider`. On the server (`renderToString` and
friends), flag hooks return their fallbacks.

## Options

| Option | Default | |
|---|---|---|
| `address` | — | the realm server |
| `token` | — | an API key, sent as a bearer token |
| `path` | `/` | the chamber to read; flags inherited from parent chambers are included |
| `streaming` | `true` | keep values current over a server-sent event stream; `false` polls instead |
| `pollingInterval` | 15 min (server), 5 min (browser) | milliseconds between polls when not streaming, or while the stream keeps failing; each wait varies by up to a tenth either way, and a value that is not positive means the default |
| `requestTimeout` | 10 s | milliseconds to wait for a response (or a stream's headers) before retrying; `0` waits forever |
| `onError` | `console.warn` | called when a refresh fails; the last known values stay in use. A failure that repeats the previous one is reported once, until a refresh succeeds (which the default notes with `console.info`) |
| `fetch` | global `fetch` | a custom fetch implementation |
| `context` | `{}` | browser only: who flags are evaluated for |
| `storage` | none | browser only: where to keep each context's latest values, for starting offline; see [Starting offline](#starting-offline) |
| `bootstrap` | none | browser only: evaluated values for `context` to serve until the server answers |

Failed refreshes are retried with exponential backoff from 1 to 30 seconds,
with jitter as in the Go SDK (each wait is a random time between half the
step and the step), so clients that fail together do not retry together.
After a 401 or 403 (a refused key) or a 404 (no such chamber), which retrying
soon would not fix, the backoff grows to 5 minutes; a 429 or 503 response's
`Retry-After` is honored, up to 5 minutes. In a browser, the `online` event
cuts a backoff short, reconnecting within a second. A stream that goes three
attempts in a row, or a minute, without delivering an event (a proxy that
buffers or cuts event streams, say, or a server at its cap on open streams)
is helped by polling every `pollingInterval` until it delivers again. A
stream that goes silent for 75 seconds (the server sends a heartbeat every
30) is reconnected.

A response that is not what the server sends (a gateway's or maintenance
page's `{}`, for example) and a stream event that cannot be used count as
failed refreshes: the last known values stay in use, and the stream stays
open. An `onError` that throws is logged with `console.error`; refreshing
carries on.

When the first load fails, `start` rejects but the client keeps retrying in
the background, so values arrive once the server is reachable. Calling
`start` again reflects the current state: it waits for the first load while
that is in flight, then resolves if values have arrived since, and otherwise
rejects with the latest failure. After `identify`
with a new context, a `BrowserClient` serves values stored for that context,
if any, or else fallbacks (never the previous context's values, which may be
another user's on a shared device) until the new context's evaluation
arrives, retrying if it fails.

Getters never throw: a missing flag, a flag of another type, or one of a
type or shape this SDK does not know returns the fallback. `json` returns a
copy of the value, so changing it changes nothing else; a `BrowserClient`'s
`allFlags` (and `useAllFlags`) returns the client's own object, so treat it
as read only. Context keys and attribute values are strings: from
JavaScript, numbers and booleans are read as strings and other values are
ignored, as if missing, by both clients.

## Development

```bash
npm ci
npm run typecheck
npm test
npm run build
```

Evaluation is a port of the Go SDK's. `../conformance/evaluation.json` holds
cases both implementations run; add one there whenever evaluation changes. See
[docs/design/evaluation.md](../../docs/design/evaluation.md).
