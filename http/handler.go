package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/helper/logging"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
)

const DefaultHandlerTimeout = 10 * time.Second

var uiExists = true

var meter = otel.Meter("github.com/steviebps/realm")

var errorCounter, _ = meter.Int64Counter(
	"handler.error.counter",
	metric.WithDescription("Number of Error Responses."),
	metric.WithUnit("{call}"))

type HandlerConfig struct {
	Storage        storage.Storage
	RequestTimeout time.Duration
	// Auth enables authentication. When nil every request is allowed.
	Auth *auth.Config
	// Draining, once closed, makes GET /readyz report 503 so load balancers
	// stop routing new requests here before the server shuts down.
	Draining <-chan struct{}
	// MaxStreams caps the watch (SSE) streams this server holds open at once;
	// beyond it, new streams get 503 and clients retry with backoff. Zero
	// means DefaultMaxStreams.
	MaxStreams int
	// MaxSubtreeChambers caps the chambers below the root of a subtree read
	// or watch (?subtree=true); a larger subtree is refused with 413. Zero
	// means DefaultMaxSubtreeChambers.
	MaxSubtreeChambers int
}

// DefaultMaxStreams is the default cap on concurrent watch streams per server.
const DefaultMaxStreams = 10000

// maxRequestBody bounds every request body (evaluation has a smaller limit).
const maxRequestBody = 4 << 20

func RealmHandler(rlm *realm.Realm, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := r.WithContext(rlm.NewContext(r.Context()))
		h.ServeHTTP(w, req)
	})
}

func NewHandler(ctx context.Context, config HandlerConfig) (http.Handler, error) {
	if config.Storage == nil {
		return nil, fmt.Errorf("storage cannot be nil")
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = DefaultHandlerTimeout
	}
	if config.MaxStreams <= 0 {
		config.MaxStreams = DefaultMaxStreams
	}
	if config.MaxSubtreeChambers <= 0 {
		config.MaxSubtreeChambers = DefaultMaxSubtreeChambers
	}
	var authn *auth.Authenticator
	if config.Auth != nil {
		var err error
		if authn, err = auth.New(*config.Auth, storage.Raw(config.Storage)); err != nil {
			return nil, err
		}
	}
	return handle(ctx, config, authn), nil
}

func handle(ctx context.Context, hc HandlerConfig, authn *auth.Authenticator) http.Handler {
	logger := logging.Ctx(ctx)
	mux := http.NewServeMux()
	brk := newBroker(hc.MaxStreams)

	if uiExists {
		mux.Handle("/ui/", gziphandler.GzipHandler(http.StripPrefix("/ui/", uiHandler(uiFiles()))))
	} else {
		mux.Handle("/ui/", handleUIEmpty())
	}

	meta := &metaStore{strg: storage.Raw(hc.Storage)}

	// A backend that reports every change, including other servers' writes,
	// drives the broker; otherwise the handler notifies it after each write.
	notify := brk.Notify
	if feed, ok := meta.strg.(storage.Watchable); ok {
		notify = func(string) {}
		go func() {
			for c := range feed.Watch(ctx) {
				// metadata keys never match a chamber watcher
				if !storage.IsReservedPath(c.Key) {
					brk.Notify(c.Key)
				}
			}
		}()
	}

	streamReads := &sharedReads{strg: hc.Storage, brk: brk, timeout: hc.RequestTimeout}
	trees := &subtrees{strg: hc.Storage, views: newSharedViews(hc.Storage, brk, hc.RequestTimeout), brk: brk, limit: hc.MaxSubtreeChambers}
	mux.Handle("/v1/chambers/", handleChambers(hc.Storage, meta, brk, streamReads, trees, notify))
	mux.Handle("/v1/evaluate/", handleEvaluate(hc.Storage, brk, streamReads))
	mux.Handle("/v1/history/", handleHistory(meta))
	mux.Handle("/v1/catalog", handleCatalog(meta))
	mux.Handle("/v1/catalog/", handleCatalog(meta))
	mux.Handle("/v1/auth/me", handleMe(authn))
	if authn != nil {
		mux.Handle("/v1/auth/", authn.Handler())
		keys := requireAdmin(handleKeys(authn.Keys()))
		mux.Handle("/v1/keys", keys)
		mux.Handle("/v1/keys/", keys)
	}
	// Liveness for orchestrators (e.g. Kubernetes probes); deliberately not
	// traced (see traced) so frequent probes do not flood trace exports. It is
	// public (see publicPaths).
	mux.HandleFunc("GET /healthz", handleHealth)
	// Readiness: storage answers and the server is not draining. Also public
	// and untraced.
	mux.HandleFunc("GET /readyz", handleReady(storage.Raw(hc.Storage), hc.Draining))

	timeoutHandler := wrapWithTimeout(withCORS(evaluatePrefix, withPrincipal(authn, mux)), hc.RequestTimeout)
	// One span per request, around authentication too, so refused requests
	// are traced and measured like the rest.
	instrumented := otelhttp.NewHandler(wrapCommonHandler(timeoutHandler), "realm",
		otelhttp.WithFilter(traced),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + routeOf(r.URL.Path)
		}),
	)
	return withLifecycle(instrumented, logger, hc.RequestTimeout)
}

