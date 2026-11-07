package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/pkg/storage"
	"golang.org/x/oauth2"
)

const (
	// SessionCookie holds a signed-in user's session.
	SessionCookie = "realm_session"
	// loginCookie holds the state of a sign-in in progress.
	loginCookie = "realm_login"
	// loginTTL bounds how long a user may take at the provider.
	loginTTL = 10 * time.Minute

	// Routes served by the authenticator.
	LoginPath    = "/v1/auth/login/"
	CallbackPath = "/v1/auth/callback/"
	LogoutPath   = "/v1/auth/logout"
)

// ErrCrossOrigin rejects cookie-authenticated writes from other sites.
var ErrCrossOrigin = errors.New("cross-origin request refused")

// Authenticator identifies the caller of each request.
type Authenticator struct {
	cfg  *Config
	keys *KeyStore
	// sealer protects session and login cookies; nil without providers,
	// when nobody can sign in.
	sealer    *sealer
	providers map[string]provider
}

// New returns an Authenticator for cfg whose API keys live in strg, which
// must be the raw storage beneath any inheritance wrapper.
func New(cfg Config, strg storage.Storage) (*Authenticator, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	a := &Authenticator{cfg: &cfg, keys: NewKeyStore(strg), providers: map[string]provider{}}
	if len(cfg.Providers) > 0 {
		var err error
		if a.sealer, err = newSealer(cfg.SessionSecret); err != nil {
			return nil, err
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		a.providers[p.Name] = newProvider(p, client)
	}
	return a, nil
}

// Keys returns the store of API keys.
func (a *Authenticator) Keys() *KeyStore { return a.keys }

// ProviderInfo describes a sign-in option for the UI.
type ProviderInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

// Providers lists the configured sign-in providers in configuration order.
func (a *Authenticator) Providers() []ProviderInfo {
	list := make([]ProviderInfo, 0, len(a.cfg.Providers))
	for _, c := range a.cfg.Providers {
		list = append(list, ProviderInfo{Name: c.Name, DisplayName: c.DisplayName, Type: c.Type})
	}
	return list
}

// session is the content of the session cookie. The role is not stored; it
// is recomputed from the access configuration on every request, so changes
// to grants apply immediately.
type session struct {
	Email      string   `json:"email"`
	Name       string   `json:"name,omitempty"`
	Provider   string   `json:"provider"`
	GitHubOrgs []string `json:"githubOrgs,omitempty"`
	// OrgsAt is when GitHubOrgs was read from GitHub (Unix seconds).
	OrgsAt  int64 `json:"orgsAt,omitempty"`
	Expires int64 `json:"exp"`
}

// loginState is the content of the login cookie between redirect and callback.
type loginState struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce"`
	ReturnTo string `json:"returnTo"`
	Expires  int64  `json:"exp"`
}

// Authenticate returns the caller of r: the admin key or an API key
// presented as a bearer token, or a user's session cookie. It returns nil
// without error when the request carries no credentials, and an error when
// they are invalid.
func (a *Authenticator) Authenticate(r *http.Request) (*Principal, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		// the scheme is case-insensitive (RFC 7235, section 2.1)
		scheme, token, _ := strings.Cut(h, " ")
		if !strings.EqualFold(scheme, "Bearer") {
			return nil, ErrInvalidKey
		}
		token = strings.TrimSpace(token)
		if a.isAdminKey(token) {
			p := adminKeyPrincipal
			return &p, nil
		}
		key, err := a.keys.Verify(r.Context(), token)
		if err != nil {
			return nil, err
		}
		return key.Principal(), nil
	}

	if a.sealer == nil {
		return nil, nil
	}
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, nil
	}
	var s session
	if err := a.sealer.open(SessionCookie, cookie.Value, &s); err != nil {
		return nil, nil
	}
	if time.Now().Unix() > s.Expires {
		return nil, nil
	}
	if !safeMethod(r.Method) && !a.sameOrigin(r) {
		return nil, ErrCrossOrigin
	}
	role := a.cfg.RoleFor(s.Provider, s.Email, s.GitHubOrgs)
	// Memberships are a snapshot from sign-in. Once they are older than
	// githubOrgsTTL, a user who owes their role to one must sign in again,
	// so someone removed from an organization loses access promptly.
	if len(s.GitHubOrgs) > 0 && time.Since(time.Unix(s.OrgsAt, 0)) > a.cfg.githubOrgsTTL &&
		a.cfg.RoleFor(s.Provider, s.Email, nil) < role {
		return nil, nil
	}
	if role == RoleNone {
		return nil, nil
	}
	return &Principal{Kind: KindUser, ID: s.Email, Name: s.Name, Provider: s.Provider, Role: role}, nil
}

// isAdminKey reports whether token is the admin key declared in the config.
func (a *Authenticator) isAdminKey(token string) bool {
	return a.cfg.adminKeyHash != "" && subtle.ConstantTimeCompare([]byte(hashSecret(token)), []byte(a.cfg.adminKeyHash)) == 1
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin guards cookie-authenticated writes against cross-site request
// forgery, on top of the cookie's SameSite=Lax.
func (a *Authenticator) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		return strings.EqualFold(origin, a.cfg.external.Scheme+"://"+a.cfg.external.Host)
	}
	return true
}

