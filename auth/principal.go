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
	// AccessEvaluate allows only evaluating flags for a context. Evaluate keys
	// are meant for browsers: they reveal values, never the rules behind them.
	AccessEvaluate Access = "evaluate"
	// AccessRead allows reading and watching chambers and their history.
	AccessRead Access = "read"
	// AccessWrite also allows creating, changing, and deleting chambers.
	AccessWrite Access = "write"
)

// accessRank orders access levels: each includes everything below it.
var accessRank = map[Access]int{AccessEvaluate: 1, AccessRead: 2, AccessWrite: 3}

func (a Access) valid() bool {
	return accessRank[a] > 0
}

// atLeast reports whether a is valid and includes min.
func (a Access) atLeast(min Access) bool {
	return a.valid() && accessRank[a] >= accessRank[min]
}

// Kind identifies how a principal authenticated.
type Kind string

const (
	KindUser Kind = "user"
	KindKey  Kind = "key"
	// KindAdminKey is the admin key declared in the server config
	// (Config.AdminKey). It acts with RoleAdmin.
	KindAdminKey Kind = "adminKey"
	// KindAnonymous is used for every request when authentication is not
	// configured. It may do anything.
	KindAnonymous Kind = "anonymous"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	Kind Kind `json:"kind"`
	// ID is the user's email address, the API key's ID, or "admin" for the
	// admin key.
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Provider is the sign-in provider of a user.
	Provider string `json:"provider,omitempty"`
	// Role applies to users (and is RoleAdmin for the admin key and for
	// anonymous callers).
	Role Role `json:"role"`
	// Access and Path apply to API keys: the key may act only on chambers
	// at or beneath Path.
	Access Access `json:"access,omitempty"`
	Path   string `json:"path,omitempty"`
}

// Anonymous is the principal of every request when authentication is off.
var Anonymous = &Principal{Kind: KindAnonymous, ID: "anonymous", Role: RoleAdmin}

// adminKeyPrincipal is the principal of requests presenting the admin key.
var adminKeyPrincipal = Principal{Kind: KindAdminKey, ID: "admin", Name: "admin key", Role: RoleAdmin}

// CanEvaluate reports whether p may evaluate the flags of the chamber at path
// for a context.
func (p *Principal) CanEvaluate(path string) bool {
	return p.allows(AccessEvaluate, RoleViewer, path)
}

// CanRead reports whether p may read the chamber at path (and its history).
func (p *Principal) CanRead(path string) bool {
	return p.allows(AccessRead, RoleViewer, path)
}

// CanWrite reports whether p may create, change, or delete the chamber at path.
func (p *Principal) CanWrite(path string) bool {
	return p.allows(AccessWrite, RoleEditor, path)
}

// CanReadCatalog reports whether p may read the attribute catalog, which maps
// attribute values to labels. The catalog belongs to the whole realm rather
// than to a chamber, and its labels can name every tenant or location, so a
// key needs read access to the root path: keys scoped beneath it, and
// evaluate keys (which are public), may not read it.
func (p *Principal) CanReadCatalog() bool {
	return p.allows(AccessRead, RoleViewer, "/")
}

// allows reports whether p has at least access (for keys, at path) or role
// (for users).
func (p *Principal) allows(access Access, role Role, path string) bool {
	if p == nil {
		return false
	}
	if p.Kind == KindKey {
		return p.Access.atLeast(access) && covers(p.Path, path)
	}
	return p.Role >= role
}

// IsAdmin reports whether p may manage API keys and the catalog. Stored API
// keys are never administrators; the admin key declared in the config is.
func (p *Principal) IsAdmin() bool {
	return p != nil && p.Kind != KindKey && p.Role >= RoleAdmin
}

// IsSecretKey reports whether p presented a key that must never reach a
// browser: a read or write key, or the admin key.
func (p *Principal) IsSecretKey() bool {
	return p != nil && (p.Kind == KindAdminKey || (p.Kind == KindKey && p.Access != AccessEvaluate))
}

// Actor is how p is recorded in change history.
func (p *Principal) Actor() string {
	switch p.Kind {
	case KindKey:
		return "key:" + p.Name
	case KindAdminKey:
		return "key:admin"
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