// routes are the registered route prefixes, used to name spans with low
// cardinality.
var routes = []string{"/v1/chambers/", "/v1/evaluate/", "/v1/history/", "/v1/catalog", "/v1/auth/", "/v1/keys", "/ui/"}

// routeOf returns the route prefix serving path, or "other".
func routeOf(path string) string {
	for _, r := range routes {
		if strings.HasPrefix(path, r) {
			return r
		}
	}
	return "other"
}

// traced reports whether a request gets a span: probes are too frequent to be
// worth one, and a watch stream would hold one open for its whole life.
func traced(r *http.Request) bool {
	return r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && !isWatchRequest(r)
}

func wrapWithTimeout(h http.Handler, t time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Streaming (watch) requests are long-lived and must not be subject to
		// the per-request timeout.
		if isWatchRequest(r) {
			h.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		var cancelFunc context.CancelFunc
		ctx, cancelFunc = context.WithTimeout(ctx, t)
		r = r.WithContext(ctx)
		h.ServeHTTP(w, r)
		cancelFunc()
	})
}

// isWatchRequest reports whether r asks to stream changes (?watch=true) on a
// route that streams. Such requests are exempt from the request timeout.
func isWatchRequest(r *http.Request) bool {
	return r.URL.Query().Get("watch") == "true" &&
		(strings.HasPrefix(r.URL.Path, "/v1/chambers/") || strings.HasPrefix(r.URL.Path, evaluatePrefix))
}

func wrapCommonHandler(next http.Handler) http.Handler {
	meter := otel.Meter("github.com/steviebps/realm")
	apiCounter, _ := meter.Int64Counter(
		"handler.counter",
		metric.WithDescription("Number of API calls."),
		metric.WithUnit("{call}"),
	)

	hostname, _ := os.Hostname()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info := requestInfoFrom(r.Context()); info != nil {
			info.span = trace.SpanContextFromContext(r.Context())
		}
		apiCounter.Add(r.Context(), 1)
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		if r.TLS != nil {
			// served over HTTPS by realm itself: keep browsers on HTTPS
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}

		if hostname != "" {
			w.Header().Set("X-Realm-Hostname", hostname)
		}
		next.ServeHTTP(w, r)
	})
}

func createResponseWithErrors(data json.RawMessage, errors []string) api.HTTPErrorAndDataResponse {
	response := api.HTTPErrorAndDataResponse{}
	if data != nil {
		response.Data = data
	}
	if len(errors) > 0 {
		response.Errors = errors
	}

	return response
}

func handleOk(w http.ResponseWriter, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if body == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusOK)
		_ = utils.WriteInterfaceWith(w, body, true)
	}
}

// handleOkJSON marshals v as the response data, or responds 500 if it cannot.
func handleOkJSON(ctx context.Context, w http.ResponseWriter, v any) {
	handleJSON(ctx, w, http.StatusOK, v)
}

// handleJSON marshals v as the response data with status, or responds 500 if
// it cannot.
func handleJSON(ctx context.Context, w http.ResponseWriter, status int, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
		return
	}
	handleWithStatus(w, status, createResponseWithErrors(raw, nil))
}

func handleWithStatus(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = utils.WriteInterfaceWith(w, body, true)
}

