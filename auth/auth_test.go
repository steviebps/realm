package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/steviebps/realm/auth/authtest"
	"github.com/steviebps/realm/pkg/storage"
	"golang.org/x/oauth2"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newStorage(t *testing.T) storage.Storage {
	t.Helper()
	s, err := storage.StorageOptions["bigcache"](map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPrincipalManagesKeys(t *testing.T) {
	adminKey := &Principal{Kind: KindKey, Access: AccessAdmin, Path: "/prod/"}
	rootAdminKey := &Principal{Kind: KindKey, Access: AccessAdmin, Path: "/"}
	writeKey := &Principal{Kind: KindKey, Access: AccessWrite, Path: "/"}
	admin := &Principal{Kind: KindUser, Role: RoleAdmin}
	editor := &Principal{Kind: KindUser, Role: RoleEditor}
	var none *Principal

	tests := []struct {
		name    string
		p       *Principal
		path    string
		manages bool // ManagesKeys
		can     bool // CanManageKeys(path)
	}{
		{"admin key in scope", adminKey, "/prod/app/", true, true},
		{"admin key at scope", adminKey, "/prod", true, true},
		{"admin key above scope", adminKey, "/", true, false},
		{"admin key prefix lookalike", adminKey, "/production/", true, false},
		{"root admin key", rootAdminKey, "/staging/", true, true},
		{"write key", writeKey, "/prod/", false, false},
		{"admin", admin, "/prod/", true, true},
		{"editor", editor, "/prod/", false, false},
		{"anonymous", Anonymous, "/prod/", true, true},
		{"nil", none, "/prod/", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.ManagesKeys(); got != tt.manages {
				t.Errorf("ManagesKeys = %v, want %v", got, tt.manages)
			}
			if got := tt.p.CanManageKeys(tt.path); got != tt.can {
				t.Errorf("CanManageKeys(%q) = %v, want %v", tt.path, got, tt.can)
			}
		})
	}
}

func TestPrincipalPermissions(t *testing.T) {
	evalKey := &Principal{Kind: KindKey, Access: AccessEvaluate, Path: "/prod/"}
	readKey := &Principal{Kind: KindKey, Access: AccessRead, Path: "/prod/"}
	writeKey := &Principal{Kind: KindKey, Access: AccessWrite, Path: "/prod/"}
	rootKey := &Principal{Kind: KindKey, Access: AccessWrite, Path: "/"}
	rootReadKey := &Principal{Kind: KindKey, Access: AccessRead, Path: ""}
	rootEvalKey := &Principal{Kind: KindKey, Access: AccessEvaluate, Path: "/"}
	adminKey := &Principal{Kind: KindKey, Access: AccessAdmin, Path: "/prod/"}
	rootAdminKey := &Principal{Kind: KindKey, Access: AccessAdmin, Path: "/"}
	viewer := &Principal{Kind: KindUser, Role: RoleViewer}
	editor := &Principal{Kind: KindUser, Role: RoleEditor}
	admin := &Principal{Kind: KindUser, Role: RoleAdmin}
	var none *Principal

	tests := []struct {
		name            string
		p               *Principal
		path            string
		read, write, ad bool
		eval            bool
	}{
		{"evaluate key in scope", evalKey, "/prod/app/", false, false, false, true},
		{"evaluate key out of scope", evalKey, "/staging/", false, false, false, false},
		{"read key in scope", readKey, "/prod/app/", true, false, false, true},
		{"read key at scope", readKey, "/prod", true, false, false, true},
		{"read key out of scope", readKey, "/staging/", false, false, false, false},
		{"read key prefix lookalike", readKey, "/production/", false, false, false, false},
		{"read key parent", readKey, "/", false, false, false, false},
		{"write key in scope", writeKey, "/prod/app/", true, true, false, true},
		{"write key out of scope", writeKey, "/dev/", false, false, false, false},
		{"root key", rootKey, "/anything/", true, true, false, true},
		{"root read key", rootReadKey, "/anything/", true, false, false, true},
		{"root evaluate key", rootEvalKey, "/anything/", false, false, false, true},
		{"admin key in scope", adminKey, "/prod/app/", true, true, false, true},
		{"admin key out of scope", adminKey, "/dev/", false, false, false, false},
		{"root admin key", rootAdminKey, "/anything/", true, true, true, true},
		{"viewer", viewer, "/prod/", true, false, false, true},
		{"editor", editor, "/prod/", true, true, false, true},
		{"admin", admin, "/prod/", true, true, true, true},
		{"anonymous", Anonymous, "/prod/", true, true, true, true},
		{"nil", none, "/prod/", false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.CanRead(tt.path); got != tt.read {
				t.Errorf("CanRead = %v, want %v", got, tt.read)
			}
			if got := tt.p.CanWrite(tt.path); got != tt.write {
				t.Errorf("CanWrite = %v, want %v", got, tt.write)
			}
			if got := tt.p.IsAdmin(); got != tt.ad {
				t.Errorf("IsAdmin = %v, want %v", got, tt.ad)
			}
			if got := tt.p.CanEvaluate(tt.path); got != tt.eval {
				t.Errorf("CanEvaluate = %v, want %v", got, tt.eval)
			}
			// the catalog is realm-wide: only root-scoped read keys may read it
			wantCatalog := tt.p != nil && tt.p != evalKey && tt.p != readKey && tt.p != writeKey && tt.p != adminKey && tt.p != rootEvalKey
			if got := tt.p.CanReadCatalog(); got != wantCatalog {
				t.Errorf("CanReadCatalog = %v, want %v", got, wantCatalog)
			}
		})
	}
}

