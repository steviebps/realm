package http

import (
	"errors"
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
		info := requestInfoFrom(ctx)
		if authn == nil {
			anon := *auth.Anonymous
			if actor := strings.TrimSpace(r.Header.Get(ActorHeader)); actor != "" {
				anon.ID = actor
			}
			if info != nil {
				info.actor = anon.Actor()
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
			if info != nil {
				info.actor = p.Actor()
			}
		}
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdmin refuses callers who are not administrators.
func requireAdmin(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.FromContext(r.Context()).IsAdmin() {
			forbidden(w, r, "only administrators may do this")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter, r *http.Request, message string) {
	noteFailure(r.Context(), http.StatusUnauthorized, errors.New(message))
	w.Header().Set("WWW-Authenticate", `Bearer realm="realm"`)
	handleError(r.Context(), w, http.StatusUnauthorized, createResponseWithErrors(nil, []string{message}))
}

func forbidden(w http.ResponseWriter, r *http.Request, message string) {
	noteFailure(r.Context(), http.StatusForbidden, errors.New(message))
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

// handleKeys manages API keys. Register it behind requireAdmin:
//
//	GET    /v1/keys       → every key (without secrets)
//	POST   /v1/keys       → {"name", "access": "read"|"write", "path"} creates a key
//	DELETE /v1/keys/<id>  → revokes a key
func handleKeys(keys *auth.KeyStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		fail := func(status int, err error) {
			noteFailure(ctx, status, err)
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
			handleOkJSON(ctx, w, list)

		case r.Method == http.MethodPost && id == "":
			var req auth.NewKeyRequest
			if err := utils.ReadInterfaceWith(r.Body, &req); err != nil {
				fail(http.StatusBadRequest, err)
				return
			}
			key, token, err := keys.Create(ctx, req, auth.FromContext(ctx).Actor())
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
			if err := keys.Revoke(ctx, id); err != nil {
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
