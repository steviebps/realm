package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

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
//	                                       (after a write, or when a targeting rule's schedule starts or ends)
func handleEvaluate(strg storage.Storage, brk *broker) http.Handler {
	chambers := &parsedChambers{strg: strg}
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
		p := auth.FromContext(ctx)
		if !p.CanEvaluate(path) {
			forbidden(w, r, fmt.Sprintf("not allowed to evaluate %s", path))
			return
		}
		// Browsers send Origin with every POST, and anyone can copy a key out
		// of front-end code. Only evaluate keys are safe to publish, so refuse
		// read and write keys and the admin key here, while shipping one is
		// still a mistake in development rather than a leak.
		if p.IsSecretKey() && r.Header.Get("Origin") != "" {
			msg := string(p.Access) + " keys must not be used in browsers, where anyone can copy them; use an evaluate key"
			if p.Kind == auth.KindAdminKey {
				msg = "the admin key must not be used in browsers, where anyone can copy it; use an evaluate key"
			}
			forbidden(w, r, msg)
			return
		}

		var req evaluateRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEvaluateBody)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			fail(http.StatusBadRequest, fmt.Errorf("invalid evaluation context: %w", err))
			return
		}
		ec := realm.EvaluationContext{Key: req.Key, Attributes: req.Attributes}

		if isWatchRequest(r) {
			streamEvents(w, r, brk, path, "flags", func(ctx context.Context) ([]byte, time.Time, error) {
				flags, changesAt, err := chambers.evaluate(ctx, path, ec)
				if err != nil {
					return nil, time.Time{}, err
				}
				data, err := json.Marshal(evaluateResponse{Flags: flags})
				return data, changesAt, err
			})
			return
		}

		flags, _, err := chambers.evaluate(ctx, path, ec)
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

// parsedChambers evaluates chambers, keeping the last parsed version of each
// path so the evaluation streams on a path share one parse per change rather
// than each re-parsing the chamber on every write.
type parsedChambers struct {
	strg storage.Storage
	mu   sync.Mutex
	last map[string]parsedChamber
}

type parsedChamber struct {
	raw     []byte
	chamber *realm.Chamber
}

// evaluate resolves every flag of the chamber at path (as inherited) for ec.
// It also returns the next time a schedule could change those values, or the
// zero time when none can.
func (p *parsedChambers) evaluate(ctx context.Context, path string, ec realm.EvaluationContext) (map[string]realm.EvaluatedFlag, time.Time, error) {
	entry, err := p.strg.Get(ctx, path)
	if err != nil {
		return nil, time.Time{}, err
	}
	c, err := p.parse(path, entry.Value)
	if err != nil {
		return nil, time.Time{}, err
	}
	flags, changesAt := c.Evaluate(ec)
	return flags, changesAt, nil
}

// parse returns the chamber stored as raw, reusing the last parse of path when
// raw is unchanged. Parsed chambers are only read, so they are safe to share.
func (p *parsedChambers) parse(path string, raw []byte) (*realm.Chamber, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if last, ok := p.last[path]; ok && bytes.Equal(last.raw, raw) {
		return last.chamber, nil
	}
	var c realm.Chamber
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("could not read chamber %s: %w", path, err)
	}
	if p.last == nil {
		p.last = make(map[string]parsedChamber)
	}
	p.last[path] = parsedChamber{raw: bytes.Clone(raw), chamber: &c}
	return &c, nil
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
