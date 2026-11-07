package auth

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/steviebps/realm/utils"
)

// DefaultSessionTTL is how long a UI sign-in lasts.
const DefaultSessionTTL = 12 * time.Hour

// DefaultGitHubOrgsTTL is how long GitHub organization memberships read at
// sign-in are trusted.
const DefaultGitHubOrgsTTL = time.Hour

// Config enables authentication on the server. Without it every request is
// allowed. Secrets may reference environment variables as ${NAME}.
//
// It needs sign-in providers, an admin key, or both. Without providers
// nobody can sign in to the UI and only API keys authenticate, so
// externalURL, sessionSecret, and access are not needed.
//
//	"auth": {
//	  "adminKey": "${REALM_ADMIN_KEY}",
//	  "externalURL": "https://realm.example.com",
//	  "sessionSecret": "${REALM_SESSION_SECRET}",
//	  "access": {
//	    "admins":  {"emails": ["owner@example.com"]},
//	    "editors": {"domains": ["example.com"]},
//	    "viewers": {"githubOrgs": ["example-org"]}
//	  },
//	  "providers": [
//	    {"name": "google", "type": "oidc", "issuer": "https://accounts.google.com",
//	     "clientId": "…", "clientSecret": "${GOOGLE_CLIENT_SECRET}",
//	     "hostedDomains": ["example.com"], "authoritativeDomains": ["example.com"]},
//	    {"name": "github", "type": "github", "clientId": "…", "clientSecret": "${GITHUB_CLIENT_SECRET}"}
//	  ]
//	}
type Config struct {
	// AdminKey, if set, is an API key with administrator rights, presented
	// as a bearer token like any other. It is declared here rather than
	// stored, so headless installs have a first credential: it may read and
	// change every chamber, edit the catalog, and create and revoke the
	// stored keys. It must be at least 32 characters (for example the output
	// of `openssl rand -hex 32`) and is usually an environment reference.
	// Change it and restart the server to rotate it.
	AdminKey string `json:"adminKey,omitempty"`
	// ExternalURL is the address users reach the server at; sign-in
	// callbacks are registered with providers under it. Required with
	// providers.
	ExternalURL string `json:"externalURL,omitempty"`
	// SessionSecret signs and encrypts session cookies. It must be at least
	// 32 characters and shared by every replica. Required with providers.
	SessionSecret string `json:"sessionSecret,omitempty"`
	// SessionTTL is a Go duration such as "12h"; DefaultSessionTTL if empty.
	SessionTTL string `json:"sessionTTL,omitempty"`
	// GitHubOrgsTTL is a Go duration bounding how long the GitHub
	// organization memberships read at sign-in are trusted;
	// DefaultGitHubOrgsTTL if empty. After it, a user whose role depends on
	// an organization grant must sign in again, which reads them afresh.
	GitHubOrgsTTL string           `json:"githubOrgsTTL,omitempty"`
	Access        AccessConfig     `json:"access"`
	Providers     []ProviderConfig `json:"providers"`

	// adminKeyHash is the SHA-256 of AdminKey, or "" without one.
	adminKeyHash  string
	sessionTTL    time.Duration
	githubOrgsTTL time.Duration
	external      *url.URL
	// domainOwners maps a lowercased email domain to the names of the
	// providers authoritative for it.
	domainOwners map[string][]string
}

// AccessConfig grants roles to signed-in users. A user receives the highest
// role any grant matches; users matching none are refused.
type AccessConfig struct {
	Admins  Grant `json:"admins"`
	Editors Grant `json:"editors"`
	Viewers Grant `json:"viewers"`
}

// Grant matches users by verified email address, email domain, or GitHub
// organization membership (GitHub sign-ins only). Email and domain entries
// for a domain that a provider is authoritative for (see
// ProviderConfig.AuthoritativeDomains) match only users who signed in
// through such a provider.
type Grant struct {
	Emails     []string `json:"emails,omitempty"`
	Domains    []string `json:"domains,omitempty"`
	GitHubOrgs []string `json:"githubOrgs,omitempty"`
}