// handleError responds with status and resp. Server errors carry a generic
// message instead of resp's errors, which can hold storage internals; callers
// log the details.
func handleError(ctx context.Context, w http.ResponseWriter, status int, resp api.HTTPErrorAndDataResponse) {
	errorCounter.Add(ctx, 1, metric.WithAttributes(attribute.Int("http.status_code", status)))
	if status >= http.StatusInternalServerError && status != http.StatusServiceUnavailable {
		resp.Errors = []string{http.StatusText(status)}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = utils.WriteInterfaceWith(w, resp, true)
}

// preconditionFailed reports whether the request's If-Match / If-None-Match
// headers reject a write against a resource whose current ETag is current
// ("" when it does not exist).
func preconditionFailed(r *http.Request, current string) bool {
	if h := r.Header.Get("If-Match"); h != "" && !etagMatches(h, current) {
		return true
	}
	if h := r.Header.Get("If-None-Match"); h != "" && etagMatches(h, current) {
		return true
	}
	return false
}

func handleChambers(strg storage.Storage, meta *metaStore, brk *broker, streamReads *sharedReads, trees *subtrees, notify func(path string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		span := trace.SpanFromContext(ctx)

		req := buildAgentRequest(r)
		span.SetAttributes(attribute.String("realm.server.logicalPath", req.Path), attribute.String("realm.server.operation", string(req.Operation)))

		if storage.IsReservedPath(req.Path) {
			handleError(ctx, w, http.StatusBadRequest, createResponseWithErrors(nil, []string{fmt.Sprintf("paths under %q are reserved", storage.ReservedPrefix)}))
			return
		}
		if err := storage.ValidatePath(req.Path); err != nil {
			handleError(ctx, w, http.StatusBadRequest, createResponseWithErrors(nil, []string{err.Error()}))
			return
		}

		fail := func(status int, err error) {
			span.SetStatus(codes.Error, err.Error())
			noteFailure(ctx, status, err)
			handleError(ctx, w, status, createResponseWithErrors(nil, []string{err.Error()}))
		}

		p := auth.FromContext(ctx)
		switch req.Operation {
		case GetOperation, ListOperation:
			if !p.CanRead(req.Path) {
				forbidden(w, r, fmt.Sprintf("not allowed to read %s", req.Path))
				return
			}
		default:
			if !p.CanWrite(req.Path) {
				forbidden(w, r, fmt.Sprintf("not allowed to change %s", req.Path))
				return
			}
		}

		// Writes decode and validate their body before touching storage.
		var body realm.Chamber
		if req.Operation == PutOperation || req.Operation == PatchOperation {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				fail(readFailure(err))
				return
			}
			if len(bytes.TrimSpace(raw)) == 0 {
				fail(http.StatusBadRequest, errors.New("request body must not be empty"))
				return
			}
			parsed, err := realm.ParseChamber(raw)
			if err != nil {
				// surface validation errors (bad types, clauses, unknown or removed fields)
				fail(http.StatusBadRequest, fmt.Errorf("invalid chamber: %w", err))
				return
			}
			body = *parsed
		}

		// commit applies the write and responds with status on success.
		commit := func(status int) {
			// The flags parents seal are read before the write, not with it:
			// a seal that lands meanwhile still hides what this write
			// stores (Lineage.Merge enforces seals; this check explains them).
			var seals map[string]string
			if req.Operation == PutOperation || req.Operation == PatchOperation {
				var err error
				if seals, err = storage.ReadAncestorSeals(ctx, strg, req.Path); err != nil {
					if errors.Is(err, storage.ErrInvalidPath) {
						fail(http.StatusBadRequest, err)
					} else {
						fail(http.StatusInternalServerError, err)
					}
					return
				}
			}
			etag, err := writeChamber(ctx, meta, r, req, &body, seals)
			var pf *preconditionError
			var nf *storage.NotFoundError
			var se *sealedError
			switch {
			case errors.As(err, &se):
				fail(http.StatusBadRequest, se)
			case errors.As(err, &pf):
				if pf.current != "" {
					w.Header().Set("ETag", pf.current)
				}
				handleError(ctx, w, http.StatusPreconditionFailed, createResponseWithErrors(nil, []string{"the chamber was changed by someone else; reload and try again"}))
			case errors.Is(err, errPatchMissing):
				fail(http.StatusBadRequest, fmt.Errorf("%w: %w", err, &storage.NotFoundError{Key: req.Path}))
			case errors.As(err, &nf):
				fail(http.StatusNotFound, nf)
			case errors.Is(err, storage.ErrConflict):
				fail(http.StatusConflict, errors.New("the chamber is being changed by others; try again"))
			case errors.Is(err, storage.ErrInvalidPath):
				// refused before anything was written
				fail(http.StatusBadRequest, err)
			case errors.Is(err, storage.ErrTooLarge):
				// refused before anything was written
				fail(http.StatusRequestEntityTooLarge, err)
			case err != nil:
				// Backends without transactions apply a write's changes one
				// at a time, so part of it may have landed: let streams look.
				notify(req.Path)
				fail(http.StatusInternalServerError, err)
			default:
				notify(req.Path)
				if etag != "" {
					w.Header().Set("ETag", etag)
				}
				if status == http.StatusCreated {
					handleWithStatus(w, status, nil)
				} else {
					handleOk(w, nil)
				}
			}
		}

		switch req.Operation {
		case GetOperation:
			// ?subtree=true returns the view of the path and of every
			// chamber below it, or streams them with watch=true (see
			// subtree.go)
			subtree, ok := subtreeQuery(r, fail)
			if !ok {
				return
			}
			if subtree {
				if isWatchRequest(r) {
					trees.stream(w, r, req.Path, fail)
				} else {
					trees.serve(w, r, req.Path, fail)
				}
				return
			}
			// ?inherit=layers returns the chamber and each parent chamber its
			// inherited view is built from, nearest first (see layers.go).
			if r.URL.Query().Get("inherit") == "layers" {
				serveLayers(w, r, strg, p, req.Path, fail)
				return
			}
			if isWatchRequest(r) {
				streamChamber(w, r, streamReads, brk, req.Path)
				return
			}

			// ?inherit=false returns the chamber exactly as stored at this path,
			// without rules inherited from its parents, plus its ETag (used by
			// editors). The default, inherited view carries no ETag because it
			// is not the representation that writes are checked against.
			own := r.URL.Query().Get("inherit") == "false"
			source := strg
			if own {
				source = meta.strg
			}
			entry, err := source.Get(ctx, req.Path)
			if err != nil {
				var nfError *storage.NotFoundError
				var invalid *storage.ChamberError
				switch {
				case errors.As(err, &nfError):
					fail(http.StatusNotFound, nfError)
				case errors.Is(err, storage.ErrInvalidPath):
					fail(http.StatusBadRequest, err)
				case errors.As(err, &invalid):
					// a stored chamber (this one or a parent) is broken: say
					// which and why, as the layers view does
					span.SetStatus(codes.Error, err.Error())
					noteFailure(ctx, http.StatusInternalServerError, err)
					errorCounter.Add(ctx, 1, metric.WithAttributes(attribute.Int("http.status_code", http.StatusInternalServerError)))
					handleWithStatus(w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				default:
					// an outage is not a missing chamber (which editors would
					// offer to create), and its cause is logged, not sent
					fail(http.StatusInternalServerError, err)
				}
				return
			}

			if own {
				w.Header().Set("ETag", etagOf(entry.Value))
			}
			setRevision(w, entry.Version)
			handleOk(w, createResponseWithErrors(entry.Value, nil))
			return

		case PutOperation:
			commit(http.StatusCreated)
			return

		case PatchOperation:
			commit(http.StatusOK)
			return

		case DeleteOperation:
			recursive, err := queryBool(r, "recursive")
			if err != nil {
				fail(http.StatusBadRequest, err)
				return
			}
			if !recursive {
				commit(http.StatusOK)
				return
			}
			if req.Path == "/" {
				fail(http.StatusBadRequest, errors.New("refusing to recursively delete every chamber; delete the chambers below / instead"))
				return
			}
			deleted, err := deleteChamberTree(ctx, meta, r, req.Path)
			var pf *preconditionError
			var nf *storage.NotFoundError
			switch {
			case errors.As(err, &pf):
				if pf.current != "" {
					w.Header().Set("ETag", pf.current)
				}
				handleError(ctx, w, http.StatusPreconditionFailed, createResponseWithErrors(nil, []string{"the chamber was changed by someone else; reload and try again"}))
			case errors.As(err, &nf):
				fail(http.StatusNotFound, nf)
			case errors.Is(err, storage.ErrConflict):
				fail(http.StatusConflict, errors.New("the chambers are being changed by others; try again"))
			case errors.Is(err, storage.ErrInvalidPath):
				fail(http.StatusBadRequest, err)
			case errors.Is(err, storage.ErrTooLarge):
				fail(http.StatusRequestEntityTooLarge, fmt.Errorf("%w; delete fewer chambers at once", err))
			case err != nil:
				// part of the deletion may have landed (see commit)
				notify(req.Path)
				for _, p := range deleted {
					notify(p)
				}
				fail(http.StatusInternalServerError, err)
			default:
				// each deleted chamber is announced, as a change feed
				// announces each key: subtree streams learn which went
				for _, p := range deleted {
					notify(p)
				}
				handleOkJSON(ctx, w, deleteResult{Deleted: deleted})
			}
			return

		case ListOperation:
			names, err := strg.List(ctx, req.Path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fail(http.StatusNotFound, errors.New(http.StatusText(http.StatusNotFound)))
					return
				}
				if errors.Is(err, storage.ErrInvalidPath) {
					fail(http.StatusBadRequest, err)
					return
				}
				fail(http.StatusInternalServerError, err)
				return
			}
			if req.Path == "/" {
				// hide realm's metadata from the root listing
				names = slices.DeleteFunc(names, isReservedName)
			}
			handleOkJSON(ctx, w, names)
			return

		default:
			span.SetStatus(codes.Error, "method not allowed")
			handleError(ctx, w, http.StatusMethodNotAllowed, createResponseWithErrors(nil, []string{http.StatusText(http.StatusMethodNotAllowed)}))
		}
	})
}