// Handler serves the sign-in routes: LoginPath<provider>, CallbackPath<provider>,
// and LogoutPath.
func (a *Authenticator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(LoginPath, a.handleLogin)
	mux.HandleFunc(CallbackPath, a.handleCallback)
	mux.HandleFunc(LogoutPath, a.handleLogout)
	return mux
}

func (a *Authenticator) redirectURL(provider string) string {
	return a.cfg.external.String() + CallbackPath + url.PathEscape(provider)
}

func (a *Authenticator) secureCookies() bool {
	return a.cfg.external != nil && a.cfg.external.Scheme == "https"
}

func (a *Authenticator) setCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   a.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

// handleLogin starts a sign-in: GET /v1/auth/login/<provider>?returnTo=/ui/…
func (a *Authenticator) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, LoginPath)
	p, ok := a.providers[name]
	if !ok {
		http.Error(w, "unknown sign-in provider", http.StatusNotFound)
		return
	}

	state, err1 := randomHex(16)
	nonce, err2 := randomHex(16)
	if err := errors.Join(err1, err2); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	login := loginState{
		Provider: name,
		State:    state,
		Verifier: oauth2.GenerateVerifier(),
		Nonce:    nonce,
		ReturnTo: safeReturnTo(r.URL.Query().Get("returnTo")),
		Expires:  time.Now().Add(loginTTL).Unix(),
	}
	target, err := p.authURL(ctx, a.redirectURL(name), login.State, login.Verifier, login.Nonce)
	if err != nil {
		logging.Ctx(ctx).ErrorCtx(ctx).Str("provider", name).Msg(err.Error())
		a.signInFailed(w, http.StatusBadGateway, "The sign-in provider is unavailable. Try again shortly.")
		return
	}
	sealed, err := a.sealer.seal(loginCookie, login)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	a.setCookie(w, loginCookie, sealed, CallbackPath, int(loginTTL.Seconds()))
	http.Redirect(w, r, target, http.StatusFound)
}

// handleCallback finishes a sign-in: GET /v1/auth/callback/<provider>?code=…&state=…
func (a *Authenticator) handleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.Ctx(ctx)
	name := strings.TrimPrefix(r.URL.Path, CallbackPath)
	p, ok := a.providers[name]
	if !ok {
		http.Error(w, "unknown sign-in provider", http.StatusNotFound)
		return
	}
	a.setCookie(w, loginCookie, "", CallbackPath, -1)

	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		a.signInFailed(w, http.StatusUnauthorized, "Sign-in was cancelled or refused: "+e)
		return
	}
	var login loginState
	cookie, err := r.Cookie(loginCookie)
	if err == nil {
		err = a.sealer.open(loginCookie, cookie.Value, &login)
	}
	if err != nil || login.Provider != name || login.State == "" || login.State != q.Get("state") || time.Now().Unix() > login.Expires {
		a.signInFailed(w, http.StatusBadRequest, "This sign-in link is invalid or has expired.")
		return
	}

	id, err := p.identify(ctx, a.redirectURL(name), q.Get("code"), login.Verifier, login.Nonce)
	if err != nil {
		logger.ErrorCtx(ctx).Str("provider", name).Msg(err.Error())
		a.signInFailed(w, http.StatusUnauthorized, "Sign-in failed: "+err.Error())
		return
	}
	if a.cfg.RoleFor(name, id.Email, id.GitHubOrgs) == RoleNone {
		logger.InfoCtx(ctx).Str("provider", name).Str("email", id.Email).Msg("sign-in refused: no role granted")
		a.signInFailed(w, http.StatusForbidden, fmt.Sprintf("%s has not been given access to this realm. Ask an administrator to grant you a role.", id.Email))
		return
	}

	now := time.Now()
	s := session{Email: id.Email, Name: id.Name, Provider: name, GitHubOrgs: id.GitHubOrgs, OrgsAt: now.Unix(), Expires: now.Add(a.cfg.sessionTTL).Unix()}
	sealed, err := a.sealer.seal(SessionCookie, s)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	a.setCookie(w, SessionCookie, sealed, "/", int(a.cfg.sessionTTL.Seconds()))
	logger.InfoCtx(ctx).Str("provider", name).Str("email", id.Email).Msg("signed in")
	http.Redirect(w, r, login.ReturnTo, http.StatusFound)
}

// handleLogout ends the session: POST /v1/auth/logout
func (a *Authenticator) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	a.setCookie(w, SessionCookie, "", "/", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Authenticator) signInFailed(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Sign-in failed</title></head>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem">
<h1>Couldn't sign you in</h1><p>%s</p><p><a href="/ui/">Back to realm</a></p></body></html>`, html.EscapeString(message))
}

// safeReturnTo keeps post-sign-in redirects on this server. Control
// characters are refused along with backslashes: browsers drop tabs and line
// breaks from a URL before parsing it, so "/<tab>/evil.example" would
// otherwise redirect to "//evil.example", another site.
func safeReturnTo(target string) string {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.ContainsFunc(target, unsafeInRedirect) {
		return "/ui/"
	}
	return target
}

// unsafeInRedirect reports whether r (a backslash or an ASCII control
// character) can make a browser read a local path as another site's.
func unsafeInRedirect(r rune) bool {
	return r == '\\' || r < ' ' || r == 0x7f
}
