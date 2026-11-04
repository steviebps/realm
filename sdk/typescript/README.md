# realm TypeScript SDK

Read realm feature flags from TypeScript or JavaScript. One package with no
runtime dependencies, for any runtime with `fetch` and streaming responses:
Node 18+, Bun, Deno, browsers, and edge runtimes.

| Client | Use in | Key | How it works |
|---|---|---|---|
| `ServerClient` | backend services | `read` | downloads the chamber and evaluates locally, like the Go SDK |
| `BrowserClient` | browsers, untrusted code | `evaluate` | the server evaluates for one context; rules never reach the client |

`realm-sdk/react` adds a provider and hooks for a `BrowserClient`; React is an
optional peer dependency, needed only if you import it.

The package is not published to npm yet. Depend on it from this repository,
for example with `"realm-sdk": "file:../realm/sdk/typescript"`; installing
runs its build.

## ServerClient

```ts
import { ServerClient } from 'realm-sdk';

const realm = new ServerClient({
  address: 'https://realm.example.com',
  token: process.env.REALM_TOKEN, // a read key
  path: '/prod/checkout/',
  attributes: { environment: 'prod' }, // used when a context doesn't set them
  version: process.env.APP_VERSION, // the "version" attribute
});
await realm.start(); // rejects if the first load fails, then keeps retrying

const context = { key: user.id, attributes: { tenantId: user.tenantId } };
if (realm.bool('new-checkout', context, false)) {
  // …
}
realm.string('banner', context, '');
realm.number('max-items', context, 50);
realm.json<{ limit: number }>('quota', context, { limit: 10 });
realm.allFlags(context); // every flag's value, for example to bootstrap a page

realm.stop();
```

## BrowserClient

```ts
import { BrowserClient } from 'realm-sdk';

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

## React

`realm-sdk/react` reads a `BrowserClient` from React 18 or later. Wrap the app
in a `RealmProvider`, then read flags with hooks; a component renders again
when a flag it reads changes.

```tsx
import { BrowserClient } from 'realm-sdk';
import { RealmProvider, useBoolFlag, useRealmStatus } from 'realm-sdk/react';

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
| `useJsonFlag<T>(flag, fallback)` | a custom (JSON) flag's value |
| `useAllFlags()` | every flag's value |
| `useRealmStatus()` | `{ ready, error }`: whether the evaluation for the current context arrived, or why starting failed |
| `useRealmClient()` | the provider's `BrowserClient` |

Hooks throw outside a `RealmProvider`. On the server (`renderToString` and
friends), flag hooks return their fallbacks.

## Options

| Option | Default | |
|---|---|---|
| `address` | — | the realm server |
| `token` | — | an API key, sent as a bearer token |
| `path` | `/` | the chamber to read; flags inherited from parent chambers are included |
| `streaming` | `true` | keep values current over a server-sent event stream; `false` polls instead |
| `pollingInterval` | 15 min (server), 5 min (browser) | milliseconds between polls when not streaming |
| `requestTimeout` | 10 s | milliseconds to wait for a response (or a stream's headers) before retrying; `0` waits forever |
| `onError` | `console.warn` | called when a refresh fails; the last known values stay in use |
| `fetch` | global `fetch` | a custom fetch implementation |

Streams reconnect with exponential backoff from 1 to 30 seconds; in a
browser, the `online` event cuts a backoff short. When the first load fails,
`start` rejects but the client keeps retrying in the background, so values
arrive once the server is reachable. After `identify` with a new
context, a `BrowserClient` serves fallbacks (never the previous context's
values, which may be another user's on a shared device) until the new
context's evaluation arrives, retrying if it fails. A stream that
goes silent for 75 seconds (the server sends a heartbeat every 30) is
reconnected. Getters never throw: a missing flag, or a flag of another type,
returns the fallback.

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