// errPatchMissing is returned when a PATCH targets a chamber that does not exist.
var errPatchMissing = errors.New("cannot patch a resource that does not exist")

// preconditionError is returned when If-Match / If-None-Match reject a write.
// current is the chamber's ETag, or empty when it does not exist.
type preconditionError struct {
	current string
}

func (e *preconditionError) Error() string { return "precondition failed" }

// sealedFlag is a flag a write would define although a parent seals it.
type sealedFlag struct {
	key string
	by  string
}

// sealedError refuses a write that adds or changes flags that parent
// chambers seal.
type sealedError struct {
	flags []sealedFlag
}

func (e *sealedError) Error() string {
	if len(e.flags) == 1 {
		f := e.flags[0]
		return fmt.Sprintf("flag %q is sealed by %s: chambers below it cannot define it", f.key, f.by)
	}
	parts := make([]string, len(e.flags))
	for i, f := range e.flags {
		parts[i] = fmt.Sprintf("%q (by %s)", f.key, f.by)
	}
	return "flags are sealed by parent chambers, which chambers below them cannot define: " + strings.Join(parts, ", ")
}

// checkSeals refuses next, the chamber a write would store, when it adds a
// flag that a parent seals (seals is from AncestorSeals) or changes the
// stored definition of one. A definition stored before the seal may stay as
// it is, hidden, so the chamber can still be edited. stored is the chamber as
// stored, or nil.
func checkSeals(seals map[string]string, stored *storage.StorageEntry, next *realm.Chamber) error {
	if len(seals) == 0 {
		return nil
	}
	var before realm.Chamber
	if stored != nil {
		// a stored chamber that cannot be parsed keeps no definition
		_ = json.Unmarshal(stored.Value, &before)
	}
	var refused []sealedFlag
	for _, key := range slices.Sorted(maps.Keys(next.Rules)) {
		by, ok := seals[key]
		if !ok {
			continue
		}
		if old, had := before.Rules[key]; had && sameFlag(old, next.Rules[key]) {
			continue
		}
		refused = append(refused, sealedFlag{key: key, by: by})
	}
	if len(refused) > 0 {
		return &sealedError{flags: refused}
	}
	return nil
}

