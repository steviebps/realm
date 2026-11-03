# Authentication and access

realm has two kinds of callers, and each authenticates differently:

| Caller | Credential | Can do |
|---|---|---|
| Applications (SDK), automation, the CLI | **API key**, sent as `Authorization: Bearer realm_…` | `read` or `write` chambers at or beneath the key's **path** |
| People using the UI | **Sign-in** with an OIDC provider or GitHub, kept in a session cookie | depends on their **role**: viewer, editor, or admin |

Authentication is **off unless the server config has an `auth` block**. Without
it the server is open to anyone who can reach it (it logs a warning at
startup), which keeps `realm server --dev` and existing installs working.

The code lives in `auth/` (keys, sessions, providers, roles) and
`http/auth.go` (middleware, `/v1/auth/me`, `/v1/keys`).

## Configuration

```json
{
  "server": {
    "storage": "s3",
    "options": { "bucket": "my-realm" },
    "auth": {
      "externalURL": "https://realm.example.com",
      "sessionSecret": "${REALM_SESSION_SECRET}",
      "sessionTTL": "12h",
      "access": {
        "admins":  { "emails": ["owner@example.com"] },
        "editors": { "domains": ["example.com"] },
        "viewers": { "githubOrgs": ["example-org"] }
      },
      "providers": [
        {
          "name": "google",
          "displayName": "Google",
          "type": "oidc",
          "issuer": "https://accounts.google.com",
          "clientId": "1234.apps.googleusercontent.com",
          "clientSecret": "${GOOGLE_CLIENT_SECRET}"
        },
        {
          "name": "github",
          "displayName": "GitHub",
          "type": "github",
          "clientId": "Iv1.abc123",
          "clientSecret": "${GITHUB_CLIENT_SECRET}"
        }
      ]
    }
  }
}
```

- **`externalURL`**: the address users reach realm at. Sign-in callbacks are
  `<externalURL>/v1/auth/callback/<provider name>`; register that URL with each
  provider. When it is `https`, cookies are marked `Secure`.
- **`sessionSecret`**: at least 32 characters, for example
  `openssl rand -hex 32`. It encrypts session cookies, so every replica must
  share it. Changing it signs everyone out.
- **Secrets** (`sessionSecret`, `clientSecret`) may reference environment
  variables as `${NAME}`. This keeps them out of the config file and works
  with Docker and Kubernetes secrets.

### Providers

- **`oidc`**: any OpenID Connect issuer, configured by discovery from
  `issuer`, such as Google, Okta, Auth0, Keycloak, Microsoft Entra or Dex.
  realm requests the `openid email profile` scopes (override with `scopes`)
  and requires the ID token to carry `email_verified: true`. Discovery runs
  on first sign-in, so an unreachable issuer does not stop the server
  starting.
- **`github`**: GitHub OAuth apps. GitHub is not OIDC, so realm reads the
  user's verified primary email and organization memberships from the GitHub
  API. It requests the scopes `read:user user:email read:org`.

Both use the authorization-code flow with PKCE, a `state` value bound to a
short-lived encrypted cookie, and (for OIDC) a nonce.

### Roles

| Role | May |
|---|---|
| `viewer` | read chambers, history, and the catalog |
| `editor` | also create, change, delete, and restore chambers |
| `admin` | also manage API keys and the attribute catalog |

A grant matches a user by verified **email**, email **domain**, or **GitHub
organization** (GitHub sign-ins only). A user gets the highest role any grant
matches. A user matching no grant is refused at sign-in.

The session stores who signed in, not their role. The role is recomputed from
the config on every request, so removing someone's grant and restarting the
server takes effect immediately.

The first administrator is whoever you list under `access.admins`; no
bootstrap token is needed.

## API keys

A key has a **name**, an **access** level (`read` for SDKs, `write` for
automation) and a **path**. It works only for chambers at that path or beneath
it (`/prod/` covers `/prod/` and `/prod/checkout/`, but not `/production/`).
Keys cannot manage keys or change the catalog.

Tokens look like `realm_<id>_<secret>`. realm stores only a SHA-256 hash of
the secret, under the reserved `/_realm/keys/` prefix, and shows the token
once, when the key is created.

Admins manage keys in the UI ("API keys" in the header) or with the API:

```text
GET    /v1/keys           list keys (no secrets)
POST   /v1/keys           {"name": "checkout", "access": "read", "path": "/prod/"}
DELETE /v1/keys/<id>      revoke
```

For headless installs, `realm keys` works directly on the storage named by a
server config, with no running server or sign-in:

```bash
realm keys create --config realm.json --name ci --access write --path /prod/
realm keys list   --config realm.json
realm keys revoke --config realm.json <id>
```

A running server sees keys created this way immediately, and sees revocations
within 30 seconds. BoltDB is locked by a running server, so with it use the UI
(or stop the server). bigcache is in memory, so `realm keys` refuses it.

### Using a key

```go
c, _ := client.NewHttpClient(&client.HttpClientConfig{
	Address: "https://realm.example.com",
	Token:   os.Getenv("REALM_TOKEN"),
})
rlm, _ := realm.NewRealm(realm.WithHttpClient(c), realm.WithPath("/prod/checkout/"), realm.WithStreaming(true))
```

The CLI reads `--token`, then `$REALM_TOKEN`, then `client.token` in the
config file.

## Endpoints

| Route | Auth | Purpose |
|---|---|---|
| `GET /v1/auth/me` | none | `{authEnabled, user, canEdit, isAdmin, providers}`; the UI starts here |
| `GET /v1/auth/login/<provider>?returnTo=/ui/…` | none | starts sign-in (`returnTo` must be a local path) |
| `GET /v1/auth/callback/<provider>` | none | finishes sign-in, sets the session cookie |
| `POST /v1/auth/logout` | none | clears the session cookie |
| `/v1/chambers/…`, `/v1/history/…` | key or session | read: `CanRead(path)`, write: `CanWrite(path)` |
| `/v1/catalog…` | key or session | read: anyone authenticated, write: admin |
| `/v1/keys…` | admin session | key management |
| `/ui/` | none | static assets; the app shows the sign-in screen |

Routes are private by default: only `/healthz`, `/ui/`, and `/v1/auth/` are
reachable without credentials (`publicPaths` in `http/auth.go`), so a new
route needs no opt-in to be protected. Requests with no credentials get `401`. Requests with credentials that lack
permission get `403`. When auth is on, history records the authenticated
identity (an email, or `key:<name>`) and ignores the self-reported
`X-Realm-Actor` header.

## Security notes

- **CSRF.** The session cookie is `HttpOnly` and `SameSite=Lax`. On top of
  that, cookie-authenticated writes are refused when `Sec-Fetch-Site` says
  another site sent them, or when `Origin` does not match `externalURL`.
  Bearer-token requests are not subject to this check, since browsers never
  attach them on their own.
- **Stateless sessions.** Sessions are AES-GCM-encrypted cookies. Sign-out
  clears the cookie, but a copied cookie stays valid until it expires
  (`sessionTTL`). Revoke access by removing the user's grant.
- **Unverified emails** are refused, because access can be granted by email
  domain.
- **Replicas.** Sessions work on any replica that shares `sessionSecret`. Key
  verification caches keys for up to 30 seconds per process. Writes
  (concurrency checks, history, catalog, key creation) are still serialized
  per process, so run one replica until the Postgres backend lands.

## Not yet built

- `realm login` for people using the CLI (OIDC device flow); the CLI uses keys.
- Per-path roles for signed-in users. Roles are global; only keys are
  path-scoped.
- Key expiry and last-used timestamps.
