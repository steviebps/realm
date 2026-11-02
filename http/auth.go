package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
)

// withPrincipal identifies the caller of every request and stores it in the
// request context. When authentication is not configured (authn is nil) every
// caller is auth.Anonymous. Invalid credentials are refused here; requests
// without credentials continue, and protected routes refuse them with
// requireAuth.
func withPrincipal(authn *auth.Authenticator, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if authn == nil {
			h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, auth.Anonymous)))
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
		if p != nil {
			ctx = auth.WithPrincipal(ctx, p)
		}
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAuth refuses requests that carry no valid credentials.
func requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.FromContext(r.Context()) == nil {
			unauthorized(w, r, "sign in or present an API key")
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

// meResponse tells the UI whether sign-in is required, who is signed in, and
// how to sign in.
type meResponse struct {
	AuthEnabled bool                `json:"authEnabled"`
	User        *auth.Principal     `json:"user"`
	Providers   []auth.ProviderInfo `json:"providers"`
}

// handleMe serves GET /v1/auth/me. It never requires credentials.
func handleMe(authn *auth.Authenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			handleError(r.Context(), w, http.StatusMethodNotAllowed, createResponseWithErrors(nil, []string{http.StatusText(http.StatusMethodNotAllowed)}))
			return
		}
		resp := meResponse{Providers: []auth.ProviderInfo{}}
		if authn != nil {
			resp.AuthEnabled = true
			resp.User = auth.FromContext(r.Context())
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

// handleKeys manages API keys. Only administrators who signed in may use it:
//
//	GET    /v1/keys       → every key (without secrets)
//	POST   /v1/keys       → {"name", "access": "read"|"write", "path"} creates a key
//	DELETE /v1/keys/<id>  → revokes a key
func handleKeys(keys *auth.KeyStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		p := auth.FromContext(ctx)
		if !p.IsAdmin() {
			forbidden(w, r, "only administrators may manage API keys")
			return
		}
		fail := func(status int, err error) {
			logging.Ctx(ctx).ErrorCtx(ctx).Str("method", r.Method).Str("path", r.URL.Path).Msg(err.Error())
			handleError(ctx, w, status, createResponseWithErrors(nil, []string{err.Error()}))
		}

		id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/keys"), "/")
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
			if err := req.Validate(); err != nil {
				fail(http.StatusBadRequest, err)
				return
			}
			key, token, err := keys.Create(ctx, req, p.Actor())
			if err != nil {
				fail(http.StatusInternalServerError, err)
				return
			}
			raw, err := json.Marshal(createdKey{Key: key, Token: token})
			if err != nil {
				fail(http.StatusInternalServerError, err)
				return
			}
			handleWithStatus(w, http.StatusCreated, createResponseWithErrors(raw, nil))

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
