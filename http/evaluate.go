package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/helper/logging"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// evaluatePrefix is the route of the evaluation endpoint.
const evaluatePrefix = "/v1/evaluate/"

// maxEvaluateBody bounds the size of an evaluation context.
const maxEvaluateBody = 64 << 10

// evaluateRequest is the context flags are evaluated for.
type evaluateRequest struct {
	Key        string            `json:"key"`
	Attributes map[string]string `json:"attributes"`
}

// evaluateResponse holds the value each flag resolves to, without its rules.
type evaluateResponse struct {
	Flags map[string]realm.EvaluatedFlag `json:"flags"`
}

// handleEvaluate evaluates every flag of a chamber, including inherited ones,
// for the context in the request body:
//
//	POST /v1/evaluate/<path>             → {"flags": {"<name>": {"type", "value"}}}
//	POST /v1/evaluate/<path>?watch=true  → the same as server-sent events, re-sent when the values change
func handleEvaluate(strg storage.Storage, brk *broker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		path := logicalPath(r, strings.TrimSuffix(evaluatePrefix, "/"))
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.String("realm.server.logicalPath", path))
		fail := func(status int, err error) {
			logging.Ctx(ctx).ErrorCtx(ctx).Str("method", r.Method).Str("path", r.URL.Path).Msg(err.Error())
			handleError(ctx, w, status, createResponseWithErrors(nil, []string{err.Error()}))
		}

		if r.Method != http.MethodPost {
			fail(http.StatusMethodNotAllowed, errors.New(http.StatusText(http.StatusMethodNotAllowed)))
			return
		}
		if storage.IsReservedPath(path) {
			fail(http.StatusBadRequest, fmt.Errorf("paths under %q are reserved", storage.ReservedPrefix))
			return
		}
		if !auth.FromContext(ctx).CanEvaluate(path) {
			forbidden(w, r, fmt.Sprintf("not allowed to evaluate %s", path))
			return
		}

		var req evaluateRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEvaluateBody)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			fail(http.StatusBadRequest, fmt.Errorf("invalid evaluation context: %w", err))
			return
		}
		ec := realm.EvaluationContext{Key: req.Key, Attributes: req.Attributes}

		if isWatchRequest(r) {
			streamEvents(w, r, brk, path, "flags", func(ctx context.Context) ([]byte, error) {
				flags, err := evaluate(ctx, strg, path, ec)
				if err != nil {
					return nil, err
				}
				return json.Marshal(evaluateResponse{Flags: flags})
			})
			return
		}

		flags, err := evaluate(ctx, strg, path, ec)
		if err != nil {
			var nf *storage.NotFoundError
			if errors.As(err, &nf) {
				fail(http.StatusNotFound, nf)
				return
			}
			fail(http.StatusInternalServerError, err)
			return
		}
		handleOkJSON(ctx, w, evaluateResponse{Flags: flags})
	})
}

// evaluate resolves every flag of the chamber at path (as inherited) for ec.
func evaluate(ctx context.Context, strg storage.Storage, path string, ec realm.EvaluationContext) (map[string]realm.EvaluatedFlag, error) {
	entry, err := strg.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var c realm.Chamber
	if err := json.Unmarshal(entry.Value, &c); err != nil {
		return nil, fmt.Errorf("could not read chamber %s: %w", path, err)
	}
	return c.Evaluate(ec), nil
}

// withCORS lets pages on any origin call the routes under prefix with a
// bearer token (an evaluate key). Credentials are never allowed cross-origin,
// so sessions cannot be used from other sites. Preflight requests are
// answered before authentication, since browsers send them without
// credentials.
func withCORS(prefix string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", "POST")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