func TestRoleFor(t *testing.T) {
	cfg := validConfig()
	cfg.Providers = []ProviderConfig{
		{Name: "corp", Type: ProviderOIDC, Issuer: "https://idp.example.com", ClientID: "id", ClientSecret: "s", AuthoritativeDomains: []string{"@Example.com"}},
		{Name: "partner", Type: ProviderOIDC, Issuer: "https://idp.partner.io", ClientID: "id", ClientSecret: "s"},
		{Name: "gh", Type: ProviderGitHub, ClientID: "id", ClientSecret: "s"},
	}
	cfg.Access = AccessConfig{
		Admins:  Grant{Emails: []string{"Owner@Example.com", "lead@partner.io"}},
		Editors: Grant{Domains: []string{"example.com", "@partner.io"}},
		Viewers: Grant{GitHubOrgs: []string{"Example-Org"}},
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		provider, email string
		orgs            []string
		want            Role
	}{
		{"corp", "owner@example.com", nil, RoleAdmin},
		{"corp", "someone@example.com", nil, RoleEditor},
		{"corp", "someone@EXAMPLE.com", nil, RoleEditor},
		{"corp", "someone@sub.example.com", nil, RoleNone},
		{"corp", "someone@example.com.evil.io", nil, RoleNone},
		{"corp", "", nil, RoleNone},
		// corp is authoritative for example.com: no other provider vouches for it
		{"partner", "owner@example.com", nil, RoleNone},
		{"gh", "owner@example.com", nil, RoleNone},
		{"gh", "someone@example.com", nil, RoleNone},
		{"gh", "someone@example.com", []string{"example-org"}, RoleViewer},
		// nobody claims partner.io, so any provider vouches for it
		{"partner", "lead@partner.io", nil, RoleAdmin},
		{"corp", "lead@partner.io", nil, RoleAdmin},
		{"gh", "someone@partner.io", nil, RoleEditor},
		{"gh", "outsider@other.io", []string{"example-org"}, RoleViewer},
		{"gh", "outsider@other.io", []string{"other-org"}, RoleNone},
		{"corp", "owner@example.com", []string{"example-org"}, RoleAdmin},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.email+"/"+strings.Join(tt.orgs, ","), func(t *testing.T) {
			if got := cfg.RoleFor(tt.provider, tt.email, tt.orgs); got != tt.want {
				t.Errorf("RoleFor = %v, want %v", got, tt.want)
			}
		})
	}
}