// sameFlag reports whether a and b are written the same.
func sameFlag(a, b *realm.Flag) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// writeChamber applies a PUT, PATCH, or DELETE of req.Path and records it in
// the chamber's history, atomically, and returns the chamber's new ETag
// (empty after a delete). Conditional headers are checked against the chamber
// as stored, and a PATCH merges into it rather than its inherited view, so
// inherited rules are never copied into the child. A PUT or PATCH that adds or
// changes a flag in seals (from AncestorSeals) is refused with a sealedError.
func writeChamber(ctx context.Context, meta *metaStore, r *http.Request, req *AgentRequest, body *realm.Chamber, seals map[string]string) (string, error) {
	var etag string
	indexKey := historyIndexKey(req.Path)
	err := storage.Update(ctx, meta.strg, []string{req.Path, indexKey}, func(cur map[string]*storage.StorageEntry) ([]storage.Op, error) {
		stored := cur[req.Path]
		current := ""
		if stored != nil {
			current = etagOf(stored.Value)
		}
		if preconditionFailed(r, current) {
			return nil, &preconditionError{current: current}
		}

		var value []byte
		var err error
		switch req.Operation {
		case PutOperation:
			if err := checkSeals(seals, stored, body); err != nil {
				return nil, err
			}
			value, err = json.Marshal(body)
		case PatchOperation:
			if stored == nil {
				return nil, errPatchMissing
			}
			merged := realm.Chamber{}
			if err := json.Unmarshal(stored.Value, &merged); err != nil {
				return nil, fmt.Errorf("could not unmarshal current chamber while patching: %w", err)
			}
			merged.OverwriteFrom(body)
			if err := checkSeals(seals, stored, &merged); err != nil {
				return nil, err
			}
			value, err = json.Marshal(&merged)
		case DeleteOperation:
			if stored == nil {
				return nil, &storage.NotFoundError{Key: req.Path}
			}
		}
		if err != nil {
			return nil, err
		}

		rev := Revision{Time: time.Now().UTC(), Actor: auth.FromContext(ctx).Actor(), Operation: req.Operation}
		etag = ""
		if value != nil {
			etag = etagOf(value)
			rev.ETag = etag
			rev.Chamber = value
		}
		history, err := historyOps(cur[indexKey], req.Path, rev)
		if err != nil {
			return nil, err
		}
		return append([]storage.Op{{Key: req.Path, Value: value}}, history...), nil
	})
	return etag, err
}

// deleteResult is the response body of a recursive delete.
type deleteResult struct {
	// Deleted lists the chambers removed, sorted.
	Deleted []string `json:"deleted"`
}

// queryBool parses the boolean query parameter name, which is false when
// absent.
func queryBool(r *http.Request, name string) (bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("query parameter %q must be a boolean", name)
	}
	return b, nil
}