// ProviderConfig is a sign-in provider.
type ProviderConfig struct {
	// Name identifies the provider in callback URLs: <externalURL>/v1/auth/callback/<name>.
	Name string `json:"name"`
	// Type is "oidc" (any OpenID Connect issuer) or "github".
	Type string `json:"type"`
	// DisplayName labels the sign-in button; defaults to Name.
	DisplayName string `json:"displayName,omitempty"`
	// Issuer is the OIDC issuer URL, used for discovery.
	Issuer       string   `json:"issuer,omitempty"`
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret"`
	Scopes       []string `json:"scopes,omitempty"`
	// HostedDomains (OIDC only) refuses sign-ins whose ID token does not
	// carry an "hd" (hosted domain) claim naming one of these domains. Google
	// sets "hd" for Google Workspace accounts only, so this keeps out
	// consumer Google accounts registered with a company address.
	HostedDomains []string `json:"hostedDomains,omitempty"`
	// AuthoritativeDomains (OIDC only) makes this provider the authority for
	// email addresses in these domains: once any provider claims a domain,
	// email and domain grants for its addresses match only users who signed
	// in through a provider claiming it. Without it, every provider can vouch
	// for every address, including GitHub, which accepts any verified email.
	AuthoritativeDomains []string `json:"authoritativeDomains,omitempty"`
}

const (
	ProviderOIDC   = "oidc"
	ProviderGitHub = "github"
)

// validate checks the configuration, expands environment references in
// secrets, and fills defaults.
func (c *Config) validate() error {
	if c.AdminKey != "" {
		// A reference to an unset variable expands to "", which must not
		// quietly turn the admin key off.
		key := utils.ExpandEnvRef(c.AdminKey)
		if len(key) < 32 {
			if key == c.AdminKey {
				// a literal key: never echo a secret into the log
				return fmt.Errorf("auth.adminKey must be at least 32 characters (it has %d)", len(key))
			}
			return fmt.Errorf("auth.adminKey must be at least 32 characters (%s holds %d)", c.AdminKey, len(key))
		}
		c.adminKeyHash = hashSecret(key)
		c.AdminKey = ""
	}
	if len(c.Providers) == 0 {
		if c.adminKeyHash == "" {
			return errors.New("auth needs at least one sign-in provider (auth.providers) or an auth.adminKey")
		}
		return nil
	}

	var err error
	if c.external, err = url.Parse(strings.TrimSuffix(c.ExternalURL, "/")); err != nil || c.external.Host == "" ||
		(c.external.Scheme != "https" && c.external.Scheme != "http") {
		return errors.New("auth.externalURL must be an absolute http(s) URL")
	}

	c.SessionSecret = utils.ExpandEnvRef(c.SessionSecret)
	if len(c.SessionSecret) < 32 {
		return errors.New("auth.sessionSecret must be at least 32 characters")
	}

	if c.sessionTTL, err = parseTTL("sessionTTL", c.SessionTTL, DefaultSessionTTL); err != nil {
		return err
	}
	if c.githubOrgsTTL, err = parseTTL("githubOrgsTTL", c.GitHubOrgsTTL, DefaultGitHubOrgsTTL); err != nil {
		return err
	}

	names := map[string]bool{}
	c.domainOwners = map[string][]string{}
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Name == "" || strings.ContainsAny(p.Name, "/?#") {
			return fmt.Errorf("auth.providers[%d].name must be set and URL-safe", i)
		}
		if names[p.Name] {
			return fmt.Errorf("auth.providers: duplicate name %q", p.Name)
		}
		names[p.Name] = true
		if p.DisplayName == "" {
			p.DisplayName = p.Name
		}
		p.ClientSecret = utils.ExpandEnvRef(p.ClientSecret)
		if p.ClientID == "" || p.ClientSecret == "" {
			return fmt.Errorf("auth provider %q needs a clientId and clientSecret", p.Name)
		}
		switch p.Type {
		case ProviderOIDC:
			if p.Issuer == "" {
				return fmt.Errorf("auth provider %q needs an issuer", p.Name)
			}
		case ProviderGitHub:
			// GitHub accepts any verified email, so it cannot vouch for a
			// domain, and it has no hosted-domain claim.
			if len(p.HostedDomains) > 0 || len(p.AuthoritativeDomains) > 0 {
				return fmt.Errorf("auth provider %q: hostedDomains and authoritativeDomains apply only to %q providers", p.Name, ProviderOIDC)
			}
		default:
			return fmt.Errorf("auth provider %q has unknown type %q (want %q or %q)", p.Name, p.Type, ProviderOIDC, ProviderGitHub)
		}
		if p.HostedDomains, err = normalizeDomains(p.Name, "hostedDomains", p.HostedDomains); err != nil {
			return err
		}
		if p.AuthoritativeDomains, err = normalizeDomains(p.Name, "authoritativeDomains", p.AuthoritativeDomains); err != nil {
			return err
		}
		for _, d := range p.AuthoritativeDomains {
			c.domainOwners[d] = append(c.domainOwners[d], p.Name)
		}
	}
	return nil
}