func validConfig() Config {
	return Config{
		ExternalURL:   "https://realm.example.com",
		SessionSecret: testSecret,
		Providers:     []ProviderConfig{{Name: "google", Type: ProviderOIDC, Issuer: "https://accounts.google.com", ClientID: "id", ClientSecret: "secret"}},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Setenv("REALM_TEST_SECRET", testSecret)
	tests := []struct {
		name   string
		modify func(*Config)
		err    string
	}{
		{"valid", func(c *Config) {}, ""},
		{"secret from env", func(c *Config) { c.SessionSecret = "${REALM_TEST_SECRET}" }, ""},
		{"short secret", func(c *Config) { c.SessionSecret = "short" }, "sessionSecret"},
		{"missing env secret", func(c *Config) { c.SessionSecret = "${REALM_TEST_UNSET}" }, "sessionSecret"},
		{"relative url", func(c *Config) { c.ExternalURL = "/realm" }, "externalURL"},
		{"bad ttl", func(c *Config) { c.SessionTTL = "-1h" }, "sessionTTL"},
		{"no providers", func(c *Config) { c.Providers = nil }, "providers"},
		{"duplicate provider", func(c *Config) { c.Providers = append(c.Providers, c.Providers[0]) }, "duplicate"},
		{"unknown type", func(c *Config) { c.Providers[0].Type = "saml" }, "unknown type"},
		{"oidc without issuer", func(c *Config) { c.Providers[0].Issuer = "" }, "issuer"},
		{"no client secret", func(c *Config) { c.Providers[0].ClientSecret = "" }, "clientSecret"},
		{"github", func(c *Config) {
			c.Providers[0] = ProviderConfig{Name: "gh", Type: ProviderGitHub, ClientID: "id", ClientSecret: "s"}
		}, ""},
		{"github orgs ttl", func(c *Config) { c.GitHubOrgsTTL = "15m" }, ""},
		{"bad github orgs ttl", func(c *Config) { c.GitHubOrgsTTL = "0s" }, "githubOrgsTTL"},
		{"hosted and authoritative domains", func(c *Config) {
			c.Providers[0].HostedDomains = []string{"example.com"}
			c.Providers[0].AuthoritativeDomains = []string{"@example.com", "example.org"}
		}, ""},
		{"empty hosted domain", func(c *Config) { c.Providers[0].HostedDomains = []string{""} }, "hostedDomains"},
		{"email as authoritative domain", func(c *Config) { c.Providers[0].AuthoritativeDomains = []string{"me@example.com"} }, "authoritativeDomains"},
		{"github cannot be authoritative", func(c *Config) {
			c.Providers[0] = ProviderConfig{Name: "gh", Type: ProviderGitHub, ClientID: "id", ClientSecret: "s", AuthoritativeDomains: []string{"example.com"}}
		}, "apply only"},
		{"github has no hosted domain", func(c *Config) {
			c.Providers[0] = ProviderConfig{Name: "gh", Type: ProviderGitHub, ClientID: "id", ClientSecret: "s", HostedDomains: []string{"example.com"}}
		}, "apply only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.modify(&c)
			err := c.validate()
			if tt.err == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.err)
			}
		})
	}
}