// deleteChamberTree deletes the chamber at root and every chamber below it in
// one Update, recording each deletion in that chamber's history, and returns
// the deleted paths, sorted. Conditional headers are checked against root's
// chamber. It returns a NotFoundError when there is no chamber at or below
// root. When the Update fails after deciding what to delete, it returns the
// paths it set out to delete with the error: part of the deletion may have
// landed.
//
// The tree is listed before the Update, so a chamber created below root
// concurrently may survive; deleting again removes it.
func deleteChamberTree(ctx context.Context, meta *metaStore, r *http.Request, root string) ([]string, error) {
	paths, err := storage.ListTree(ctx, meta.strg, root, 0)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, 2*len(paths))
	for _, p := range paths {
		keys = append(keys, p, historyIndexKey(p))
	}

	var deleted []string
	err = storage.Update(ctx, meta.strg, keys, func(cur map[string]*storage.StorageEntry) ([]storage.Op, error) {
		current := ""
		if stored := cur[root]; stored != nil {
			current = etagOf(stored.Value)
		}
		if preconditionFailed(r, current) {
			return nil, &preconditionError{current: current}
		}

		deleted = deleted[:0]
		var ops []storage.Op
		actor := auth.FromContext(ctx).Actor()
		now := time.Now().UTC()
		for _, p := range paths {
			if cur[p] == nil {
				continue // a path that only leads to chambers below it
			}
			history, err := historyOps(cur[historyIndexKey(p)], p, Revision{Time: now, Actor: actor, Operation: DeleteOperation})
			if err != nil {
				return nil, err
			}
			ops = append(ops, storage.Op{Key: p})
			ops = append(ops, history...)
			deleted = append(deleted, p)
		}
		if len(deleted) == 0 {
			return nil, &storage.NotFoundError{Key: root}
		}
		return ops, nil
	})
	slices.Sort(deleted)
	return deleted, err
}

// setRevision sets the revision header (see api.RevisionHeader), unless the
// storage does not track revisions (rev is 0).
func setRevision(w http.ResponseWriter, rev uint64) {
	if rev != 0 {
		w.Header().Set(api.RevisionHeader, strconv.FormatUint(rev, 10))
	}
}

// streamChamber holds an SSE connection open and pushes the chamber at path
// whenever it changes. Clients evaluate the chamber themselves, so its
// schedules need no pushes of their own.
func streamChamber(w http.ResponseWriter, r *http.Request, reads *sharedReads, brk *broker, path string) {
	streamEvents(w, r, brk, path, "chamber", false, func(ctx context.Context) (streamPayload, error) {
		entry, err := reads.Get(ctx, path)
		if err != nil {
			return streamPayload{}, err
		}
		return streamPayload{data: entry.Value, revision: entry.Version}, nil
	})
}

// streamPayload is what a stream sends as one event.
type streamPayload struct {
	data []byte
	// revision is the storage revision data was produced from, sent as the
	// event's id; 0 when the storage does not track revisions.
	revision uint64
	// changesAt is when data changes by itself (a schedule boundary of an
	// evaluated flag); zero when it changes only with writes.
	changesAt time.Time
}

// streamEvents holds an SSE connection open and sends the payload produced by
// current as a single event: once at the start, then again whenever the broker
// signals a change at or above path and the payload differs from the last one
// sent. It emits periodic heartbeats so dead connections and idle
// intermediaries are handled. A payload spanning several lines is sent as
// several data fields (see eventText).
//
// Each event's id is the revision its payload was produced from, when the
// storage tracks revisions. A write that leaves the payload unchanged sends
// nothing, so the last id names the first revision that produced the payload
// the client holds, which may be older than storage's.
//
// current may also return a time at which the payload changes by itself (a
// schedule boundary of an evaluated flag); the payload is produced again then.
//
// A payload whose source does not exist yet is not fatal: the stream waits for
// it to be created. Neither is a failure to produce it (storage unreachable,
// say): the client keeps the payload it has, and it is produced again with
// backoff, so a storage outage does not make every client reconnect at once.
//
// The request must have been read in full: the stream lifts the request's
// read deadline, and bounds each write instead, so a client that stops
// reading loses its stream rather than holding it.
//
// public marks a stream anyone may open, which the broker limits to a share
// of its streams (see broker.publicMax).
func streamEvents(w http.ResponseWriter, r *http.Request, brk *broker, path, event string, public bool, current func(context.Context) (streamPayload, error)) {
	ctx := r.Context()
	logger := logging.Ctx(ctx)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Bool("realm.server.watch", true))

	if !canStream(ctx, w) {
		return
	}

	signals, unsubscribe, ok := brk.Subscribe(path, public)
	if !ok {
		refuseStream(ctx, w)
		return
	}
	defer unsubscribe()

	// Commit the headers now, so clients see the stream open even while
	// they wait for the payload's source to be created.
	conn := startStream(w)
	if conn == nil {
		return
	}
	defer conn.close()

	// recheck fires when the last payload produced changes by itself.
	recheck := time.NewTimer(0)
	recheck.Stop()
	defer recheck.Stop()
	// retry fires when a payload that could not be produced is due to be
	// produced again.
	retry := newStreamBackoff()
	defer retry.stop()

	var last []byte
	// send writes the current payload unless it is unchanged. It returns
	// false when the connection should be torn down.
	send := func() bool {
		payload, err := current(ctx)
		if ctx.Err() != nil {
			return false
		}
		if err != nil {
			var nfError *storage.NotFoundError
			if errors.As(err, &nfError) {
				// wait for it to be created
				retry.failures = 0
				recheck.Stop()
				return true
			}
			if errors.Is(err, storage.ErrInvalidPath) {
				// storage refuses the path: it will never exist
				return false
			}
			delay := retry.fail()
			// the shared read logs the failure itself, once for every stream
			logger.DebugCtx(ctx).Str("path", path).Str("error", err.Error()).Dur("retryIn", delay).Msg("could not produce a stream payload")
			return true
		}
		retry.succeed()
		if payload.changesAt.IsZero() {
			recheck.Stop()
		} else {
			recheck.Reset(time.Until(payload.changesAt))
		}
		if last != nil && bytes.Equal(payload.data, last) {
			return true
		}
		if !conn.write(eventText(event, payload.revision, payload.data)) {
			return false
		}
		last = payload.data
		return true
	}

	if !send() {
		return
	}
	conn.run(ctx, signals, send, retry, send, recheck.C, send)
}

