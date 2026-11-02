package auth

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// DefaultSessionTTL is how long a UI sign-in lasts.
const DefaultSessionTTL = 12 * time.Hour

// Config enables authentication on the server. Without it every request is
// allowed. Secrets may reference environment variables as ${NAME}.
//
//	"auth": {
//	  "externalURL": "https://realm.example.com",
//	  "sessionSecret": "${REALM_SESSION_SECRET}",
//	  "access": {
//	    "admins":  {"emails": ["owner@example.com"]},
//	    "editors": {"domains": ["example.com"]},
//	    "viewers": {"githubOrgs": ["example-org"]}
//	  },
//	  "providers": [
//	    {"name": "google", "type": "oidc", "issuer": "https://accounts.google.com",
//	     "clientId": "…", "clientSecret": "${GOOGLE_CLIENT_SECRET}"},
//	    {"name": "github", "type": "github", "clientId": "…", "clientSecret": "${GITHUB_CLIENT_SECRET}"}
//	  ]
//	}
type Config struct {
	// ExternalURL is the address users reach the server at; sign-in
	// callbacks are registered with providers under it.
	ExternalURL string `json:"externalURL"`
	// SessionSecret signs and encrypts session cookies. It must be at least
	// 32 characters and shared by every replica.
	SessionSecret string `json:"sessionSecret"`
	// SessionTTL is a Go duration such as "12h"; DefaultSessionTTL if empty.
	SessionTTL string           `json:"sessionTTL,omitempty"`
	Access     AccessConfig     `json:"access"`
	Providers  []ProviderConfig `json:"providers"`

	sessionTTL time.Duration
	external   *url.URL
}

// AccessConfig grants roles to signed-in users. A user receives the highest
// role any grant matches; users matching none are refused.
type AccessConfig struct {
	Admins  Grant `json:"admins"`
	Editors Grant `json:"editors"`
	Viewers Grant `json:"viewers"`
}

// Grant matches users by verified email address, email domain, or GitHub
// organization membership (GitHub sign-ins only).
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
}

const (
	ProviderOIDC   = "oidc"
	ProviderGitHub = "github"
)

// validate checks the configuration, expands environment references in
// secrets, and fills defaults.
func (c *Config) validate() error {
	var err error
	if c.external, err = url.Parse(strings.TrimSuffix(c.ExternalURL, "/")); err != nil || c.external.Host == "" ||
		(c.external.Scheme != "https" && c.external.Scheme != "http") {
		return errors.New("auth.externalURL must be an absolute http(s) URL")
	}

	c.SessionSecret = os.ExpandEnv(c.SessionSecret)
	if len(c.SessionSecret) < 32 {
		return errors.New("auth.sessionSecret must be at least 32 characters")
	}

	c.sessionTTL = DefaultSessionTTL
	if c.SessionTTL != "" {
		if c.sessionTTL, err = time.ParseDuration(c.SessionTTL); err != nil || c.sessionTTL <= 0 {
			return fmt.Errorf("auth.sessionTTL must be a positive duration: %q", c.SessionTTL)
		}
	}

	if len(c.Providers) == 0 {
		return errors.New("auth.providers must list at least one sign-in provider")
	}
	names := map[string]bool{}
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
		p.ClientSecret = os.ExpandEnv(p.ClientSecret)
		if p.ClientID == "" || p.ClientSecret == "" {
			return fmt.Errorf("auth provider %q needs a clientId and clientSecret", p.Name)
		}
		switch p.Type {
		case ProviderOIDC:
			if p.Issuer == "" {
				return fmt.Errorf("auth provider %q needs an issuer", p.Name)
			}
		case ProviderGitHub:
		default:
			return fmt.Errorf("auth provider %q has unknown type %q (want %q or %q)", p.Name, p.Type, ProviderOIDC, ProviderGitHub)
		}
	}
	return nil
}

// RoleFor returns the highest role granted to a user.
func (a *AccessConfig) RoleFor(email string, githubOrgs []string) Role {
	switch {
	case a.Admins.matches(email, githubOrgs):
		return RoleAdmin
	case a.Editors.matches(email, githubOrgs):
		return RoleEditor
	case a.Viewers.matches(email, githubOrgs):
		return RoleViewer
	}
	return RoleNone
}

func (g *Grant) matches(email string, githubOrgs []string) bool {
	if email != "" {
		for _, e := range g.Emails {
			if strings.EqualFold(e, email) {
				return true
			}
		}
		if at := strings.LastIndexByte(email, '@'); at >= 0 {
			domain := email[at+1:]
			for _, d := range g.Domains {
				if strings.EqualFold(strings.TrimPrefix(d, "@"), domain) {
					return true
				}
			}
		}
	}
	return slices.ContainsFunc(g.GitHubOrgs, func(org string) bool {
		return slices.ContainsFunc(githubOrgs, func(member string) bool { return strings.EqualFold(org, member) })
	})
}