// parseTTL parses the positive duration value of the named setting, or
// returns def when value is empty.
func parseTTL(name, value string, def time.Duration) (time.Duration, error) {
	if value == "" {
		return def, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("auth.%s must be a positive duration: %q", name, value)
	}
	return d, nil
}

// normalizeDomains lowercases domains and strips a leading "@".
func normalizeDomains(provider, field string, domains []string) ([]string, error) {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if d == "" || strings.ContainsAny(d, "@/ ") {
			return nil, fmt.Errorf("auth provider %q: %s has an invalid domain %q", provider, field, d)
		}
		out = append(out, d)
	}
	return out, nil
}

// RoleFor returns the highest role granted to a user who signed in through
// the named provider.
func (c *Config) RoleFor(provider, email string, githubOrgs []string) Role {
	vouched := email != "" && c.vouchesFor(provider, email)
	switch {
	case c.Access.Admins.matches(email, vouched, githubOrgs):
		return RoleAdmin
	case c.Access.Editors.matches(email, vouched, githubOrgs):
		return RoleEditor
	case c.Access.Viewers.matches(email, vouched, githubOrgs):
		return RoleViewer
	}
	return RoleNone
}

// vouchesFor reports whether provider may vouch for email in email and
// domain grants: it is authoritative for the email's domain, or no provider
// is.
func (c *Config) vouchesFor(provider, email string) bool {
	owners, claimed := c.domainOwners[strings.ToLower(emailDomain(email))]
	return !claimed || slices.Contains(owners, provider)
}

func emailDomain(email string) string {
	if at := strings.LastIndexByte(email, '@'); at >= 0 {
		return email[at+1:]
	}
	return ""
}

// matches reports whether g grants a user: by email or domain when the
// user's provider vouches for their email, or by GitHub organization.
func (g *Grant) matches(email string, vouched bool, githubOrgs []string) bool {
	if vouched {
		if slices.ContainsFunc(g.Emails, func(e string) bool { return strings.EqualFold(e, email) }) {
			return true
		}
		domain := emailDomain(email)
		if domain != "" && slices.ContainsFunc(g.Domains, func(d string) bool {
			return strings.EqualFold(strings.TrimPrefix(d, "@"), domain)
		}) {
			return true
		}
	}
	return slices.ContainsFunc(g.GitHubOrgs, func(org string) bool {
		return slices.ContainsFunc(githubOrgs, func(member string) bool { return strings.EqualFold(org, member) })
	})
}
