package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// DisableSubtreeCompression stops gzipping subtree reads and watches
	// (?subtree=true), which are otherwise gzipped for clients that accept
	// it. Each subtree stream compresses its own copy of every event, so
	// turning it off trades bandwidth for CPU.
	DisableSubtreeCompression bool
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
	if feed, ok := storage.ChangeFeed(hc.Storage); ok {
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

	reader := newChamberReader(hc.Storage, brk, hc.RequestTimeout)
	trees := &subtrees{strg: hc.Storage, views: reader.views, brk: brk, limit: hc.MaxSubtreeChambers, compress: !hc.DisableSubtreeCompression}
	mux.Handle("/v1/chambers/", handleChambers(reader, newChamberWriter(hc.Storage, notify), brk, trees))
	mux.Handle("/v1/evaluate/", handleEvaluate(reader, brk))
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

func handleChambers(reader *chamberReader, writer *chamberWriter, brk *broker, trees *subtrees) http.Handler {
	strg := reader.strg
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

		// write applies the write and responds with status on success.
		write := func(status int, recursive bool) {
			res, err := writer.Write(ctx, writeRequest{
				Path:          req.Path,
				Operation:     req.Operation,
				Body:          &body,
				Recursive:     recursive,
				Preconditions: preconditions{IfMatch: r.Header.Get("If-Match"), IfNoneMatch: r.Header.Get("If-None-Match")},
				Actor:         p.Actor(),
			})
			var pf *preconditionError
			switch {
			case errors.As(err, &pf):
				if pf.current != "" {
					w.Header().Set("ETag", pf.current)
				}
				handleError(ctx, w, http.StatusPreconditionFailed, createResponseWithErrors(nil, []string{"the chamber was changed by someone else; reload and try again"}))
			case err != nil:
				fail(writeStatus(err), err)
			case recursive:
				handleOkJSON(ctx, w, deleteResult{Deleted: res.Deleted})
			default:
				if res.ETag != "" {
					w.Header().Set("ETag", res.ETag)
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
				streamChamber(w, r, reader, brk, req.Path)
				return
			}

			// ?inherit=false returns the chamber exactly as stored at this path,
			// without rules inherited from its parents, plus its ETag (used by
			// editors). The default, inherited view carries no ETag because it
			// is not the representation that writes are checked against.
			own := r.URL.Query().Get("inherit") == "false"
			entry, err := reader.Chamber(ctx, req.Path, own)
			if err != nil {
				failRead(ctx, w, p, err)
				return
			}

			if own {
				w.Header().Set("ETag", etagOf(entry.Value))
			}
			setRevision(w, entry.Version)
			handleOk(w, createResponseWithErrors(entry.Value, nil))
			return

		case PutOperation:
			write(http.StatusCreated, false)
			return

		case PatchOperation:
			write(http.StatusOK, false)
			return

		case DeleteOperation:
			recursive, err := queryBool(r, "recursive")
			if err != nil {
				fail(http.StatusBadRequest, err)
				return
			}
			write(http.StatusOK, recursive)
			return

		case ListOperation:
			names, err := strg.List(ctx, req.Path)
			if err != nil {
				failRead(ctx, w, p, err)
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

// setRevision sets the revision header (see api.RevisionHeader), unless the
// storage does not track revisions (rev is 0).
func setRevision(w http.ResponseWriter, rev uint64) {
	if rev != 0 {
		w.Header().Set(api.RevisionHeader, strconv.FormatUint(rev, 10))
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
		if lastErr == nil {
			if err := storage.Health(strg); err != nil {
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
