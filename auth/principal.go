// Package auth authenticates callers of the realm server and decides what
// each may do. Applications and automation present API keys; people using
// the UI sign in through an OIDC provider or GitHub and receive a role.
package auth

import (
	"context"
	"fmt"
	"strings"
)

// Role is the level of access granted to a signed-in user. Higher roles
// include everything the lower roles may do.
type Role int

const (
	// RoleNone grants nothing; the user may not use this realm.
	RoleNone Role = iota
	// RoleViewer may read chambers, history, and the catalog.
	RoleViewer
	// RoleEditor may also change chambers.
	RoleEditor
	// RoleAdmin may also manage API keys and the attribute catalog.
	RoleAdmin
)

var roleNames = [...]string{"none", "viewer", "editor", "admin"}

func (r Role) String() string {
	if r < 0 || int(r) >= len(roleNames) {
		return fmt.Sprintf("Role(%d)", int(r))
	}
	return roleNames[r]
}

func (r Role) MarshalText() ([]byte, error) {
	return []byte(r.String()), nil
}

func (r *Role) UnmarshalText(b []byte) error {
	for i, name := range roleNames {
		if string(b) == name {
			*r = Role(i)
			return nil
		}
	}
	return fmt.Errorf("unknown role %q", b)
}

// Access is what an API key may do within its path.
type Access string

const (
	// AccessRead allows reading and watching chambers and their history.
	AccessRead Access = "read"
	// AccessWrite also allows creating, changing, and deleting chambers.
	AccessWrite Access = "write"
)

func (a Access) valid() bool {
	return a == AccessRead || a == AccessWrite
}

// Kind identifies how a principal authenticated.
type Kind string

const (
	KindUser Kind = "user"
	KindKey  Kind = "key"
	// KindAnonymous is used for every request when authentication is not
	// configured. It may do anything.
	KindAnonymous Kind = "anonymous"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	Kind Kind `json:"kind"`
	// ID is the user's email address or the API key's ID.
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Provider is the sign-in provider of a user.
	Provider string `json:"provider,omitempty"`
	// Role applies to users (and is RoleAdmin for anonymous callers).
	Role Role `json:"role"`
	// Access and Path apply to API keys: the key may act only on chambers
	// at or beneath Path.
	Access Access `json:"access,omitempty"`
	Path   string `json:"path,omitempty"`
}

// Anonymous is the principal of every request when authentication is off.
var Anonymous = &Principal{Kind: KindAnonymous, ID: "anonymous", Role: RoleAdmin}

// CanRead reports whether p may read the chamber at path (and its history).
func (p *Principal) CanRead(path string) bool {
	if p == nil {
		return false
	}
	if p.Kind == KindKey {
		return p.Access.valid() && covers(p.Path, path)
	}
	return p.Role >= RoleViewer
}

// CanWrite reports whether p may create, change, or delete the chamber at path.
func (p *Principal) CanWrite(path string) bool {
	if p == nil {
		return false
	}
	if p.Kind == KindKey {
		return p.Access == AccessWrite && covers(p.Path, path)
	}
	return p.Role >= RoleEditor
}

// IsAdmin reports whether p may manage API keys and the catalog. API keys are
// never administrators.
func (p *Principal) IsAdmin() bool {
	return p != nil && p.Kind != KindKey && p.Role >= RoleAdmin
}

// Actor is how p is recorded in change history.
func (p *Principal) Actor() string {
	if p.Kind == KindKey {
		return "key:" + p.Name
	}
	return p.ID
}

// NormalizePath returns path with a leading and trailing slash, the form
// chamber paths and key scopes are compared in.
func NormalizePath(path string) string {
	path = "/" + strings.Trim(path, "/") + "/"
	if path == "//" {
		return "/"
	}
	return path
}

// covers reports whether the scope prefix includes path.
func covers(prefix, path string) bool {
	return strings.HasPrefix(NormalizePath(path), NormalizePath(prefix))
}

type principalKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal carried by ctx, or nil.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}
