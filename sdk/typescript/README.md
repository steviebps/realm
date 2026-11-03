# realm TypeScript SDK

Read realm feature flags from TypeScript or JavaScript. One package with no
runtime dependencies, for any runtime with `fetch` and streaming responses:
Node 18+, Bun, Deno, browsers, and edge runtimes.

| Client | Use in | Key | How it works |
|---|---|---|---|
| `ServerClient` | backend services | `read` | downloads the chamber and evaluates locally, like the Go SDK |
| `BrowserClient` | browsers, untrusted code | `evaluate` | the server evaluates for one context; rules never reach the client |

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
await realm.start(); // rejects if the first load fails

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
await flags.start();

flags.bool('new-checkout', false);
const unsubscribe = flags.subscribe(() => render()); // called when any value changes
await flags.identify({ key: otherUser.id }); // re-evaluates for a new context
```

In React, `subscribe` and `bool` fit `useSyncExternalStore` directly:

```ts
const enabled = useSyncExternalStore(
  (cb) => flags.subscribe(cb),
  () => flags.bool('new-checkout', false),
);
```

## Options

| Option | Default | |
|---|---|---|
| `address` | — | the realm server |
| `token` | — | an API key, sent as a bearer token |
| `path` | `/` | the chamber to read; flags inherited from parent chambers are included |
| `streaming` | `true` | keep values current over a server-sent event stream; `false` polls instead |
| `pollingInterval` | 15 min (server), 5 min (browser) | milliseconds between polls when not streaming |
| `onError` | `console.warn` | called when a refresh fails; the last known values stay in use |
| `fetch` | global `fetch` | a custom fetch implementation |

Streams reconnect with exponential backoff from 1 to 30 seconds. A stream that
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
