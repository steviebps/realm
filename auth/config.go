package auth

import (
	"bytes"
	"cmp"
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
//	    "editors": [
//	      {"githubOrgs": ["example-org"]},
//	      {"domains": ["example.com"], "paths": ["/prod/tenants/"]}
//	    ],
//	    "viewers": {"domains": ["example.com"]}
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
	// grants holds every grant of Access with its role and its paths in
	// canonical form.
	grants []roleGrant
}

// AccessConfig grants roles to signed-in users. Each role takes one grant or
// a list of them. A user's role on a chamber is the highest role among the
// grants that match them and cover the chamber's path; users matching no
// grant are refused.
type AccessConfig struct {
	Admins  Grants `json:"admins"`
	Editors Grants `json:"editors"`
	Viewers Grants `json:"viewers"`
}

// Grant matches users by verified email address, email domain, or GitHub
// organization membership (GitHub sign-ins only), and gives them its role on
// the chambers at and beneath Paths. Email and domain entries for a domain
// that a provider is authoritative for (see
// ProviderConfig.AuthoritativeDomains) match only users who signed in
// through such a provider.
type Grant struct {
	Emails     []string `json:"emails,omitempty"`
	Domains    []string `json:"domains,omitempty"`
	GitHubOrgs []string `json:"githubOrgs,omitempty"`
	// Paths limits the grant to the chambers at and beneath these paths, as
	// it limits an API key: "/prod/" covers "/prod/" and "/prod/checkout/",
	// but not "/production/". Without Paths the grant covers every chamber.
	// Admin grants cannot have paths: administrators manage API keys and
	// the attribute catalog, which belong to the whole realm.
	Paths []string `json:"paths,omitempty"`
}

// Grants are the grants of one role. In JSON a role takes one grant object
// or an array of them, so configs written before roles took lists, with one
// object per role, stay valid.
type Grants []Grant

// UnmarshalJSON accepts a grant object, an array of grant objects, or null
// (no grants). Like the rest of the server config, it refuses fields a grant
// does not have: a misspelled "paths" would otherwise give its role on every
// chamber.
func (g *Grants) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*g = nil
		return nil
	}
	var list []Grant
	var err error
	if bytes.HasPrefix(b, []byte("[")) {
		err = utils.ReadRequiredInterfaceWith(bytes.NewReader(b), &list)
	} else {
		list = make([]Grant, 1)
		err = utils.ReadRequiredInterfaceWith(bytes.NewReader(b), &list[0])
	}
	if err != nil {
		return fmt.Errorf("invalid grant: %w", err)
	}
	*g = list
	return nil
}

// roleGrant is a grant with its role. Its paths are canonical, and nil when
// it covers every chamber.
type roleGrant struct {
	role  Role
	grant Grant
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
	// Without providers nobody signs in and access is unused, but a broken
	// grant is still a mistake to report.
	if err := c.validateAccess(); err != nil {
		return err
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

// validateAccess checks the paths of the grants, and keeps every grant with
// its role and its paths in canonical form for RolesFor, highest role first.
func (c *Config) validateAccess() error {
	c.grants = nil
	for _, role := range []struct {
		name   string
		role   Role
		grants Grants
	}{
		{"admins", RoleAdmin, c.Access.Admins},
		{"editors", RoleEditor, c.Access.Editors},
		{"viewers", RoleViewer, c.Access.Viewers},
	} {
		for i, g := range role.grants {
			where := "auth.access." + role.name
			if len(role.grants) > 1 {
				where += fmt.Sprintf("[%d]", i)
			}
			if role.role == RoleAdmin && g.Paths != nil {
				return fmt.Errorf("%s cannot have paths: administrators manage API keys and the attribute catalog, which belong to the whole realm; grant the editor role on those paths instead", where)
			}
			var err error
			if g.Paths, err = grantPaths(g.Paths); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			c.grants = append(c.grants, roleGrant{role: role.role, grant: g})
		}
	}
	return nil
}

// grantPaths returns a grant's paths in canonical form (see scopePath), or
// nil when the grant covers every chamber: it has no paths, or "/" is one.
func grantPaths(paths []string) ([]string, error) {
	if paths == nil {
		return nil, nil
	}
	if len(paths) == 0 {
		// Covering nothing and covering everything are both plausible
		// readings of an empty list; neither is assumed.
		return nil, errors.New(`paths is empty: list the paths the grant covers, or leave "paths" out to cover every chamber`)
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		if p == "" {
			// likely a value that was meant to be filled in
			return nil, errors.New(`paths has an empty path: write "/" to cover every chamber`)
		}
		var err error
		if out[i], err = scopePath(p); err != nil {
			return nil, fmt.Errorf("paths: %w", err)
		}
	}
	if slices.Contains(out, "/") {
		return nil, nil
	}
	return out, nil
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

// RolesFor returns the roles granted to a user who signed in through the
// named provider: their role on every chamber, from the matching grants
// without paths, and the higher roles that matching grants with paths give
// them on the chambers at and beneath those paths, in the form
// Principal.Paths describes. The user may not sign in when they have neither:
// RoleNone and no paths.
//
// The result depends only on the role each chamber gets, so two sets of
// grants that give every chamber the same role return equal results.
func (c *Config) RolesFor(provider, email string, githubOrgs []string) (Role, []PathRole) {
	vouched := email != "" && c.vouchesFor(provider, email)
	var granted Principal
	var onPaths []PathRole
	for _, g := range c.grants {
		if g.role <= granted.Role {
			// grants come highest role first: none of the rest adds anything
			break
		}
		if !g.grant.matches(email, vouched, githubOrgs) {
			continue
		}
		if g.grant.Paths == nil {
			granted.Role = g.role
		}
		for _, p := range g.grant.Paths {
			onPaths = append(onPaths, PathRole{Path: p, Role: g.role})
		}
	}
	// Sorted by path, highest role first, each entry comes after the entries
	// for the paths above it: keep those that raise the role on their path.
	slices.SortFunc(onPaths, func(a, b PathRole) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(b.Role, a.Role))
	})
	for _, e := range onPaths {
		if e.Role > granted.RoleAt(e.Path) {
			granted.Paths = append(granted.Paths, e)
		}
	}
	return granted.Role, granted.Paths
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