func TestSealer(t *testing.T) {
	s, err := newSealer(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.seal("a", session{Email: "x@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var got session
	if err := s.open("a", sealed, &got); err != nil || got.Email != "x@example.com" {
		t.Fatalf("open = %+v, %v", got, err)
	}
	if err := s.open("b", sealed, &got); err == nil {
		t.Error("a value sealed for one purpose opened for another")
	}
	tampered := []byte(sealed)
	tampered[len(tampered)-2] ^= 1
	if err := s.open("a", string(tampered), &got); err == nil {
		t.Error("a tampered value opened")
	}
	other, _ := newSealer(strings.Repeat("z", 32))
	if err := other.open("a", sealed, &got); err == nil {
		t.Error("a value opened with a different secret")
	}
}

func TestKeyStore(t *testing.T) {
	ctx := context.Background()
	strg := newStorage(t)
	keys := NewKeyStore(strg)

	if _, _, err := keys.Create(ctx, NewKeyRequest{Name: "", Access: AccessRead}, "t"); err == nil {
		t.Error("created a key without a name")
	}
	if _, _, err := keys.Create(ctx, NewKeyRequest{Name: "x", Access: "root"}, "t"); err == nil {
		t.Error("created a key with unknown access")
	}
	if _, _, err := keys.Create(ctx, NewKeyRequest{Name: "x", Access: AccessRead, Path: "/_realm/keys"}, "t"); err == nil {
		t.Error("created a key scoped to realm's metadata")
	}

	key, token, err := keys.Create(ctx, NewKeyRequest{Name: "sdk", Access: AccessRead, Path: "prod"}, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, TokenPrefix) || key.Hash != "" || key.Path != "/prod/" || key.CreatedBy != "owner@example.com" {
		t.Fatalf("unexpected key %+v / token %q", key, token)
	}

	// another server process sees the key immediately
	other := NewKeyStore(strg)
	got, err := other.Verify(ctx, token)
	if err != nil || got.ID != key.ID || got.Principal().Path != "/prod/" {
		t.Fatalf("Verify = %+v, %v", got, err)
	}

	for _, bad := range []string{"", "realm_", "realm_" + key.ID, "realm_" + key.ID + "_wrong", "other_" + key.ID + "_x", "realm_nope_x"} {
		if _, err := keys.Verify(ctx, bad); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Verify(%q) = %v, want ErrInvalidKey", bad, err)
		}
	}

	list, err := keys.List(ctx)
	if err != nil || len(list) != 1 || list[0].Hash != "" {
		t.Fatalf("List = %+v, %v", list, err)
	}

	// an admin key revokes only keys within its own path; others look absent
	ci, _, err := keys.Create(ctx, NewKeyRequest{Name: "ci", Access: AccessWrite, Path: "/prod/ci/"}, "t")
	if err != nil {
		t.Fatal(err)
	}
	stagingAdmin := &Principal{Kind: KindKey, Access: AccessAdmin, Path: "/staging/"}
	if err := keys.RevokeFor(ctx, ci.ID, stagingAdmin); !errors.As(err, new(*storage.NotFoundError)) {
		t.Fatalf("RevokeFor out of scope = %v, want NotFoundError", err)
	}
	prodAdmin := &Principal{Kind: KindKey, Access: AccessAdmin, Path: "/prod/"}
	if err := keys.RevokeFor(ctx, ci.ID, prodAdmin); err != nil {
		t.Fatalf("RevokeFor in scope: %v", err)
	}
	if list, _ = keys.List(ctx); len(list) != 1 || list[0].ID != key.ID {
		t.Fatalf("after RevokeFor, List = %+v", list)
	}

	if err := keys.Revoke(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Verify(ctx, token); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("revoked key still verifies: %v", err)
	}
	// the other process notices once its cache expires
	other.snap.Store(&keySnapshot{keys: other.snap.Load().keys, loadedAt: time.Now().Add(-keyCacheTTL - time.Second)})
	if _, err := other.Verify(ctx, token); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("revoked key still verifies in another process: %v", err)
	}
	var nf *storage.NotFoundError
	if err := keys.Revoke(ctx, key.ID); !errors.As(err, &nf) {
		t.Errorf("revoking twice = %v, want NotFoundError", err)
	}
}

// realmServer serves the authenticator's routes plus /whoami, which reports
// the authenticated principal, from an httptest server whose URL is the
// configured external URL.
func realmServer(t *testing.T, configure func(*Config)) (*Authenticator, *httptest.Server) {
	t.Helper()
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)

	cfg := Config{ExternalURL: srv.URL, SessionSecret: testSecret}
	configure(&cfg)
	a, err := New(cfg, newStorage(t))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/auth/", a.Handler())
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		p, err := a.Authenticate(r)
		switch {
		case err != nil:
			http.Error(w, err.Error(), http.StatusForbidden)
		case p == nil:
			http.Error(w, "nobody", http.StatusUnauthorized)
		default:
			_, _ = io.WriteString(w, p.ID+" "+p.Role.String()+" "+p.Provider)
		}
	})
	mux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ui:"+r.URL.Path) })
	handler = mux
	return a, srv
}

