# Authentication and access

realm has two kinds of callers, and each authenticates differently:

| Caller | Credential | Can do |
|---|---|---|
| Applications (SDKs, browsers), automation, the CLI | **API key**, sent as `Authorization: Bearer realm_…` | `evaluate`, `read`, or `write` chambers at or beneath the key's **path** |
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
      "githubOrgsTTL": "1h",
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
          "clientSecret": "${GOOGLE_CLIENT_SECRET}",
          "hostedDomains": ["example.com"],
          "authoritativeDomains": ["example.com"]
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
  starting. Two options harden a company identity provider:
  - **`hostedDomains`** refuses sign-ins whose ID token lacks an `hd`
    (hosted domain) claim naming one of these domains. Google sets `hd` only
    for Google Workspace accounts, so this keeps out consumer Google accounts
    that someone registered with a company address. With a single domain,
    realm also passes it to Google as the `hd` hint, so the account chooser
    offers only that domain's accounts.
  - **`authoritativeDomains`** makes the provider the authority for email
    addresses in these domains; see [Roles](#roles).
- **`github`**: GitHub OAuth apps. GitHub is not OIDC, so realm reads the
  user's verified primary email and organization memberships from the GitHub
  API. It requests the scopes `read:user user:email read:org`. GitHub accepts
  any verified email and never re-checks it, so a GitHub primary email shows
  only that the user once controlled the address. It cannot be
  `authoritativeDomains` for a domain or have `hostedDomains`.

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

**Which provider may vouch for an email.** By default every provider may
vouch for every address, so with both a company IdP and GitHub configured,
someone whose GitHub primary email is `@example.com` (perhaps an address they
no longer control) matches `"domains": ["example.com"]`. To prevent that, list
the domain under the company provider's `authoritativeDomains`. Once any
provider claims a domain, email and domain grants for addresses in it match
only users who signed in through a provider that claims it. Addresses in
unclaimed domains still match through any provider, so existing configs keep
working. Organization grants are unaffected.

The session stores who signed in and through which provider, not their role.
The role is recomputed from the config on every request, so removing
someone's grant and restarting the server takes effect immediately.

**GitHub organization memberships** are read once, at sign-in, and stored in
the session. They are trusted for `githubOrgsTTL` (default `1h`). After that,
a user whose role depends on an organization grant must sign in again, which
reads their memberships afresh, so someone removed from an organization loses
access within that time. A user with an email or domain grant at least as high
keeps their session. After upgrading, sessions issued before this check
existed count as stale.

The first administrator is whoever you list under `access.admins`; no
bootstrap token is needed.

## API keys

A key has a **name**, an **access** level and a **path**. The access levels
are:

- `evaluate`, for browsers: it may only evaluate flags for a context
  (`POST /v1/evaluate/…`), which reveals values but never rules, so it is safe
  in front-end code. It cannot read chambers, history, or the catalog. See
  [evaluation.md](evaluation.md).
- `read`, for server SDKs: it may read and watch chambers and their history.
  A key scoped to the root path `/` may also read the attribute catalog.
- `write`, for automation: it may also change chambers.

Each level includes the ones before it, but `read` and `write` keys are
secrets: the evaluate endpoint refuses them on requests from a browser (those
with an `Origin` header), so one cannot be shipped in front-end code by
mistake.

A key works only for chambers at its path or beneath it (`/prod/` covers
`/prod/` and `/prod/checkout/`, but not `/production/`). Keys cannot manage
keys or change the catalog.

The attribute catalog belongs to the whole realm, not to a chamber, and its
labels can name every tenant or location. So only `read` and `write` keys
scoped to `/` may read it. A key scoped to `/prod/` cannot, even though some
of the catalog's values may appear in `/prod/` chambers.

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
| `POST /v1/evaluate/…` | key or session | `CanEvaluate(path)`: any key covering the path, any signed-in user; only evaluate keys when the request has an `Origin` header; CORS-enabled for bearer tokens |
| `/v1/catalog…` | key or session | read: `CanReadCatalog()`, so any signed-in user or a `read`/`write` key scoped to `/`; write: admin |
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
  domain. A verified email is only as trustworthy as the provider behind it;
  use `authoritativeDomains` (and `hostedDomains` with Google) so domain
  grants depend on the company IdP alone.
- **Organization removal** takes effect within `githubOrgsTTL`, not
  immediately, because memberships are cached in the session.
- **Replicas.** Sessions work on any replica that shares `sessionSecret`. Key
  verification caches keys for up to 30 seconds per process. With the
  `postgres` backend, writes (concurrency checks, history, catalog, key
  creation) are checked in the database, so any number of replicas is safe;
  with other backends they are serialized per process (and, with `file`, across
  processes on one machine through a lock file), so run one replica.

## Not yet built

- `realm login` for people using the CLI (OIDC device flow); the CLI uses keys.
- Per-path roles for signed-in users. Roles are global; only keys are
  path-scoped.
- Key expiry and last-used timestamps.
