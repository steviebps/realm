package http

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
)

// publicPaths need no credentials: liveness, readiness, the UI's static assets, and
// sign-in itself. Every other route refuses requests without them. A path
// ending in "/" also covers the path without it, so "/ui" reaches the
// router's redirect to "/ui/".
var publicPaths = []string{"/healthz", "/readyz", "/ui/", "/v1/auth/"}

func isPublic(path string) bool {
	return slices.ContainsFunc(publicPaths, func(p string) bool {
		return strings.HasPrefix(path, p) || path == strings.TrimSuffix(p, "/")
	})
}

// withPrincipal identifies the caller of every request and stores it in the
// request context. When authentication is not configured (authn is nil) every
// caller is anonymous, named by the self-reported ActorHeader. Otherwise
// invalid credentials are refused, and so are missing ones outside publicPaths.
func withPrincipal(authn *auth.Authenticator, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if authn == nil {
			anon := *auth.Anonymous
			if actor := strings.TrimSpace(r.Header.Get(ActorHeader)); actor != "" {
				anon.ID = actor
			}
			h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, &anon)))
			return
		}
		p, err := authn.Authenticate(r)
		switch {
		case errors.Is(err, auth.ErrInvalidKey):
			unauthorized(w, r, err.Error())
			return
		case errors.Is(err, auth.ErrCrossOrigin):
			handleError(ctx, w, http.StatusForbidden, createResponseWithErrors(nil, []string{err.Error()}))
			return
		case err != nil:
			logging.Ctx(ctx).ErrorCtx(ctx).Msg(err.Error())
			handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{http.StatusText(http.StatusInternalServerError)}))
			return
		}
		if p == nil {
			if !isPublic(r.URL.Path) {
				unauthorized(w, r, "sign in or present an API key")
				return
			}
		} else {
			ctx = auth.WithPrincipal(ctx, p)
		}
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireKeyManager refuses callers who may not manage any API keys: users
// below admin and keys below admin access. handleKeys narrows the rest to the
// keys within each caller's scope.
func requireKeyManager(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.FromContext(r.Context()).ManagesKeys() {
			forbidden(w, r, "only administrators and admin keys may manage API keys")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter, r *http.Request, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="realm"`)
	handleError(r.Context(), w, http.StatusUnauthorized, createResponseWithErrors(nil, []string{message}))
}

func forbidden(w http.ResponseWriter, r *http.Request, message string) {
	handleError(r.Context(), w, http.StatusForbidden, createResponseWithErrors(nil, []string{message}))
}

// meResponse tells the UI whether sign-in is required, who is signed in,
// what they may do, and how to sign in.
type meResponse struct {
	AuthEnabled bool                `json:"authEnabled"`
	User        *auth.Principal     `json:"user"`
	CanEdit     bool                `json:"canEdit"`
	IsAdmin     bool                `json:"isAdmin"`
	Providers   []auth.ProviderInfo `json:"providers"`
}

// handleMe serves GET /v1/auth/me. It never requires credentials.
func handleMe(authn *auth.Authenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			handleError(r.Context(), w, http.StatusMethodNotAllowed, createResponseWithErrors(nil, []string{http.StatusText(http.StatusMethodNotAllowed)}))
			return
		}
		p := auth.FromContext(r.Context())
		resp := meResponse{CanEdit: p.CanWrite("/"), IsAdmin: p.IsAdmin(), Providers: []auth.ProviderInfo{}}
		if authn != nil {
			resp.AuthEnabled = true
			resp.User = p
			resp.Providers = authn.Providers()
		}
		handleOkJSON(r.Context(), w, resp)
	})
}

// createdKey is the response to creating a key; Token is shown only once.
type createdKey struct {
	Key   auth.Key `json:"key"`
	Token string   `json:"token"`
}

// handleKeys manages API keys. Register it behind requireKeyManager. Each
// caller sees and changes only the keys at or beneath the paths where it may
// manage keys: everything for signed-in admins, and an admin key's own path
// for admin keys.
//
//	GET    /v1/keys       → the caller's keys (without secrets)
//	POST   /v1/keys       → {"name", "access": "evaluate"|"read"|"write"|"admin", "path"} creates a key
//	DELETE /v1/keys/<id>  → revokes a key
func handleKeys(keys *auth.KeyStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		p := auth.FromContext(ctx)
		fail := func(status int, err error) {
			logging.Ctx(ctx).ErrorCtx(ctx).Str("method", r.Method).Str("path", r.URL.Path).Msg(err.Error())
			handleError(ctx, w, status, createResponseWithErrors(nil, []string{err.Error()}))
		}

		id := routeParam(r, "/v1/keys")
		switch {
		case r.Method == http.MethodGet && id == "":
			list, err := keys.List(ctx)
			if err != nil {
				fail(http.StatusInternalServerError, err)
				return
			}
			list = slices.DeleteFunc(list, func(k auth.Key) bool { return !p.CanManageKeys(k.Path) })
			handleOkJSON(ctx, w, list)

		case r.Method == http.MethodPost && id == "":
			var req auth.NewKeyRequest
			if err := utils.ReadInterfaceWith(r.Body, &req); err != nil {
				fail(http.StatusBadRequest, err)
				return
			}
			if err := req.Validate(); err != nil {
				fail(http.StatusBadRequest, err)
				return
			}
			if !p.CanManageKeys(req.Path) {
				forbidden(w, r, fmt.Sprintf("this key may only create keys at or beneath %s", p.Path))
				return
			}
			key, token, err := keys.Create(ctx, req, p.Actor())
			if errors.Is(err, auth.ErrInvalidKeyRequest) {
				fail(http.StatusBadRequest, err)
				return
			}
			if err != nil {
				fail(http.StatusInternalServerError, err)
				return
			}
			handleJSON(ctx, w, http.StatusCreated, createdKey{Key: key, Token: token})

		case r.Method == http.MethodDelete && id != "":
			if err := keys.RevokeFor(ctx, id, p); err != nil {
				var nf *storage.NotFoundError
				if errors.As(err, &nf) {
					fail(http.StatusNotFound, err)
					return
				}
				fail(http.StatusInternalServerError, err)
				return
			}
			handleOk(w, nil)

		default:
			fail(http.StatusMethodNotAllowed, errors.New(http.StatusText(http.StatusMethodNotAllowed)))
		}
	})
}