// canStream reports whether w can stream, responding 500 when it cannot.
func canStream(ctx context.Context, w http.ResponseWriter) bool {
	if _, ok := w.(http.Flusher); !ok {
		handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{"streaming is not supported"}))
		return false
	}
	return true
}

// refuseStream responds to a watch request the broker has no room for: the
// client retries later.
func refuseStream(ctx context.Context, w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	handleError(ctx, w, http.StatusServiceUnavailable, createResponseWithErrors(nil, []string{"too many open streams; try again later"}))
}

// streamConn is an open server-sent event stream.
type streamConn struct {
	w  http.ResponseWriter
	rc *http.ResponseController
	// lifetime fires when the stream has been open long enough (see
	// maxStreamLifetime).
	lifetime *time.Timer
}

// startStream starts a server-sent event response on w, once the caller has
// subscribed to the changes it streams. The request must have been read in
// full: the stream lifts the request's read deadline, and bounds each write
// instead, so a client that stops reading loses its stream rather than
// holding it. It returns nil when the client is gone; otherwise the caller
// closes the stream when it ends.
func startStream(w http.ResponseWriter) *streamConn {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	// Streams end after a while and clients reconnect, which re-checks their
	// credentials (a revoked key or expired session loses its stream) and
	// spreads streams over replicas added since.
	c := &streamConn{w: w, rc: rc, lifetime: time.NewTimer(maxStreamLifetime + rand.N(maxStreamLifetime/4))}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if !c.write("") {
		c.close()
		return nil
	}
	return c
}

// close releases the stream's timers.
func (c *streamConn) close() {
	c.lifetime.Stop()
}

// buffer writes s without flushing it, giving the client streamWriteTimeout
// to take what the write sends. It returns false when the stream should end.
func (c *streamConn) buffer(s string) bool {
	_ = c.rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	_, err := io.WriteString(c.w, s)
	return err == nil
}

// flush sends what was buffered. It returns false when the stream should end.
func (c *streamConn) flush() bool {
	_ = c.rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	return c.rc.Flush() == nil
}

// write writes and flushes s. It returns false when the stream should end.
func (c *streamConn) write(s string) bool {
	return c.buffer(s) && c.flush()
}

// run serves the stream until ctx is done, its lifetime ends, or a callback
// returns false: it calls onSignal for each broker signal, onRetry when retry
// is due, and onRecheck when recheck fires (nil never fires), and sends
// heartbeats in between so dead connections and idle intermediaries are
// noticed.
func (c *streamConn) run(ctx context.Context, signals <-chan struct{}, onSignal func() bool, retry *streamBackoff, onRetry func() bool, recheck <-chan time.Time, onRecheck func() bool) {
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		var ok bool
		select {
		case <-ctx.Done():
			return
		case <-c.lifetime.C:
			return
		case <-signals:
			ok = onSignal()
		case <-recheck:
			ok = onRecheck()
		case <-retry.timer.C:
			ok = onRetry()
		case <-heartbeat.C:
			ok = c.write(": ping\n\n")
		}
		if !ok {
			return
		}
	}
}

// streamBackoff schedules a stream's next attempt after failures to produce
// what it sends (see streamRetryDelay).
type streamBackoff struct {
	// timer fires when the next attempt is due.
	timer *time.Timer
	// failures counts the consecutive failures.
	failures int
}

// newStreamBackoff returns a backoff with nothing scheduled.
func newStreamBackoff() *streamBackoff {
	t := time.NewTimer(0)
	t.Stop()
	return &streamBackoff{timer: t}
}

// fail records a failure and schedules the next attempt, returning its delay.
func (b *streamBackoff) fail() time.Duration {
	b.failures++
	delay := streamRetryDelay(b.failures)
	b.timer.Reset(delay)
	return delay
}

// succeed records a success and cancels any scheduled attempt.
func (b *streamBackoff) succeed() {
	b.failures = 0
	b.timer.Stop()
}

