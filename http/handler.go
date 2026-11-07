package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
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
		mux.Handle("/ui/", gziphandler.GzipHandler(http.StripPrefix("/ui/", http.FileServer(webFS()))))
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

	mux.Handle("/v1/chambers/", handleChambers(hc.Storage, meta, brk, notify))
	mux.Handle("/v1/evaluate/", handleEvaluate(hc.Storage, brk))
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
	return otelhttp.NewHandler(wrapCommonHandler(timeoutHandler, logger), "realm",
		otelhttp.WithFilter(traced),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + routeOf(r.URL.Path)
		}),
	)
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

func wrapCommonHandler(next http.Handler, logger *logging.TracedLogger) http.Handler {
	meter := otel.Meter("github.com/steviebps/realm")
	apiCounter, _ := meter.Int64Counter(
		"handler.counter",
		metric.WithDescription("Number of API calls."),
		metric.WithUnit("{call}"),
	)

	hostname, _ := os.Hostname()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(logger.WithContext(r.Context()))
		apiCounter.Add(r.Context(), 1)
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
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

func handleChambers(strg storage.Storage, meta *metaStore, brk *broker, notify func(path string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		logger := logging.Ctx(ctx)
		errorLog := logger.ErrorCtx(ctx).Str("method", r.Method).Str("path", r.URL.Path)
		span := trace.SpanFromContext(ctx)

		req := buildAgentRequest(r)
		span.SetAttributes(attribute.String("realm.server.logicalPath", req.Path), attribute.String("realm.server.operation", string(req.Operation)))

		if storage.IsReservedPath(req.Path) {
			handleError(ctx, w, http.StatusBadRequest, createResponseWithErrors(nil, []string{fmt.Sprintf("paths under %q are reserved", storage.ReservedPrefix)}))
			return
		}

		fail := func(status int, err error) {
			span.SetStatus(codes.Error, err.Error())
			errorLog.Msg(err.Error())
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
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					fail(http.StatusRequestEntityTooLarge, fmt.Errorf("request body must be at most %d bytes", tooLarge.Limit))
					return
				}
				fail(http.StatusBadRequest, err)
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
			etag, err := writeChamber(ctx, meta, r, req, &body)
			var pf *preconditionError
			var nf *storage.NotFoundError
			switch {
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
			case err != nil:
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
			// ?inherit=layers returns the chamber and each parent chamber its
			// inherited view is built from, nearest first (see layers.go).
			if r.URL.Query().Get("inherit") == "layers" {
				serveLayers(w, r, strg, p, req.Path, fail)
				return
			}
			if isWatchRequest(r) {
				streamChamber(w, r, strg, brk, req.Path)
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
				if errors.As(err, &nfError) {
					err = nfError
				}
				fail(http.StatusNotFound, err)
				return
			}

			if own {
				w.Header().Set("ETag", etagOf(entry.Value))
			}
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
			case err != nil:
				fail(http.StatusInternalServerError, err)
			default:
				// subscribers at or below the path are all signaled
				notify(req.Path)
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

// writeChamber applies a PUT, PATCH, or DELETE of req.Path and records it in
// the chamber's history, atomically, and returns the chamber's new ETag
// (empty after a delete). Conditional headers are checked against the chamber
// as stored, and a PATCH merges into it rather than its inherited view, so
// inherited rules are never copied into the child.
func writeChamber(ctx context.Context, meta *metaStore, r *http.Request, req *AgentRequest, body *realm.Chamber) (string, error) {
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

// chamberTree returns root and every path below it that holds or leads to a
// chamber, as stored (without inheritance). Paths are canonical.
func chamberTree(ctx context.Context, strg storage.Storage, root string) ([]string, error) {
	paths := []string{root}
	for i := 0; i < len(paths); i++ {
		names, err := strg.List(ctx, paths[i])
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, name := range names {
			// backends name a child "a" or "a/", and the file backend lists
			// a chamber's own entry as "."
			name = strings.TrimSuffix(name, "/")
			if name == "" || name == "." {
				continue
			}
			// realm's metadata lives only beneath the root; a chamber named
			// "_realm" anywhere else is an ordinary one
			if paths[i] == "/" && isReservedName(name) {
				continue
			}
			paths = append(paths, paths[i]+name+"/")
		}
	}
	return paths, nil
}

// deleteChamberTree deletes the chamber at root and every chamber below it in
// one Update, recording each deletion in that chamber's history, and returns
// the deleted paths. Conditional headers are checked against root's chamber.
// It returns a NotFoundError when there is no chamber at or below root.
//
// The tree is listed before the Update, so a chamber created below root
// concurrently may survive; deleting again removes it.
func deleteChamberTree(ctx context.Context, meta *metaStore, r *http.Request, root string) ([]string, error) {
	paths, err := chamberTree(ctx, meta.strg, root)
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
	if err != nil {
		return nil, err
	}
	slices.Sort(deleted)
	return deleted, nil
}

// streamChamber holds an SSE connection open and pushes the chamber at path
// whenever it changes. Clients evaluate the chamber themselves, so its
// schedules need no pushes of their own.
func streamChamber(w http.ResponseWriter, r *http.Request, strg storage.Storage, brk *broker, path string) {
	streamEvents(w, r, brk, path, "chamber", func(ctx context.Context) ([]byte, time.Time, error) {
		entry, err := strg.Get(ctx, path)
		if err != nil {
			return nil, time.Time{}, err
		}
		return entry.Value, time.Time{}, nil
	})
}

// streamEvents holds an SSE connection open and sends the payload produced by
// current as a single event: once at the start, then again whenever the broker
// signals a change at or above path and the payload differs from the last one
// sent. It emits periodic heartbeats so dead connections and idle
// intermediaries are handled. Payloads must be single-line (compact JSON).
//
// current may also return a time at which the payload changes by itself (a
// schedule boundary of an evaluated flag); the payload is produced again then.
// A zero time means it changes only with writes.
//
// A payload whose source does not exist yet is not fatal: the stream waits for
// it to be created. Any other error closes the stream.
func streamEvents(w http.ResponseWriter, r *http.Request, brk *broker, path, event string, current func(context.Context) ([]byte, time.Time, error)) {
	ctx := r.Context()
	logger := logging.Ctx(ctx)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Bool("realm.server.watch", true))

	flusher, ok := w.(http.Flusher)
	if !ok {
		handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{"streaming is not supported"}))
		return
	}

	signals, unsubscribe, ok := brk.Subscribe(path)
	if !ok {
		w.Header().Set("Retry-After", "5")
		handleError(ctx, w, http.StatusServiceUnavailable, createResponseWithErrors(nil, []string{"too many open streams; try again later"}))
		return
	}
	defer unsubscribe()

	// Streams end after a while and clients reconnect, which re-checks their
	// credentials (a revoked key or expired session loses its stream) and
	// spreads streams over replicas added since.
	lifetime := time.NewTimer(maxStreamLifetime + rand.N(maxStreamLifetime/4))
	defer lifetime.Stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Commit the headers now, so clients see the stream open even while
	// they wait for the payload's source to be created.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// recheck fires when the last payload produced changes by itself.
	recheck := time.NewTimer(0)
	recheck.Stop()
	defer recheck.Stop()

	var last []byte
	// send writes the current payload unless it is unchanged. It returns
	// false when the connection should be torn down.
	send := func() bool {
		data, changesAt, err := current(ctx)
		if changesAt.IsZero() {
			recheck.Stop()
		} else {
			recheck.Reset(time.Until(changesAt))
		}
		if err != nil {
			var nfError *storage.NotFoundError
			if errors.As(err, &nfError) {
				return true
			}
			logger.ErrorCtx(ctx).Str("path", path).Msg(err.Error())
			return false
		}
		if last != nil && bytes.Equal(data, last) {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		flusher.Flush()
		last = data
		return true
	}

	if !send() {
		return
	}

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-lifetime.C:
			return
		case <-signals:
			if !send() {
				return
			}
		case <-recheck.C:
			if !send() {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// maxStreamLifetime is how long a watch stream stays open before the server
// closes it (plus up to a quarter more, at random, so streams opened together
// do not all reconnect together).
var maxStreamLifetime = 20 * time.Minute

// handleHealth reports that the server is up and serving requests.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// readyTimeout bounds the storage check behind GET /readyz.
const readyTimeout = 2 * time.Second

// handleReady reports whether this server should receive traffic: it is not
// draining for shutdown and its storage answers a read of the root chamber.
func handleReady(strg storage.Storage, draining <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		select {
		case <-draining:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("draining\n"))
			return
		default:
		}
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if _, err := strg.Get(ctx, "/"); err != nil {
			var nf *storage.NotFoundError
			if !errors.As(err, &nf) {
				logging.Ctx(ctx).WarnCtx(ctx).Str("error", err.Error()).Msg("readiness check: storage unavailable")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("storage unavailable\n"))
				return
			}
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
