package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

// Identity is who a provider says signed in.
type Identity struct {
	// Email is verified by the provider.
	Email string
	Name  string
	// GitHubOrgs lists the user's GitHub organizations (GitHub sign-ins only).
	GitHubOrgs []string
	// HostedDomain is the ID token's "hd" claim (OIDC sign-ins only).
	HostedDomain string
}

// provider runs the authorization-code flow (with PKCE) for one sign-in
// provider.
type provider interface {
	// authURL returns where to send the browser to sign in.
	authURL(ctx context.Context, redirectURL, state, verifier, nonce string) (string, error)
	// identify exchanges the callback's code for the user's identity.
	identify(ctx context.Context, redirectURL, code, verifier, nonce string) (*Identity, error)
	// allowsHostedDomain reports whether a user whose sign-in carried the
	// hosted domain hd ("" when none) may still be signed in.
	allowsHostedDomain(hd string) bool
}

// allowsHostedDomain applies the provider's hostedDomains, if any.
func (p *oidcProvider) allowsHostedDomain(hd string) bool {
	return len(p.cfg.HostedDomains) == 0 ||
		slices.ContainsFunc(p.cfg.HostedDomains, func(d string) bool { return strings.EqualFold(d, hd) })
}

// allowsHostedDomain allows everyone: GitHub has no hosted domains.
func (p *githubProvider) allowsHostedDomain(string) bool { return true }

func newProvider(c *ProviderConfig, client *http.Client) provider {
	if c.Type == ProviderGitHub {
		return &githubProvider{cfg: c, client: client, endpoint: github.Endpoint, apiURL: "https://api.github.com"}
	}
	return &oidcProvider{cfg: c, client: client}
}

// oidcProvider signs users in with any OpenID Connect issuer. Discovery runs
// on first use, so an unreachable issuer does not stop the server starting.
type oidcProvider struct {
	cfg    *ProviderConfig
	client *http.Client

	mu       sync.Mutex
	provider *oidc.Provider
}

func (p *oidcProvider) discover(ctx context.Context) (*oidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider == nil {
		discovered, err := oidc.NewProvider(oidc.ClientContext(ctx, p.client), p.cfg.Issuer)
		if err != nil {
			return nil, fmt.Errorf("could not discover OIDC issuer %q: %w", p.cfg.Issuer, err)
		}
		p.provider = discovered
	}
	return p.provider, nil
}

func (p *oidcProvider) oauth(ctx context.Context, redirectURL string) (*oauth2.Config, *oidc.Provider, error) {
	discovered, err := p.discover(ctx)
	if err != nil {
		return nil, nil, err
	}
	scopes := p.cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	return &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		Endpoint:     discovered.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       scopes,
	}, discovered, nil
}

func (p *oidcProvider) authURL(ctx context.Context, redirectURL, state, verifier, nonce string) (string, error) {
	conf, _, err := p.oauth(ctx, redirectURL)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)}
	if len(p.cfg.HostedDomains) == 1 {
		// Google's account chooser offers only accounts in this domain. It is
		// a hint; identify enforces the claim.
		opts = append(opts, oauth2.SetAuthURLParam("hd", p.cfg.HostedDomains[0]))
	}
	return conf.AuthCodeURL(state, opts...), nil
}

func (p *oidcProvider) identify(ctx context.Context, redirectURL, code, verifier, nonce string) (*Identity, error) {
	conf, discovered, err := p.oauth(ctx, redirectURL)
	if err != nil {
		return nil, err
	}
	ctx = oidc.ClientContext(ctx, p.client)
	token, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("could not exchange the authorization code: %w", err)
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("the provider returned no ID token")
	}
	idToken, err := discovered.Verifier(&oidc.Config{ClientID: p.cfg.ClientID}).Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("invalid ID token: %w", err)
	}
	if idToken.Nonce != nonce {
		return nil, errors.New("invalid ID token: nonce mismatch")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
		Name          string `json:"name"`
		HostedDomain  string `json:"hd"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("invalid ID token claims: %w", err)
	}
	// Access is granted by email and domain, so an unverified address could
	// impersonate someone else.
	if claims.Email == "" || claims.EmailVerified == nil || !*claims.EmailVerified {
		return nil, errors.New("the provider did not return a verified email address")
	}
	if !p.allowsHostedDomain(claims.HostedDomain) {
		return nil, fmt.Errorf("%s is not an account of an allowed organization domain", claims.Email)
	}
	return &Identity{Email: claims.Email, Name: claims.Name, HostedDomain: claims.HostedDomain}, nil
}

// githubProvider signs users in with GitHub OAuth, which is not OpenID
// Connect: identity comes from the GitHub API.
type githubProvider struct {
	cfg      *ProviderConfig
	client   *http.Client
	endpoint oauth2.Endpoint
	apiURL   string
}

func (p *githubProvider) oauth(redirectURL string) *oauth2.Config {
	scopes := p.cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"read:user", "user:email", "read:org"}
	}
	return &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		Endpoint:     p.endpoint,
		RedirectURL:  redirectURL,
		Scopes:       scopes,
	}
}

func (p *githubProvider) authURL(_ context.Context, redirectURL, state, verifier, _ string) (string, error) {
	return p.oauth(redirectURL).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), nil
}

func (p *githubProvider) identify(ctx context.Context, redirectURL, code, verifier, _ string) (*Identity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.client)
	conf := p.oauth(redirectURL)
	token, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("could not exchange the authorization code: %w", err)
	}
	api := conf.Client(ctx, token)

	var user struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := p.get(ctx, api, "/user", &user); err != nil {
		return nil, err
	}

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := p.get(ctx, api, "/user/emails", &emails); err != nil {
		return nil, err
	}
	id := &Identity{Name: user.Name}
	if id.Name == "" {
		id.Name = user.Login
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			id.Email = e.Email
		}
	}
	if id.Email == "" {
		return nil, errors.New("your GitHub account has no verified primary email address")
	}

	var orgs []struct {
		Login string `json:"login"`
	}
	if err := p.get(ctx, api, "/user/orgs?per_page=100", &orgs); err != nil {
		return nil, err
	}
	for _, o := range orgs {
		id.GitHubOrgs = append(id.GitHubOrgs, o.Login)
	}
	return id, nil
}

func (p *githubProvider) get(ctx context.Context, client *http.Client, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub API %s: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("GitHub API %s: %s: %s", path, res.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(res.Body).Decode(v)
}
