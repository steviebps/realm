# realm

[![release](https://github.com/steviebps/realm/actions/workflows/go.yml/badge.svg)](https://github.com/steviebps/realm/actions/workflows/go.yml)

realm is an open-source feature-flag and configuration platform written in Go.
One binary gives you three things:

- **A server** that stores flags and serves them over HTTP, with a web UI at `/ui/`.
- **A CLI** (`realm server`, `realm client`, `realm keys`).
- **A Go SDK** that your application embeds to read flags from a local snapshot.

A TypeScript SDK in [`sdk/typescript`](sdk/typescript) reads flags in Node
services and in browsers, with hooks for React.

## Features

- **Typed flags:** `boolean`, `number`, `string`, and `custom` (any JSON).
- **Hierarchical chambers:** a child chamber inherits the flags of its parents.
- **Targeting rules:** match on any attribute (tenant, location, environment,
  semver `version`, build SHA, …).
- **Progressive rollout:** deterministic, sticky percentages, bucketed on any attribute.
- **Scheduled rules:** limit a rule to a window of time, such as three hours on a Wednesday.
- **Real-time updates:** the SDK polls, or receives changes over server-sent events.
- **Safe editing:** ETag optimistic concurrency and per-chamber change history.
- **Storage:** file, BoltDB, in-memory, Google Cloud Storage, Amazon S3, and
  PostgreSQL (which supports running several replicas).
- **Optional auth:** OIDC or GitHub sign-in with roles, and path-scoped API keys.
- **Browsers and other languages:** the server evaluates flags for a context and
  returns only the values, never the rules.

## Contents

- [Quick start](#quick-start)
- [Install](#install)
- [Concepts](#concepts)
- [Run the server](#run-the-server)
- [Manage flags](#manage-flags)
- [Use the Go SDK](#use-the-go-sdk)
- [Use the TypeScript SDK](#use-the-typescript-sdk)
- [Authentication](#authentication)
- [Reference](#reference)
- [Documentation](#documentation)

## Quick start

This procedure starts a server with in-memory storage, adds a flag, and reads it.

1. Start the server:

   ```bash
   docker run --rm -p 8080:8080 ghcr.io/steviebps/realm server --dev
   ```

2. Create a chamber named `root` with one flag:

   ```bash
   curl -X POST http://localhost:8080/v1/chambers/root \
     -H 'Content-Type: application/json' \
     -d '{"rules":{"new-checkout":{"type":"boolean","value":false}}}'
   ```

3. Read the chamber:

   ```bash
   curl http://localhost:8080/v1/chambers/root
   ```

4. Open the web UI at <http://localhost:8080/ui/>.

> [!NOTE]
> Dev mode keeps all data in memory. When the server stops, the data is lost.

## Install

Use one of these methods.

| Method | Command | Web UI |
|---|---|---|
| Docker | `docker pull ghcr.io/steviebps/realm` | Included |
| Release binary | Download from [GitHub Releases](https://github.com/steviebps/realm/releases) | Included |
| `go install` | `go install github.com/steviebps/realm@latest` | Not included |
| From source | `make build-ui` (needs Node 20+) | Included |

Docker images are multi-arch (`linux/amd64`, `linux/arm64`). The `latest` tag
and the semver tags are releases. The `edge` tag follows `master`.

To use the Go SDK in your application, add the module:

```bash
go get github.com/steviebps/realm
```

## Concepts

| Term | Meaning |
|---|---|
| **Chamber** | A namespace of flags at a path, for example `/prod/checkout/`. A chamber inherits the flags of its parent chambers. |
| **Flag** | A typed default `value` and an ordered list of `targeting` rules. |
| **Targeting rule** | A list of clauses, a `value`, and an optional `rollout`. All clauses must match. |
| **Clause** | `{ "attribute", "op", "values" }`. A clause on an attribute that is not set never matches. |
| **Rollout** | `{ "percentage", "bucketBy", "seed" }`. Limits a rule to a sticky percentage of the matching contexts. |
| **Evaluation context** | A `key` and a set of attributes that describe the subject of a request. |

### How realm evaluates a flag

1. realm examines the targeting rules in order.
2. If all clauses of a rule match and the context is in the rollout, realm
   returns the value of that rule.
3. If no rule applies, realm returns the default `value` of the flag.

A context that is not in the rollout goes to the next rule.

### Example flag

```json
{
  "rules": {
    "new-checkout": {
      "type": "boolean",
      "value": false,
      "targeting": [
        {
          "id": "pilot-locations",
          "clauses": [
            { "attribute": "environment", "op": "in", "values": ["prod"] },
            { "attribute": "locationId", "op": "in", "values": ["loc-123", "loc-456"] }
          ],
          "value": true
        },
        {
          "id": "gradual",
          "clauses": [{ "attribute": "version", "op": "semverGte", "values": ["v2.0.0"] }],
          "value": true,
          "rollout": { "percentage": 20, "bucketBy": "locationId" }
        }
      ]
    }
  }
}
```

In this example:

- Production contexts at `loc-123` and `loc-456` get `true`.
- 20% of the locations that run `v2.0.0` or later get `true`.
- All other contexts get `false`.

### Operators

| Operator | Matches when the attribute… |
|---|---|
| `in` | is equal to one of the values |
| `notIn` | is set and is not equal to any of the values |
| `startsWith` | starts with one of the values (for example, a build SHA prefix) |
| `semverGte` | is a semver version greater than or equal to the value (one value only) |
| `semverLte` | is a semver version less than or equal to the value (one value only) |

`bucketBy` is `key` by default. To roll out to a full group at once, bucket on a
larger attribute, for example `locationId`. Refer to
[docs/design/targeting.md](docs/design/targeting.md) for the full model.

## Run the server

### Dev mode

Dev mode uses in-memory storage on port 8080. It needs no config file.

```bash
realm server --dev
```

### With a config file

1. Write a config file. Start from [configs/realm.json](configs/realm.json):

   ```json
   {
     "client": { "address": "http://localhost:8080" },
     "server": {
       "storage": "cacheable",
       "port": "8080",
       "inheritable": true,
       "options": { "cache": "bigcache", "source": "boltdb", "path": "./.realm" }
     }
   }
   ```

2. Start the server:

   ```bash
   realm server --config ./configs/realm.json
   ```

Server settings:

| Key | Meaning |
|---|---|
| `storage` | The storage backend. Refer to [Storage backends](#storage-backends). |
| `options` | The settings of the storage backend. |
| `port` | The HTTP port. |
| `inheritable` | If `true`, child chambers inherit the flags of their parents. |
| `certFile`, `keyFile` | A TLS certificate and key. If you set both, the server uses HTTPS. |
| `logLevel` | The minimum level logged: `debug`, `info` (default), `warn`, or `error`. `--debug` overrides it. |
| `shutdownDelay` | How long to keep serving after `SIGTERM` while `/readyz` reports 503, such as `"5s"`. Gives load balancers time to stop routing to the server. Default: none. |
| `auth` | Sign-in and API keys. Refer to [Authentication](#authentication). |

### Storage backends

| `storage` | Options | Notes |
|---|---|---|
| `file` | `path` | JSON files on disk, in the directory `path`. Writes replace files atomically, and a lock file serializes changes, so `realm keys` can run beside the server. |
| `boltdb` | `path` | An embedded key/value database, `realm.db` in the directory `path`. The directory must exist. Only one process can open the database: another fails after 1 second. |
| `bigcache` | `shards`, `life_window`, `clean_window` | In memory. Data is lost when the server stops. |
| `gcs` | `bucket` | Google Cloud Storage. Uses the standard Google credential chain. |
| `s3` | `bucket`, `region`, `prefix`, `endpoint`, `force_path_style` | Amazon S3 or an S3-compatible service. Uses the standard AWS credential chain. |
| `postgres` | `url`, `max_conns` | PostgreSQL. Supports several server replicas. Refer to [Run several replicas](#run-several-replicas). |
| `cacheable` | `cache`, `source`, plus the options of both | A write-through `cache` in front of a `source`. The source cannot be `postgres`. |

For production, we recommend `postgres`. For a single replica, `cacheable`
with `bigcache` in front of `s3` also works.

> [!IMPORTANT]
> With any backend other than `postgres`, run only one server replica. Other
> backends check concurrent changes and send real-time updates only within one
> server.

### Run several replicas

Use the `postgres` backend. The server creates its tables on startup.

```json
{
  "server": {
    "storage": "postgres",
    "options": { "url": "postgres://realm@db.internal:5432/realm?sslmode=verify-full" },
    "port": "8080",
    "inheritable": true
  }
}
```

- When `url` is empty, the standard `PG*` environment variables are used. Put
  the password in `PGPASSWORD` or a `.pgpass` file, not in the config.
- Every replica sees every change within milliseconds: writes are checked in
  the database (so `If-Match` works across replicas), and each replica learns
  of others' writes with `LISTEN`/`NOTIFY` and pushes them to its streams.
- Each replica holds one connection open for `LISTEN`. That connection cannot
  go through a pooler in transaction mode, such as PgBouncer's
  `pool_mode = transaction`. Use session mode or connect directly.

### With Docker

The image reads its config from `/etc/realm/realm.json`. Mount your config file at that path:

```bash
docker run -p 8080:8080 \
  -v "$PWD/configs/realm.json:/etc/realm/realm.json:ro" \
  ghcr.io/steviebps/realm
```

- The image runs as a non-root user.
- To keep data from the `file` or `boltdb` backends, set `path` to a
  directory in `/var/lib/realm`. That directory is a volume.
- The `s3` and `gcs` backends work with Kubernetes workload identity. They need
  no more configuration.
- Use `GET /healthz` for liveness probes and `GET /readyz` for readiness
  probes. `/readyz` reports 503 when storage does not answer within 2 seconds
  and while the server drains for shutdown.

### Run in production

- **Shutdown.** On `SIGTERM` the server fails `/readyz`, waits
  `shutdownDelay`, closes open streams (clients reconnect to another replica),
  and lets in-flight requests finish for up to 20 seconds. Keep
  `terminationGracePeriodSeconds` above `shutdownDelay` + 20s.
- **Logs** are one JSON object per line on standard error. On a terminal they
  are human-readable instead; set `REALM_LOG_FORMAT` to `json` or `console` to
  choose.
- **Telemetry** is exported over OTLP/HTTP and configured by the standard
  `OTEL_*` variables: `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`
  (default `realm`), `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER`.
  `OTEL_SDK_DISABLED=true` or `--notraces` turns it off. Every request except
  probes and streams gets a span, and the server records
  `realm.server.streams.active` and `realm.postgres.feed.reconnects`.
- **Streams.** Each server holds at most 10,000 streams; beyond that, new
  streams get 503 and clients retry with backoff. Streams close after about 20
  to 25 minutes and clients reconnect, so a revoked key or expired session
  loses its stream within that time.
- **Limits.** Request bodies are limited to 4 MiB (evaluation contexts to
  64 KiB), and request headers must arrive within 10 seconds.

## Manage flags

You can change flags in three ways:

- **Web UI** at `/ui/`. Edit targeting and rollouts, examine a diff before you
  save, and restore an earlier version from the history.
- **CLI** with `realm client`.
- **HTTP API** at `/v1/chambers/<path>`.

### CLI

`realm client` sends requests to a running server. Set the server address with
`--address`, or with `client.address` in the config file.

```bash
realm client list /                  # list the chambers below a path
realm client get /prod/checkout/     # show one chamber as JSON
realm client put /prod/checkout/     # make an empty chamber (fails if it exists)
realm client put /prod/checkout/ -f checkout.json  # create or replace from a file ("-" for stdin)
realm client delete /prod/checkout/  # delete a chamber
```

> [!NOTE]
> `realm client put -f` replaces the whole chamber. The file is validated
> first: an unknown field anywhere (a misspelled `schedule`, say) is an error,
> as it is for every write to the server.

### HTTP API

| Request | Result |
|---|---|
| `GET /v1/chambers/<path>` | Returns the chamber, with the flags that it inherits. |
| `GET /v1/chambers/<path>?inherit=false` | Returns the chamber as stored, with an `ETag`. |
| `GET /v1/chambers/<path>?list=true` | Lists the chambers below the path. |
| `GET /v1/chambers/<path>?watch=true` | Streams the chamber as server-sent events. |
| `POST /v1/chambers/<path>` | Creates or replaces the chamber. |
| `PATCH /v1/chambers/<path>` | Merges the body into the chamber. |
| `DELETE /v1/chambers/<path>` | Deletes the chamber. |
| `GET /v1/history/<path>` | Returns the change history of the chamber. |
| `POST /v1/evaluate/<path>` | Evaluates every flag for the context in the body. Returns only the values. |
| `POST /v1/evaluate/<path>?watch=true` | Streams the evaluated values as server-sent events. |
| `GET /healthz` | Returns the health of the server. |

To prevent lost updates, send `If-Match: <etag>` with a write. If the chamber
changed after you read it, the server returns `412 Precondition Failed`. To make
sure that you do not replace an existing chamber, send `If-None-Match: *`.

## Use the Go SDK

The SDK gets a chamber when it starts. It keeps a local copy of the chamber and
reads flags from that copy, so a flag read sends no network request.

### Read flags

```go
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/steviebps/realm/client"
	realmhttp "github.com/steviebps/realm/http"
	realm "github.com/steviebps/realm/pkg"
)

func main() {
	c, err := client.NewHttpClient(&client.HttpClientConfig{
		Address: "http://localhost:8080",
		Token:   os.Getenv("REALM_TOKEN"), // only if the server has auth enabled
	})
	if err != nil {
		log.Fatal(err)
	}

	rlm, err := realm.NewRealm(
		realm.WithHttpClient(c),
		realm.WithPath("root"),
		realm.WithVersion("v1.0.0"),
	)
	if err != nil {
		log.Fatal(err)
	}
	if err := rlm.Start(); err != nil {
		log.Fatal(err)
	}
	defer rlm.Stop()

	// Outside a request, make a context that holds the current snapshot.
	ctx := rlm.NewContext(context.Background())
	port, _ := rlm.Float64(ctx, "port", 3000)
	log.Println("port:", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// RealmHandler (below) puts the snapshot in each request context.
		msg, _ := rlm.String(r.Context(), "message", "DEFAULT")
		_, _ = w.Write([]byte(msg))
	})

	log.Fatal(http.ListenAndServe(":3000", realmhttp.RealmHandler(rlm, mux)))
}
```

The read methods are `Bool`, `String`, `Float64`, and `CustomValue` (decodes
JSON into a value that you supply). If a flag is missing or has a different
type, `Bool`, `String`, and `Float64` return your default and an error. For a full example
with graceful shutdown, refer to [examples/go/main.go](examples/go/main.go).

### Set attributes for targeting

Attributes come from two sources. If the two sources set the same attribute,
the request value replaces the process value.

- **Process attributes** describe the deployment. Set them one time on the SDK.
- **Request attributes** describe the subject of a request. Attach them to the
  request context.

```go
rlm, _ := realm.NewRealm(
	realm.WithHttpClient(c),
	realm.WithPath("root"),
	realm.WithVersion("v2.3.0"), // sets the "version" attribute
	realm.WithAttributes(map[string]string{
		"environment": "prod",
		"service":     "checkout",
		"buildSha":    sha,
	}),
)

ctx := rlm.NewContextWithEvaluation(r.Context(), realm.EvaluationContext{
	Key:        userID, // the "key" attribute
	Attributes: map[string]string{"tenantId": tenantID, "locationId": locationID},
})
enabled, _ := rlm.Bool(ctx, "new-checkout", false)
```

### Get updates

By default, the SDK polls the server every 15 minutes. To change the interval,
use `WithPollingInterval`.

To get changes in real time, use `WithStreaming(true)`. The server then sends
changes to the SDK over server-sent events. If the server does not support
streaming, the SDK polls.

```go
rlm, _ := realm.NewRealm(
	realm.WithHttpClient(c),
	realm.WithPath("root"),
	realm.WithStreaming(true),
)
```

To watch a chamber without the SDK, use curl:

```bash
curl -N 'http://localhost:8080/v1/chambers/root?watch=true'
```

## Use the TypeScript SDK

[`sdk/typescript`](sdk/typescript) has two clients. The package is not on npm
yet; add it as a `file:` dependency from this repository.

| Client | Use it in | API key | Where flags are evaluated |
|---|---|---|---|
| `ServerClient` | Node services | `read` | in your process, like the Go SDK |
| `BrowserClient` | browsers | `evaluate` | on the server, so the rules stay private |

```ts
import { BrowserClient } from 'realm-sdk';

const flags = new BrowserClient({
  address: 'https://realm.example.com',
  token: 'realm_…', // an evaluate key
  path: '/prod/web/',
  context: { key: userId, attributes: { tenantId } },
});
await flags.start();
flags.bool('new-checkout', false);
flags.subscribe(() => render()); // runs when a value changes
```

Both clients receive changes over server-sent events. For the options and the
`ServerClient` API, refer to [sdk/typescript/README.md](sdk/typescript/README.md).

Other languages can call the evaluation endpoint directly:

```bash
curl -X POST http://localhost:8080/v1/evaluate/prod/web/ \
  -H "Authorization: Bearer $REALM_TOKEN" \
  -d '{"key": "user-123", "attributes": {"tenantId": "acme"}}'
```

### Use the BrowserClient in React

`realm-sdk/react` has a provider and hooks for a `BrowserClient`.

1. Make the client and wrap the app in a `RealmProvider`. The provider starts
   the client, and evaluates again when `context` changes, for example when a
   user signs in:

   ```tsx
   // App.tsx
   import { BrowserClient } from 'realm-sdk';
   import { RealmProvider } from 'realm-sdk/react';

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
   ```

2. Read flags in your components:

   ```tsx
   // Checkout.tsx
   import { useBoolFlag } from 'realm-sdk/react';

   export function Checkout() {
     const newCheckout = useBoolFlag('new-checkout', false);
     return newCheckout ? <NewCheckout /> : <LegacyCheckout />;
   }
   ```

Until the first evaluation arrives, the hooks return their fallbacks, or
the `bootstrap` or stored values for the context if you set the client's
`bootstrap` or `storage` options (see
[Starting offline](sdk/typescript/README.md#starting-offline));
`useRealmStatus()` tells you when it has arrived and where the values came from. When a value changes on the server,
React renders the components that use that flag again. For every hook, refer
to [sdk/typescript/README.md](sdk/typescript/README.md#react).

## Authentication

> [!WARNING]
> If the server config has no `auth` block, all users that can connect to the
> server can read and change all flags.

Authentication has two parts:

- **Sign-in** for the web UI, with any OIDC provider or GitHub. Each user gets
  the role `viewer`, `editor`, or `admin`.
- **API keys** for applications and automation. An `evaluate` key is for
  browsers: it can only evaluate flags, so you can put it in front-end code. A
  `read` key is for server SDKs. A `write` key is for automation. Keep `read`
  and `write` keys secret: the server refuses them on requests from browsers.
  A key gives access to its path and to all chambers below it.

For the `auth` config block, refer to [docs/design/auth.md](docs/design/auth.md).

### Make the first API key

Administrators manage keys in the web UI (**API keys** in the header menu). To
make a key without the UI, use `realm keys`. This command writes directly to the
storage in the server config. It needs no running server and no sign-in.

1. If the server uses `boltdb`, stop the server. A running server locks the database.
2. Make a key:

   ```bash
   realm keys create --config realm.json --name checkout --access read --path /prod/
   ```

3. Copy the token from the output. realm shows the token one time only and
   keeps only a hash of it.

Other key commands:

```bash
realm keys list   --config realm.json
realm keys revoke --config realm.json <id>
```

A running server accepts a new key immediately. It stops accepting a revoked
key in 30 seconds or less.

> [!NOTE]
> `realm keys` does not work with the `bigcache` backend, because its data is
> in the memory of the server.

### Use an API key

The SDK and the CLI send the key as a bearer token
(`Authorization: Bearer realm_…`).

- **SDK:** set `Token` in `client.HttpClientConfig`.
- **CLI:** realm uses the first value it finds: `--token`, then `$REALM_TOKEN`,
  then `client.token` in the config file.

```bash
export REALM_TOKEN=realm_…
realm client --address https://realm.example.com get /prod/checkout/
```

## Reference

### Global CLI flags

| Flag | Meaning |
|---|---|
| `-c`, `--config` | The realm config file. |
| `-a`, `--address` | The address of the realm server (`realm client`). |
| `--token` | The API key (`realm client`). |
| `-d`, `--debug` | Run in debug mode. |
| `--stdouttraces` | Write traces to stdout. |
| `--notraces` | Disable tracing. |

`realm server` also accepts `--dev` (in-memory storage, no config file) and
`--port`.

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): the data model, request flow, storage, and roadmap.
- [docs/design/targeting.md](docs/design/targeting.md): the targeting and rollout design.
- [docs/design/auth.md](docs/design/auth.md): the authentication design and the `auth` config.
- [docs/design/evaluation.md](docs/design/evaluation.md): server-side evaluation and the TypeScript SDK.
- [CONTRIBUTING.md](CONTRIBUTING.md): build, test, and send a pull request.

## License

realm is licensed under the Apache License 2.0. Refer to [LICENSE.txt](LICENSE.txt).
