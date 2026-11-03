# realm

[![release](https://github.com/steviebps/realm/actions/workflows/go.yml/badge.svg)](https://github.com/steviebps/realm/actions/workflows/go.yml)

```bash
go install github.com/steviebps/realm
```

## example commands

### server

#### start a local realm server

```bash
realm server --config ./configs/realm.json
```

#### run with Docker

Multi-arch images (`linux/amd64`, `linux/arm64`) with the web UI embedded are
published to GitHub Container Registry: `latest` and semver tags for releases,
`edge` for `master`.

```bash
# try it out: in-memory storage, UI at http://localhost:8080/ui/
docker run --rm -p 8080:8080 ghcr.io/steviebps/realm server --dev

# with a config file (the default command reads /etc/realm/realm.json)
docker run -p 8080:8080 -v "$PWD/configs/realm.json:/etc/realm/realm.json:ro" ghcr.io/steviebps/realm
```

The image runs as a non-root user. Point the `file` or `boltdb` backends at
`/var/lib/realm` (a volume) to persist data; the `s3` and `gcs` backends use
the standard AWS / Google credential chains, so workload identity on
Kubernetes works without extra configuration. `GET /healthz` is a liveness
endpoint suitable for probes.

### client

`realm client` talks to a running server. Set the server with `--address`
(or `client.address` in the config file).

```bash
realm client list /                                 # chambers beneath a path
realm client get /prod/checkout/                    # one chamber, as JSON
realm client put /prod/checkout/                    # create an empty chamber
realm client delete /prod/checkout/
```

When the server has authentication enabled, pass an API key. The CLI reads
`--token` first, then `$REALM_TOKEN`, then `client.token` in the config file:

```bash
export REALM_TOKEN=realm_…
realm client --address https://realm.example.com get /prod/checkout/
```

### API keys

Administrators manage keys in the UI ("API keys" in the header menu). For
headless setups, and to create the first key, `realm keys` works directly on
the storage named by a server config. It needs no running server and no
sign-in.

```bash
# evaluate keys for browsers, read keys for server SDKs, write keys for automation;
# a key covers its path and everything beneath it
realm keys create --config realm.json --name web --access evaluate --path /prod/
realm keys create --config realm.json --name checkout --access read --path /prod/
realm keys create --config realm.json --name ci --access write --path /

realm keys list   --config realm.json
realm keys revoke --config realm.json <id>
```

`create` prints the token once; only a hash of it is stored. A running server
picks up new keys immediately and revocations within 30 seconds. BoltDB is
locked while a server is running, so stop the server first or use the UI. The
in-memory `bigcache` backend can't be used with `realm keys`.

## example code snippets

```go
package main

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "log"
 "net/http"
 "os"
 "os/signal"
 "syscall"
 "time"

 "github.com/steviebps/realm/client"
 realmhttp "github.com/steviebps/realm/http"
 realm "github.com/steviebps/realm/pkg"
)

type CustomStruct struct {
 Foo string `json:"foo,omitempty"`
}

func main() {
 var err error

 client, err := client.NewHttpClient(&client.HttpClientConfig{Address: "http://localhost:8080"})
 if err != nil {
  log.Fatal(err)
 }

 rlm, err := realm.NewRealm(realm.WithHttpClient(client), realm.WithVersion("v1.0.0"), realm.WithPath("root"), realm.WithPollingInterval(1*time.Minute))
 if err != nil {
  log.Fatal(err)
 }
 err = rlm.Start()
 if err != nil {
  log.Fatal(err)
 }

 bootCtx := rlm.NewContext(context.Background())
 port, _ := rlm.Float64(bootCtx, "port", 3000)

 mux := http.NewServeMux()
 mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
  message, _ := rlm.String(r.Context(), "message", "DEFAULT")
  w.Write([]byte(message))
 })

 mux.HandleFunc("/custom", func(w http.ResponseWriter, r *http.Request) {
  var custom *CustomStruct
  if err := rlm.CustomValue(r.Context(), "custom", &custom); err != nil {
   http.Error(w, err.Error(), http.StatusInternalServerError)
   return
  }

  w.Header().Set("Content-Type", "application/json")
  json.NewEncoder(w).Encode(custom)
 })

 rlmHandler := realmhttp.RealmHandler(rlm, mux)

 server := &http.Server{Addr: fmt.Sprintf(":%d", int(port)), Handler: rlmHandler}

 go func() {
  log.Println("Listening on :", port)
  if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
   log.Fatal(err)
  }
 }()

 sigChan := make(chan os.Signal, 1)
 signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
 <-sigChan

 rlm.Stop()
 if err := server.Shutdown(bootCtx); err != nil {
  log.Fatal(err.Error())
 }
}
```

### targeting and progressive rollout

Each flag has a default `value` and an ordered list of `targeting` rules. The
first rule whose clauses all match the evaluation context wins; otherwise the
default is served. A rule may carry a `rollout` that limits it to a
deterministic, sticky percentage of matching subjects.

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

Operators: `in`, `notIn`, `startsWith` (e.g. build SHAs), `semverGte`,
`semverLte`. A clause on an attribute the context does not set never matches.
`rollout.bucketBy` defaults to `key`; bucketing by a coarser attribute such as
`locationId` rolls out to whole groups at once.

Attributes come from two places. Process-wide facts are set once on the SDK,
and per-request facts travel with the request context (request wins):

```go
rlm, _ := realm.NewRealm(
  realm.WithHttpClient(client),
  realm.WithPath("root"),
  realm.WithVersion("v2.3.0"), // the "version" attribute
  realm.WithAttributes(map[string]string{"environment": "prod", "service": "checkout", "buildSha": sha}),
)

ctx := rlm.NewContextWithEvaluation(r.Context(), realm.EvaluationContext{
  Key:        userID,
  Attributes: map[string]string{"tenantId": tenantID, "locationId": locationID},
})
enabled, _ := rlm.Bool(ctx, "new-checkout", false)
```

See [docs/design/targeting.md](docs/design/targeting.md) for the full model.

### real-time updates (streaming)

By default the SDK polls (every 15 min). Opt into streaming with
`WithStreaming(true)` and flag changes are pushed to the client over
server-sent events within milliseconds, with automatic fallback to polling if
the server doesn't support it.

```go
rlm, _ := realm.NewRealm(
	realm.WithHttpClient(client),
	realm.WithPath("root"),
	realm.WithStreaming(true),
)
```

You can watch a chamber directly over HTTP too:

```bash
curl -N 'http://localhost:8080/v1/chambers/root?watch=true'
```

### authentication

Add an `auth` block to the server config to require sign-in for the UI
(any OIDC provider, or GitHub) with viewer / editor / admin roles, and API keys
for applications and automation. Keys are `evaluate` (browsers), `read`, or
`write`, and limited to a chamber path:

```bash
realm keys create --config realm.json --name checkout --access read --path /prod/
```

```go
client, _ := client.NewHttpClient(&client.HttpClientConfig{
	Address: "https://realm.example.com",
	Token:   os.Getenv("REALM_TOKEN"),
})
```

Without an `auth` block the server is open to anyone who can reach it. See
[docs/design/auth.md](docs/design/auth.md).

### TypeScript and browsers

[`sdk/typescript`](sdk/typescript) has two clients. `ServerClient` evaluates
locally with a `read` key, like the Go SDK. `BrowserClient` asks the server to
evaluate for one context with an `evaluate` key, so rules never reach the
browser:

```ts
const flags = new BrowserClient({ address, token, path: '/prod/web/', context: { key: userId } });
await flags.start();
flags.bool('new-checkout', false);
```

Other languages can call the endpoint directly:

```bash
curl -X POST http://localhost:8080/v1/evaluate/prod/web/ \
  -H "Authorization: Bearer $REALM_TOKEN" \
  -d '{"key": "user-123", "attributes": {"tenantId": "acme"}}'
```

See [docs/design/evaluation.md](docs/design/evaluation.md).
