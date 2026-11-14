# realm

[![release](https://github.com/steviebps/realm/actions/workflows/go.yml/badge.svg)](https://github.com/steviebps/realm/actions/workflows/go.yml)

realm is an open-source feature-flag and configuration platform written in Go.
One binary gives you three things:

- **A server** that stores flags and serves them over HTTP, with a web UI at `/ui/`.
- **A CLI** (`realm server`, `realm client`).
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
- **Storage:** file, BoltDB, in-memory, Google Cloud Storage, Amazon S3,
  PostgreSQL, and etcd (the last two support running several replicas).
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
| **Flag** | A typed default `value` and an ordered list of `targeting` rules. A flag with `"sealed": true` cannot be overridden by the chambers below its chamber. |
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

   Or skip `--config` by putting the file where realm finds it; see
   [Where realm finds its config](#where-realm-finds-its-config).

#### Where realm finds its config

Every command (`server`, `client`, `keys`) uses the first of:

1. `--config <file>`
2. `$REALM_CONFIG`
3. `realm/realm.json` in your user config directory: `$XDG_CONFIG_HOME` or
   `~/.config` on Linux, `~/Library/Application Support` on macOS, `%AppData%`
   on Windows
4. `/etc/realm/realm.json` (not on Windows)

A file named by `--config` or `$REALM_CONFIG` must exist; the default locations
are skipped if they do not. realm never reads a config file from the working
directory, so a `realm.json` in a cloned repository cannot redirect the CLI,
and your `$REALM_TOKEN`, to another server. `realm --help` prints the default
paths for your machine. `realm client` works without any config file; the
server needs one unless you use `--dev`.

Server settings:

| Key | Meaning |
|---|---|
| `storage` | The storage backend. Refer to [Storage backends](#storage-backends). |
| `options` | The settings of the storage backend. |
| `port` | The HTTP port. Default: `8080`. |
| `inheritable` | If `true`, child chambers inherit the flags of their parents, up to the root chamber `/`. |
| `certFile`, `keyFile` | A TLS certificate and key. If you set both, the server uses HTTPS (TLS 1.2 or later). It checks the files every minute and loads a renewed certificate without a restart, and sends `Strict-Transport-Security`. |
| `logLevel` | The minimum level logged: `debug`, `info` (default), `warn`, or `error`. `--debug` overrides it. |
| `shutdownDelay` | How long to keep serving after `SIGTERM` while `/readyz` reports 503, such as `"5s"`. Gives load balancers time to stop routing to the server. Default: none. |
| `maxStreams` | The most watch streams the server holds open at once. Evaluation streams may take at most three quarters of them, so server SDKs always find room. Default: `10000`. |
| `maxSubtreeChambers` | The most chambers below the root of a subtree read or watch (`?subtree=true`). The server refuses a larger subtree with 413. Default: `10000`. |
| `auth` | Sign-in and API keys. Refer to [Authentication](#authentication). |

### Storage backends

| `storage` | Options | Notes |
|---|---|---|
| `file` | `path` | JSON files on disk, in the directory `path`. Writes replace files atomically, and a lock file serializes changes, so other processes can share the directory. Chamber names may not contain `\` or `:` or end in `.` or a space, which some file systems would read as other names. A chamber's history is kept under one file name that holds its whole path, so chamber paths are limited to about 200 bytes. |
| `boltdb` | `path` | An embedded key/value database, `realm.db` in the directory `path`. The directory must exist. Only one process can open the database: another fails after 1 second. Each change, such as a chamber and its history entry, is saved in one transaction, so a crash never leaves half of it. |
| `bigcache` | `shards`, `life_window`, `clean_window` | In memory, for development and tests: data is lost when the server stops, and memory grows with every write. Entries are kept until then, unless you set `life_window` (a duration such as `"24h"`), which deletes each entry that long after it was written. |
| `gcs` | `bucket` | Google Cloud Storage. Uses the standard Google credential chain. |
| `s3` | `bucket`, `region`, `prefix`, `endpoint`, `force_path_style` | Amazon S3 or an S3-compatible service. Uses the standard AWS credential chain. |
| `postgres` | `url`, `max_conns` | PostgreSQL. Supports several server replicas. Refer to [Run several replicas](#run-several-replicas). |
| `etcd` | `endpoints`, `prefix`, `username`, `password`, `ca_file`, `cert_file`, `key_file` | An etcd v3 cluster. Supports several server replicas. Refer to [Run several replicas on etcd](#run-several-replicas-on-etcd). |
| `cacheable` | `cache`, `source`, plus the options of both | A `cache` in front of a `source`. Reads fill the cache; writes go to the source and drop the entry from the cache. A `bigcache` cache keeps entries for `life_window` (default 5 minutes). The source cannot be `postgres` or `etcd`. |

For production, we recommend `postgres`, or `etcd` if you already run a
cluster (on Kubernetes, say). For a single replica, `cacheable` with
`bigcache` in front of `s3` also works.

> [!IMPORTANT]
> With any backend other than `postgres` and `etcd`, run only one server
> replica. Other backends check concurrent changes and send real-time updates
> only within one server.

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
- The first start creates the tables, which needs `CREATE` on the schema.
  After that, `SELECT`, `INSERT`, `UPDATE`, and `DELETE` on them are enough.
- Unless your server, database, role, or URL sets them, sessions use
  `statement_timeout` 30s, `lock_timeout` 15s, and
  `idle_in_transaction_session_timeout` 30s, so a replica that dies mid-write
  cannot block the others' writes for long.
- A replica whose `LISTEN` connection stops answering for 10 seconds
  reconnects. `/readyz` fails when its change feed has not caught up for 30
  seconds, and the `realm.postgres.feed.age` metric reports how far behind it
  is. After a restore or failover that rewinds the database, every stream
  re-reads its chamber.

### Run several replicas on etcd

Use the `etcd` backend with an etcd v3 cluster (tested with etcd 3.7).

```json
{
  "server": {
    "storage": "etcd",
    "options": {
      "endpoints": "https://etcd-0.internal:2379,https://etcd-1.internal:2379,https://etcd-2.internal:2379",
      "ca_file": "/etc/realm/etcd-ca.pem",
      "username": "realm",
      "password": "${REALM_ETCD_PASSWORD}"
    },
    "port": "8080",
    "inheritable": true
  }
}
```

- Entries live under `prefix` (default `/realm`), so other applications, or
  other realm installations with their own prefix, can share the cluster.
  With etcd authentication, give realm's user read and write on that prefix.
- `password` may name an environment variable as `${NAME}`. With `https://`
  endpoints or `ca_file`, realm uses TLS; `cert_file` and `key_file` add a
  client certificate for `--client-cert-auth`.
- Every replica sees every change within milliseconds: etcd checks each
  write's versions (so `If-Match` works across replicas), and each replica
  watches realm's prefix and pushes changes to its streams.
- etcd caps each transaction at `--max-txn-ops` operations (128 by default)
  and each request at `--max-request-bytes` (1.5 MiB). Deleting a chamber
  together with the chambers below it takes two or three operations per
  chamber, so a large tree may need deleting in parts, or a higher limit. A
  change over either limit is refused with 413, and nothing is written.
- If the cluster compacts away revisions a replica's watch still needed, or
  is restored from an older backup, every stream re-reads its chamber.
  `/readyz` fails when the change feed has not been current for 30 seconds,
  and the `realm.etcd.feed.age` metric reports how long it has not been.

### With Docker

The image reads its config from `/etc/realm/realm.json`, a default location.
Mount your config file at that path, or set `REALM_CONFIG` to another path:

```bash
docker run -p 8080:8080 \
  -v "$PWD/configs/realm.json:/etc/realm/realm.json:ro" \
  ghcr.io/steviebps/realm
```

- The image runs as a non-root user.
- To require authentication without setting up sign-in, put an admin key in
  the config and pass it in from a secret. Then make API keys with
  `realm client keys` from anywhere that can reach the server. Refer to
  [Make the first API key](#make-the-first-api-key).

  ```bash
  # realm.json: "auth": {"adminKey": "${REALM_ADMIN_KEY}"}
  docker run -p 8080:8080 -e REALM_ADMIN_KEY \
    -v "$PWD/realm.json:/etc/realm/realm.json:ro" \
    ghcr.io/steviebps/realm
  ```

- To keep data from the `file` or `boltdb` backends, set `path` to a
  directory in `/var/lib/realm`. That directory is a volume.
- The `s3` and `gcs` backends work with Kubernetes workload identity. They need
  no more configuration.
- Use `GET /healthz` for liveness probes and `GET /readyz` for readiness
  probes. `/readyz` reports 503 when storage does not answer within 2 seconds,
  when the `postgres` or `etcd` change feed has stalled, and while the server drains for
  shutdown. It checks storage at most once a second, however often it is
  probed.

### Run in production

- **Shutdown.** On `SIGTERM` the server fails `/readyz`, waits
  `shutdownDelay`, closes open streams (clients reconnect to another replica),
  and lets in-flight requests finish for up to 20 seconds. Keep
  `terminationGracePeriodSeconds` above `shutdownDelay` + 20s.
- **Logs** are one JSON object per line on standard error. On a terminal they
  are human-readable instead; set `REALM_LOG_FORMAT` to `json` or `console` to
  choose.
- **Access logs.** Every request is logged when it ends, with its status,
  duration, caller (`actor`), request ID, and, when it failed, why: changes
  at `info`, refused credentials and permissions at `warn`, server errors at
  `error`, and reads (evaluations included) and probes at `debug`. Client
  mistakes, such as reading a chamber that does not exist, are never logged
  as errors. Every response carries its request ID in `X-Request-Id`; the
  server reuses the ID your proxy sends in that header.
- **Telemetry** (traces and metrics) is off by default. Turn it on with
  `--traces=otlp` (or `REALM_TRACES=otlp`) to export over OTLP/HTTP, or
  `--traces=stdout` to print traces. The exporter is configured by the
  standard `OTEL_*` variables: `OTEL_EXPORTER_OTLP_ENDPOINT`,
  `OTEL_SERVICE_NAME` (default `realm`), `OTEL_RESOURCE_ATTRIBUTES`,
  `OTEL_TRACES_SAMPLER`; `OTEL_SDK_DISABLED=true` overrides `--traces`. Every
  request except probes and streams gets a span, and the server records
  `realm.server.streams.active`, `realm.postgres.feed.reconnects`, and
  `realm.etcd.feed.restarts`.
- **Streams.** Each server holds at most 10,000 streams (`maxStreams`);
  beyond that, new streams get 503 and clients retry with backoff. Evaluation
  streams, which anyone holding a published evaluate key can open, may take
  at most three quarters of them, so server SDKs keep hearing about changes. Streams close after about 20
  to 25 minutes and clients reconnect, so a revoked key or expired session
  loses its stream within that time. A stream whose client takes more than 30
  seconds to accept an event is closed. When storage fails, streams stay open
  and retry with backoff; clients keep the values they have.
- **Browser security.** Responses forbid framing and MIME sniffing. The UI is
  served with a strict `Content-Security-Policy` (only its own scripts,
  styles, and fonts; requests only to this server).
- **Limits.** Request bodies are limited to 4 MiB (evaluation contexts to
  64 KiB). Request headers must arrive within 10 seconds, the rest of a
  request within the 10-second request timeout, and a client must take the
  response within 30 seconds.

## Manage flags

You can change flags in three ways:

- **Web UI** at `/ui/`. Edit targeting and rollouts, examine a diff before you
  save, and restore an earlier version from the history.
- **CLI** with `realm client`.
- **HTTP API** at `/v1/chambers/<path>`.

### CLI

`realm client` sends requests to a running server. realm uses the first server
address it finds: `--address`, then `$REALM_ADDRESS`, then `client.address` in
the [config file](#where-realm-finds-its-config).

```bash
realm client list /                  # list the chambers below a path
realm client get /prod/checkout/     # show one chamber as JSON
realm client put /prod/checkout/     # make an empty chamber (fails if it exists)
realm client put /prod/checkout/ -f checkout.json  # create or replace from a file ("-" for stdin)
realm client delete /prod/checkout/  # delete a chamber (the chambers below it are kept)
realm client delete -r /prod/        # delete a chamber and every chamber below it
```

> [!NOTE]
> `realm client put -f` replaces the whole chamber. The file is validated
> first: an unknown field anywhere (a misspelled `schedule`, say) is an error,
> as it is for every write to the server.

### Sealed flags

With `inheritable`, the nearest chamber that defines a flag wins, so anyone
who can write a child chamber can replace a parent's flag. To stop that, seal
the flag in the parent:

```json
{
  "rules": {
    "entitlement.onlineOrders": {
      "type": "boolean",
      "value": false,
      "sealed": true,
      "targeting": [
        { "clauses": [{ "attribute": "tenantId", "op": "in", "values": ["t-42"] }], "value": true }
      ]
    }
  }
}
```

- Every chamber below the parent serves the parent's definition. If several
  parents seal the flag, the farthest one wins.
- A write to a chamber below it that adds the flag, or changes the chamber's
  definition of it, fails with `400 Bad Request`. The error names the chamber
  that seals the flag. Admins get the same error.
- A definition saved below the parent before the seal is not served. It stays
  in its chamber until someone deletes it, and writes that leave it unchanged
  still succeed. `?inherit=layers` lists these definitions under `hidden`.
- To change the flag, edit it in the chamber that seals it, or unseal it there.
- Without `inheritable`, `sealed` is stored but has no effect.

### HTTP API

| Request | Result |
|---|---|
| `GET /v1/chambers/<path>` | Returns the chamber, with the flags that it inherits. |
| `GET /v1/chambers/<path>?inherit=false` | Returns the chamber as stored, with an `ETag`. |
| `GET /v1/chambers/<path>?inherit=layers` | Returns the chamber and each parent chamber its inherited view consults, nearest first, and which chamber serves each flag (`sources`). Also returns the flags that parents seal (`sealedBy`) and the definitions that those seals hide (`hidden`). Needs read access to each of those parent chambers. |
| `GET /v1/chambers/<path>?list=true` | Lists the chambers below the path. |
| `GET /v1/chambers/<path>?watch=true` | Streams the chamber as server-sent events. |
| `POST /v1/chambers/<path>` | Creates or replaces the chamber. |
| `PATCH /v1/chambers/<path>` | Merges the body into the chamber. |
| `DELETE /v1/chambers/<path>` | Deletes the chamber. The chambers below it are kept. |
| `DELETE /v1/chambers/<path>?recursive=true` | Deletes the chamber and every chamber below it in one change, and returns the deleted paths. Refused for `/`. |
| `GET /v1/history/<path>` | Returns the change history of the chamber. |
| `POST /v1/evaluate/<path>` | Evaluates every flag for the context in the body. Returns only the values. |
| `POST /v1/evaluate/<path>?watch=true` | Streams the evaluated values as server-sent events. |
| `GET /healthz` | Returns the health of the server. |

On storage that tracks revisions (`postgres`, `etcd`, and `boltdb` without a
cache), chamber reads and evaluations carry an `X-Realm-Revision` header, and
each event of a stream carries the same revision as its `id`. It is the
highest storage revision among the chamber and the parents its inherited view
merges, so a write to any of them changes it. A write that leaves a stream's
payload unchanged sends nothing, so a stream's last `id` may be older than the
chamber's revision while describing the same data. The revision names a
version of the chamber but does not order versions: deleting a parent chamber
can lower it.

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
	// If realm is unreachable, Start returns the error but keeps retrying in
	// the background; until it succeeds, reads return your defaults. Don't
	// exit: a service that cannot start without realm turns a realm outage
	// into its own.
	if err := rlm.Start(); err != nil {
		log.Printf("serving default flag values until realm is reachable: %v", err)
	}
	defer rlm.Stop()

	// Outside a request, make a context that holds the current snapshot.
	ctx := rlm.NewContext(context.Background())
	port, _ := rlm.Float64(ctx, "port", 3000)
	log.Println("port:", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		msg, _ := rlm.String(r.Context(), "message", "DEFAULT")
		_, _ = w.Write([]byte(msg))
	})

	// Pin one snapshot to each request, so every flag read in it agrees.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(rlm.NewContext(r.Context())))
	})
	log.Fatal(http.ListenAndServe(":3000", handler))
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
use `WithPollingInterval` (at least 1 second).

To get changes in real time, use `WithStreaming(true)`. The server then sends
changes to the SDK over server-sent events. If the stream keeps failing (three
attempts in a row, or a minute without a delivery), for example because the
server is at its stream limit or a proxy buffers the stream, the SDK also polls
at the polling interval until the stream recovers. If the server does not
support streaming, the SDK polls, and tries the stream again every few
minutes.

The SDK never stops your service: when realm is unreachable it keeps serving
the last flags it loaded (or your defaults), and retries with jittered backoff,
honoring `Retry-After`. A refused key (401, 403) or a missing chamber (404) is
retried after minutes rather than seconds. It logs at `info` and above (set
your own logger with `WithLogger`), with a `component=realm` field: the first
failure and the recovery, and repeated failures at most every 5 minutes.

To record which configuration a request ran under (on its spans or logs, for
example), call `Revision` with the request's context. It returns the server's
revision of the snapshot pinned to the context (see `X-Realm-Revision` under
[HTTP API](#http-api)), or `0` before a chamber loads or when the server's
storage does not track revisions.

```go
ctx = rlm.NewContext(ctx)
span.SetAttributes(attribute.String("realm.revision", strconv.FormatUint(rlm.Revision(ctx), 10)))
```

The SDK reads flags written by newer realm servers: a targeting rule that uses
an operator it does not know never applies, and a flag of a type it does not
know returns your default with an error. The server refuses both in writes it
does not support.

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

[`sdk/typescript`](sdk/typescript) has two clients. Install it with
`npm install @steviebps/realm-sdk`; it is released with realm, at the same
version.

| Client | Use it in | API key | Where flags are evaluated |
|---|---|---|---|
| `ServerClient` | Node services | `read` | in your process, like the Go SDK |
| `BrowserClient` | browsers | `evaluate` | on the server, so the rules stay private |

```ts
import { BrowserClient } from '@steviebps/realm-sdk';

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

`@steviebps/realm-sdk/react` has a provider and hooks for a `BrowserClient`.

1. Make the client and wrap the app in a `RealmProvider`. The provider starts
   the client, and evaluates again when `context` changes, for example when a
   user signs in:

   ```tsx
   // App.tsx
   import { BrowserClient } from '@steviebps/realm-sdk';
   import { RealmProvider } from '@steviebps/realm-sdk/react';

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
   import { useBoolFlag } from '@steviebps/realm-sdk/react';

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

Authentication has three parts:

- **Sign-in** for the web UI, with any OIDC provider or GitHub. Each user gets
  the role `viewer`, `editor`, or `admin`.
- **API keys** for applications and automation. An `evaluate` key is for
  browsers: it can only evaluate flags, so you can put it in front-end code. A
  `read` key is for server SDKs. A `write` key is for automation. Keep `read`
  and `write` keys secret: the server refuses them on requests from browsers.
  A key gives access to its path and to all chambers below it.
- **An admin key**, declared in the server config, for headless installs and
  automation. It can do everything an `admin` user can, including making and
  revoking API keys.

You need sign-in, an admin key, or both. Without sign-in, nobody can use the
web UI and only API keys work, which suits a container that only serves SDKs.
For the `auth` config block, refer to [docs/design/auth.md](docs/design/auth.md).

### Make the first API key

Administrators who sign in manage keys in the web UI (**API keys** in the
header menu). Without the UI, use the admin key:

1. Make a secret of at least 32 characters, and give it to the server as an
   environment variable:

   ```bash
   export REALM_ADMIN_KEY=$(openssl rand -hex 32)
   ```

2. Refer to it in the server config, and start the server:

   ```json
   "auth": {
     "adminKey": "${REALM_ADMIN_KEY}"
   }
   ```

3. Make a key with `realm client keys`, which calls the running server:

   ```bash
   REALM_TOKEN=$REALM_ADMIN_KEY realm client keys create --name checkout --access read --path /prod/
   ```

4. Copy the token from the output. realm shows the token one time only and
   keeps only a hash of it.

Other key commands:

```bash
realm client keys list
realm client keys revoke <id>
```

The server accepts a new key immediately. With several replicas, the others
stop accepting a revoked key in 30 seconds or less.

The admin key is not stored: realm keeps a hash of it in memory. To change it,
change the variable and restart the server. Do not use it in browsers; the
server refuses it there, as it does `read` and `write` keys.

### Use an API key

The SDK and the CLI send the key as a bearer token
(`Authorization: Bearer realm_…`).

- **SDK:** set `Token` in `client.HttpClientConfig`.
- **CLI:** realm uses the first value it finds: `--token`, then `$REALM_TOKEN`,
  then `client.token` in the config file.

```bash
export REALM_ADDRESS=https://realm.example.com
export REALM_TOKEN=realm_…
realm client get /prod/checkout/
```

## Reference

### Global CLI flags

| Flag | Meaning |
|---|---|
| `-c`, `--config` | The realm config file. Default: `$REALM_CONFIG`, then the [default locations](#where-realm-finds-its-config). |
| `-a`, `--address` | The address of the realm server (`realm client`). Default: `$REALM_ADDRESS`, then `client.address` in the config file. |
| `--token` | The API key (`realm client`). |
| `-d`, `--debug` | Run in debug mode. |
| `--traces` | Export traces and metrics: `off` (default), `otlp`, or `stdout`. Default: `$REALM_TRACES`, then `off`. |

`realm server` also accepts `--dev` (in-memory storage, no config file) and
`--port`.

### Environment variables

| Variable | Meaning |
|---|---|
| `REALM_CONFIG` | The config file, when `--config` is not given. |
| `REALM_ADDRESS` | The server address for `realm client`, when `--address` is not given. |
| `REALM_TOKEN` | The API key for `realm client`, when `--token` is not given. |
| `REALM_LOG_FORMAT` | `json` or `console` log output. |
| `REALM_TRACES` | `off`, `otlp`, or `stdout` telemetry export, when `--traces` is not given. |

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): the data model, request flow, storage, and roadmap.
- [docs/design/targeting.md](docs/design/targeting.md): the targeting and rollout design.
- [docs/design/auth.md](docs/design/auth.md): the authentication design and the `auth` config.
- [docs/design/evaluation.md](docs/design/evaluation.md): server-side evaluation and the TypeScript SDK.
- [CONTRIBUTING.md](CONTRIBUTING.md): build, test, and send a pull request.

## License

realm is licensed under the Apache License 2.0. Refer to [LICENSE.txt](LICENSE.txt).