func get(t *testing.T, c *http.Client, target string) (int, string) {
	t.Helper()
	res, err := c.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, strings.TrimSpace(string(b))
}

func TestOIDCSignIn(t *testing.T) {
	issuer := authtest.NewIssuer(t, "owner@example.com")
	_, srv := realmServer(t, func(c *Config) {
		c.Access = AccessConfig{Admins: Grant{Emails: []string{"owner@example.com"}}, Viewers: Grant{Domains: []string{"example.com"}}}
		c.Providers = []ProviderConfig{{Name: "test", Type: ProviderOIDC, Issuer: issuer.URL, ClientID: authtest.ClientID, ClientSecret: authtest.ClientSecret}}
	})

	t.Run("admin signs in and returns where they started", func(t *testing.T) {
		browser := authtest.Browser(t)
		if status, _ := get(t, browser, srv.URL+"/whoami"); status != http.StatusUnauthorized {
			t.Fatalf("before sign-in: %d", status)
		}
		status, body := get(t, browser, srv.URL+"/v1/auth/login/test?returnTo=/ui/chambers/prod/")
		if status != http.StatusOK || body != "ui:/ui/chambers/prod/" {
			t.Fatalf("sign-in ended at %d %q", status, body)
		}
		if status, body := get(t, browser, srv.URL+"/whoami"); status != http.StatusOK || body != "owner@example.com admin test" {
			t.Fatalf("after sign-in: %d %q", status, body)
		}

		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/auth/logout", nil)
		res, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if status, _ := get(t, browser, srv.URL+"/whoami"); status != http.StatusUnauthorized {
			t.Fatalf("after sign-out: %d", status)
		}
	})

	t.Run("returnTo cannot leave the server", func(t *testing.T) {
		issuer.SignInAs("someone@example.com", true)
		status, body := get(t, authtest.Browser(t), srv.URL+"/v1/auth/login/test?returnTo=//evil.example/")
		if status != http.StatusOK || body != "ui:/ui/" {
			t.Fatalf("sign-in ended at %d %q", status, body)
		}
	})

	t.Run("users without a role are refused", func(t *testing.T) {
		issuer.SignInAs("stranger@other.io", true)
		browser := authtest.Browser(t)
		if status, body := get(t, browser, srv.URL+"/v1/auth/login/test"); status != http.StatusForbidden || !strings.Contains(body, "stranger@other.io") {
			t.Fatalf("sign-in = %d %q", status, body)
		}
		if status, _ := get(t, browser, srv.URL+"/whoami"); status != http.StatusUnauthorized {
			t.Fatalf("refused user has a session: %d", status)
		}
	})

	t.Run("unverified emails are refused", func(t *testing.T) {
		issuer.SignInAs("owner@example.com", false)
		if status, _ := get(t, authtest.Browser(t), srv.URL+"/v1/auth/login/test"); status != http.StatusUnauthorized {
			t.Fatalf("sign-in = %d", status)
		}
	})

	t.Run("callbacks without the login cookie are refused", func(t *testing.T) {
		status, _ := get(t, authtest.Browser(t), srv.URL+"/v1/auth/callback/test?code=x&state=y")
		if status != http.StatusBadRequest {
			t.Fatalf("callback = %d", status)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		if status, _ := get(t, authtest.Browser(t), srv.URL+"/v1/auth/login/nope"); status != http.StatusNotFound {
			t.Fatalf("login = %d", status)
		}
	})
}

func TestCrossOriginWritesAreRefused(t *testing.T) {
	issuer := authtest.NewIssuer(t, "owner@example.com")
	a, srv := realmServer(t, func(c *Config) {
		c.Access = AccessConfig{Admins: Grant{Emails: []string{"owner@example.com"}}}
		c.Providers = []ProviderConfig{{Name: "test", Type: ProviderOIDC, Issuer: issuer.URL, ClientID: authtest.ClientID, ClientSecret: authtest.ClientSecret}}
	})
	browser := authtest.Browser(t)
	get(t, browser, srv.URL+"/v1/auth/login/test")
	u, _ := url.Parse(srv.URL)
	cookies := browser.Jar.Cookies(u)

	tests := []struct {
		name    string
		method  string
		headers map[string]string
		wantErr error
	}{
		{"same-origin write", http.MethodPost, map[string]string{"Origin": srv.URL, "Sec-Fetch-Site": "same-origin"}, nil},
		{"write without browser headers", http.MethodPost, nil, nil},
		{"cross-site write", http.MethodPost, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, ErrCrossOrigin},
		{"foreign origin write", http.MethodDelete, map[string]string{"Origin": "https://evil.example"}, ErrCrossOrigin},
		{"cross-site read", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, srv.URL+"/v1/chambers/", nil)
			for _, c := range cookies {
				r.AddCookie(c)
			}
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			p, err := a.Authenticate(r)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && (p == nil || p.Role != RoleAdmin) {
				t.Fatalf("principal = %+v", p)
			}
		})
	}
}

