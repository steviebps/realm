// Package authtest provides a fake OpenID Connect issuer for tests that
// exercise realm's sign-in flow end to end.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	ClientID     = "realm-test"
	ClientSecret = "realm-test-secret"
)

// Issuer is a fake OIDC issuer that approves every authorization request as
// the current user, checking PKCE and echoing the nonce like a real issuer.
type Issuer struct {
	*httptest.Server

	mu            sync.Mutex
	email         string
	emailVerified bool
	codes         map[string]grant
	signer        jose.Signer
	jwks          jose.JSONWebKeySet
}

type grant struct {
	nonce, challenge, redirectURI, email string
	verified                             bool
}

// NewIssuer starts a fake issuer that signs users in as email.
func NewIssuer(t testing.TB, email string) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "test", Algorithm: string(jose.RS256), Use: "sig"}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jwk}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	iss := &Issuer{
		email:         email,
		emailVerified: true,
		codes:         map[string]grant{},
		signer:        signer,
		jwks:          jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", iss.discovery)
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, iss.jwks) })
	mux.HandleFunc("/authorize", iss.authorize)
	mux.HandleFunc("/token", iss.token)
	iss.Server = httptest.NewServer(mux)
	t.Cleanup(iss.Close)
	return iss
}

// SignInAs changes who the issuer approves next.
func (iss *Issuer) SignInAs(email string, verified bool) {
	iss.mu.Lock()
	defer iss.mu.Unlock()
	iss.email, iss.emailVerified = email, verified
}

func (iss *Issuer) discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                iss.URL,
		"authorization_endpoint":                iss.URL + "/authorize",
		"token_endpoint":                        iss.URL + "/token",
		"jwks_uri":                              iss.URL + "/jwks",
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (iss *Issuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != ClientID || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	code := rand.Text()
	iss.mu.Lock()
	iss.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri"), email: iss.email, verified: iss.emailVerified}
	iss.mu.Unlock()

	target, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	v := target.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	target.RawQuery = v.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (iss *Issuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != ClientID || secret != ClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	iss.mu.Lock()
	g, ok := iss.codes[r.PostForm.Get("code")]
	delete(iss.codes, r.PostForm.Get("code"))
	iss.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || g.redirectURI != r.PostForm.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}

	now := time.Now()
	claims := map[string]any{
		"iss":            iss.URL,
		"aud":            ClientID,
		"sub":            g.email,
		"email":          g.email,
		"email_verified": g.verified,
		"name":           "Test User",
		"nonce":          g.nonce,
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	}
	idToken, err := jwt.Signed(iss.signer).Claims(claims).Serialize()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"access_token": "access", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Browser is an HTTP client with a cookie jar that follows redirects, like a
// browser signing in.
func Browser(t testing.TB) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}