// stop cancels any scheduled attempt.
func (b *streamBackoff) stop() {
	b.timer.Stop()
}

// eventText returns data as one server-sent event, with id rev unless rev is
// 0. Each line of data becomes a data field, which clients join back with
// newlines: a payload spanning lines (a chamber stored pretty-printed, say)
// would otherwise end the event's fields at its first line, and clients would
// drop the rest.
func eventText(event string, rev uint64, data []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "event: %s\n", event)
	if rev != 0 {
		var buf [20]byte
		b.WriteString("id: ")
		b.Write(strconv.AppendUint(buf[:0], rev, 10))
		b.WriteByte('\n')
	}
	text := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(data))
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteByte('\n')
	return b.String()
}

// maxStreamLifetime is how long a watch stream stays open before the server
// closes it (plus up to a quarter more, at random, so streams opened together
// do not all reconnect together).
var maxStreamLifetime = 20 * time.Minute

// streamWriteTimeout bounds each write to a watch stream.
var streamWriteTimeout = 30 * time.Second

// streamRetryDelay is how long a stream waits before producing its payload
// again after failures consecutive failures: doubling from one second to 30,
// with jitter so streams on many paths do not retry together.
func streamRetryDelay(failures int) time.Duration {
	d := 30 * time.Second
	if failures <= 5 {
		d = time.Second << (failures - 1)
	}
	return d/2 + rand.N(d/2+1)
}

// sharedReads reads chambers for watch streams. A write wakes every stream at
// or below it at once; rather than each reading (and, with inheritance,
// merging) the chamber itself, the streams of a path share one read.
type sharedReads struct {
	strg storage.Storage
	brk  *broker
	// timeout bounds each shared read.
	timeout time.Duration
	group   singleflight.Group
}

// Get returns the entry at path. Callers share a read only when it started
// after the latest change the broker announced before they called, so a
// stream woken by a change never receives a read begun before that change.
// Ordinary requests read storage directly: a change made through another
// replica of a Watchable backend is announced only after it is read back
// from the change feed, so a shared read could miss a client's own write.
func (s *sharedReads) Get(ctx context.Context, path string) (*storage.StorageEntry, error) {
	key := path + "\x00" + strconv.FormatUint(s.brk.Generation(), 10)
	ch := s.group.DoChan(key, func() (any, error) {
		// Detached from the caller: the stream that started the read may
		// end before the others sharing it.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
		defer cancel()
		entry, err := s.strg.Get(rctx, path)
		var nf *storage.NotFoundError
		if err != nil && !errors.As(err, &nf) {
			logging.Ctx(ctx).WarnCtx(ctx).Str("path", path).Str("error", err.Error()).Msg("watch streams could not read the chamber; they will retry")
		}
		return entry, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*storage.StorageEntry), nil
	}
}

// handleHealth reports that the server is up and serving requests.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// readyTimeout bounds the storage check behind GET /readyz.
const readyTimeout = 2 * time.Second

// readyCacheFor is how long a storage check answers GET /readyz: the route is
// public, so frequent probes (or anyone else) must not each reach storage.
var readyCacheFor = time.Second

// handleReady reports whether this server should receive traffic: it is not
// draining for shutdown, its storage answers a read of the root chamber, and
// a storage that reports its own health (such as a change feed) is healthy.
func handleReady(strg storage.Storage, draining <-chan struct{}) http.HandlerFunc {
	var (
		mu        sync.Mutex
		checkedAt time.Time
		lastErr   error
	)
	// check runs the storage check, or returns the last one's result while
	// it is recent; concurrent probes wait for the check under way.
	check := func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if !checkedAt.IsZero() && time.Since(checkedAt) < readyCacheFor {
			return lastErr
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readyTimeout)
		defer cancel()
		lastErr = nil
		if _, err := strg.Get(ctx, "/"); err != nil {
			var nf *storage.NotFoundError
			if !errors.As(err, &nf) {
				lastErr = fmt.Errorf("storage unavailable: %w", err)
			}
		}
		if hr, ok := strg.(interface{ Health() error }); ok && lastErr == nil {
			if err := hr.Health(); err != nil {
				lastErr = fmt.Errorf("storage unhealthy: %w", err)
			}
		}
		checkedAt = time.Now()
		return lastErr
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		select {
		case <-draining:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("draining\n"))
			return
		default:
		}
		if err := check(r.Context()); err != nil {
			logging.Ctx(r.Context()).WarnCtx(r.Context()).Str("error", err.Error()).Msg("readiness check failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			// the cause can name storage internals: it is logged, not sent
			_, _ = w.Write([]byte("storage unavailable\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	}
}

func handleUIEmpty() http.Handler {
	stubHTML := `
	<!DOCTYPE html>
	<html>
	<body>
	<h1>Realm UI is not available</h1>
	</body>
	</html>
	`
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(stubHTML))
	})
}