func TestBearerTokens(t *testing.T) {
	a, _ := realmServer(t, func(c *Config) {
		c.Providers = []ProviderConfig{{Name: "gh", Type: ProviderGitHub, ClientID: "id", ClientSecret: "secret"}}
	})
	_, token, err := a.Keys().Create(context.Background(), NewKeyRequest{Name: "ci", Access: AccessWrite, Path: "/prod/"}, "t")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		header  string
		wantErr error
		want    string
	}{
		{"Bearer " + token, nil, "key:ci"},
		{"Bearer realm_bogus_token", ErrInvalidKey, ""},
		{"Basic " + token, ErrInvalidKey, ""},
		{"", nil, ""},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodPost, "/v1/chambers/prod/", nil)
		// bearer tokens are not subject to cross-origin checks
		r.Header.Set("Origin", "https://elsewhere.example")
		if tt.header != "" {
			r.Header.Set("Authorization", tt.header)
		}
		p, err := a.Authenticate(r)
		if !errors.Is(err, tt.wantErr) {
			t.Errorf("%q: err = %v, want %v", tt.header, err, tt.wantErr)
			continue
		}
		if got := ""; p != nil {
			got = p.Actor()
			if got != tt.want {
				t.Errorf("%q: actor = %q, want %q", tt.header, got, tt.want)
			}
		} else if tt.want != "" {
			t.Errorf("%q: no principal", tt.header)
		}
	}
}

// fakeGitHub serves GitHub's OAuth and user API endpoints.
func fakeGitHub(t *testing.T, emails, orgs string) *httptest.Server {
	t.Helper()
	var challenge string
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		challenge = q.Get("code_challenge")
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=gh-code&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("code") != "gh-code" || oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != challenge {
			http.Error(w, `{"error":"bad_verification_code"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"gh-token","token_type":"bearer"}`)
	})
	api := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer gh-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}
	}
	mux.HandleFunc("/user", api(`{"login":"octo","name":""}`))
	mux.HandleFunc("/user/emails", api(emails))
	mux.HandleFunc("/user/orgs", api(orgs))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubSignIn(t *testing.T) {
	tests := []struct {
		name, emails, orgs string
		wantStatus         int
		want               string
	}{
		{
			name:       "org member",
			emails:     `[{"email":"old@other.io","primary":false,"verified":true},{"email":"octo@other.io","primary":true,"verified":true}]`,
			orgs:       `[{"login":"Example-Org"}]`,
			wantStatus: http.StatusOK,
			want:       "octo@other.io viewer gh",
		},
		{
			name:       "not a member",
			emails:     `[{"email":"octo@other.io","primary":true,"verified":true}]`,
			orgs:       `[]`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "unverified primary email",
			emails:     `[{"email":"octo@other.io","primary":true,"verified":false}]`,
			orgs:       `[{"login":"example-org"}]`,
			wantStatus: http.StatusUnauthorized,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := fakeGitHub(t, tt.emails, tt.orgs)
			a, srv := realmServer(t, func(c *Config) {
				c.Access = AccessConfig{Viewers: Grant{GitHubOrgs: []string{"example-org"}}}
				c.Providers = []ProviderConfig{{Name: "gh", Type: ProviderGitHub, ClientID: "id", ClientSecret: "secret"}}
			})
			p := a.providers["gh"].(*githubProvider)
			p.endpoint = oauth2.Endpoint{AuthURL: gh.URL + "/login/oauth/authorize", TokenURL: gh.URL + "/login/oauth/access_token"}
			p.apiURL = gh.URL

			browser := authtest.Browser(t)
			if status, _ := get(t, browser, srv.URL+"/v1/auth/login/gh"); status != tt.wantStatus {
				t.Fatalf("sign-in = %d, want %d", status, tt.wantStatus)
			}
			if tt.want == "" {
				return
			}
			if status, body := get(t, browser, srv.URL+"/whoami"); status != http.StatusOK || body != tt.want {
				t.Fatalf("whoami = %d %q, want %q", status, body, tt.want)
			}
		})
	}
}

// useFakeGitHub points the GitHub provider named name at a fake GitHub.
func useFakeGitHub(a *Authenticator, name string, gh *httptest.Server) {
	p := a.providers[name].(*githubProvider)
	p.endpoint = oauth2.Endpoint{AuthURL: gh.URL + "/login/oauth/authorize", TokenURL: gh.URL + "/login/oauth/access_token"}
	p.apiURL = gh.URL
}

func TestHostedDomain(t *testing.T) {
	issuer := authtest.NewIssuer(t, "someone@example.com")
	_, srv := realmServer(t, func(c *Config) {
		c.Access = AccessConfig{Viewers: Grant{Domains: []string{"example.com"}}}
		c.Providers = []ProviderConfig{{Name: "google", Type: ProviderOIDC, Issuer: issuer.URL, ClientID: authtest.ClientID, ClientSecret: authtest.ClientSecret, HostedDomains: []string{"Example.com"}}}
	})
	tests := []struct {
		name       string
		hd         any
		wantStatus int
	}{
		{"workspace account", "example.com", http.StatusOK},
		{"workspace account, other case", "EXAMPLE.COM", http.StatusOK},
		{"consumer account with a company address", nil, http.StatusUnauthorized},
		{"another workspace", "other.io", http.StatusUnauthorized},
		{"hd of the wrong type", true, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issuer.SetClaim("hd", tt.hd)
			browser := authtest.Browser(t)
			if status, _ := get(t, browser, srv.URL+"/v1/auth/login/google"); status != tt.wantStatus {
				t.Fatalf("sign-in = %d, want %d", status, tt.wantStatus)
			}
			if got := issuer.LastAuthorize().Get("hd"); got != "example.com" {
				t.Errorf("authorization request hd = %q, want the configured domain as a hint", got)
			}
			wantWhoami := http.StatusUnauthorized
			if tt.wantStatus == http.StatusOK {
				wantWhoami = http.StatusOK
			}
			if status, _ := get(t, browser, srv.URL+"/whoami"); status != wantWhoami {
				t.Fatalf("whoami = %d, want %d", status, wantWhoami)
			}
		})
	}
}

func TestAuthoritativeDomains(t *testing.T) {
	corp := authtest.NewIssuer(t, "")
	partner := authtest.NewIssuer(t, "")
	gh := fakeGitHub(t, `[{"email":"former@example.com","primary":true,"verified":true}]`, `[]`)
	a, srv := realmServer(t, func(c *Config) {
		c.Access = AccessConfig{
			Admins:  Grant{Emails: []string{"former@example.com"}},
			Editors: Grant{Domains: []string{"example.com"}},
			Viewers: Grant{Domains: []string{"partner.io"}},
		}
		c.Providers = []ProviderConfig{
			{Name: "corp", Type: ProviderOIDC, Issuer: corp.URL, ClientID: authtest.ClientID, ClientSecret: authtest.ClientSecret, AuthoritativeDomains: []string{"example.com"}},
			{Name: "partner", Type: ProviderOIDC, Issuer: partner.URL, ClientID: authtest.ClientID, ClientSecret: authtest.ClientSecret},
			{Name: "gh", Type: ProviderGitHub, ClientID: "id", ClientSecret: "secret"},
		}
	})
	useFakeGitHub(a, "gh", gh)

	tests := []struct {
		name, provider, email string
		issuer                *authtest.Issuer
		wantStatus            int
		want                  string
	}{
		{"authoritative provider", "corp", "someone@example.com", corp, http.StatusOK, "someone@example.com editor corp"},
		{"authoritative provider, email grant", "corp", "former@example.com", corp, http.StatusOK, "former@example.com admin corp"},
		{"other OIDC provider for a claimed domain", "partner", "someone@example.com", partner, http.StatusForbidden, ""},
		{"GitHub email in a claimed domain", "gh", "", nil, http.StatusForbidden, ""},
		{"unclaimed domain through any provider", "partner", "guest@partner.io", partner, http.StatusOK, "guest@partner.io viewer partner"},
		{"unclaimed domain through the authoritative provider", "corp", "guest@partner.io", corp, http.StatusOK, "guest@partner.io viewer corp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.issuer != nil {
				tt.issuer.SignInAs(tt.email, true)
			}
			browser := authtest.Browser(t)
			if status, _ := get(t, browser, srv.URL+"/v1/auth/login/"+tt.provider); status != tt.wantStatus {
				t.Fatalf("sign-in = %d, want %d", status, tt.wantStatus)
			}
			if tt.want == "" {
				return
			}
			if status, body := get(t, browser, srv.URL+"/whoami"); status != http.StatusOK || body != tt.want {
				t.Fatalf("whoami = %d %q, want %q", status, body, tt.want)
			}
		})
	}

	// A session issued before a provider claimed the domain stops matching
	// domain grants once it does, since roles are recomputed per request.
	s := session{Email: "someone@example.com", Provider: "partner", Expires: time.Now().Add(time.Hour).Unix()}
	sealed, err := a.sealer.seal(SessionCookie, s)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: sealed})
	if p, err := a.Authenticate(r); p != nil || err != nil {
		t.Errorf("session from a non-authoritative provider = %+v, %v", p, err)
	}
}

func TestGitHubOrgsExpire(t *testing.T) {
	a, _ := realmServer(t, func(c *Config) {
		c.GitHubOrgsTTL = "1h"
		c.Access = AccessConfig{
			Admins:  Grant{Emails: []string{"lead@other.io"}},
			Editors: Grant{GitHubOrgs: []string{"example-org"}},
			Viewers: Grant{Domains: []string{"other.io"}, GitHubOrgs: []string{"viewers-org"}},
		}
		c.Providers = []ProviderConfig{{Name: "gh", Type: ProviderGitHub, ClientID: "id", ClientSecret: "secret"}}
	})
	now := time.Now()
	fresh, stale := now.Add(-time.Minute).Unix(), now.Add(-2*time.Hour).Unix()

	tests := []struct {
		name   string
		email  string
		orgs   []string
		orgsAt int64
		want   Role // RoleNone: no principal, so the user must sign in again
	}{
		{"fresh org grant", "octo@other.io", []string{"example-org"}, fresh, RoleEditor},
		{"stale org grant", "octo@else.io", []string{"viewers-org"}, stale, RoleNone},
		{"stale org grant above the user's other grants", "octo@other.io", []string{"example-org"}, stale, RoleNone},
		{"stale org grant the user does not need", "lead@other.io", []string{"example-org"}, stale, RoleAdmin},
		{"stale org grant matching another grant's role", "octo@other.io", []string{"viewers-org"}, stale, RoleViewer},
		{"session from before orgsAt existed", "octo@else.io", []string{"viewers-org"}, 0, RoleNone},
		{"no orgs", "octo@other.io", nil, 0, RoleViewer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sealed, err := a.sealer.seal(SessionCookie, session{Email: tt.email, Provider: "gh", GitHubOrgs: tt.orgs, OrgsAt: tt.orgsAt, Expires: now.Add(time.Hour).Unix()})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/whoami", nil)
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: sealed})
			p, err := a.Authenticate(r)
			if err != nil {
				t.Fatal(err)
			}
			got := RoleNone
			if p != nil {
				got = p.Role
			}
			if got != tt.want {
				t.Errorf("role = %v, want %v", got, tt.want)
			}
		})
	}
}
