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
}

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
	var authn *auth.Authenticator
	if config.Auth != nil {
		var err error
		if authn, err = auth.New(*config.Auth, rawStorage(config.Storage)); err != nil {
			return nil, err
		}
	}
	return handle(ctx, config, authn), nil
}

func handle(ctx context.Context, hc HandlerConfig, authn *auth.Authenticator) http.Handler {
	logger := logging.Ctx(ctx)
	mux := http.NewServeMux()
	brk := newBroker()

	if uiExists {
		mux.Handle("/ui/", otelhttp.NewHandler(gziphandler.GzipHandler(http.StripPrefix("/ui/", http.FileServer(webFS()))), "/ui/"))
	} else {
		mux.Handle("/ui/", otelhttp.NewHandler(handleUIEmpty(), "/ui/"))
	}

	meta := &metaStore{strg: rawStorage(hc.Storage)}

	// A backend that reports every change, including other servers' writes,
	// drives the broker; otherwise the handler notifies it after each write.
	notify := brk.Notify
	if feed, ok := meta.strg.(storage.Watchable); ok {
		notify = func(string) {}
		go func() {
			for c := range feed.Watch(ctx) {
				brk.Notify(c.Key)
			}
		}()
	}

	mux.Handle("/v1/chambers/", otelhttp.NewHandler(handleChambers(hc.Storage, meta, brk, notify), "/v1/chambers/"))
	mux.Handle("/v1/evaluate/", otelhttp.NewHandler(handleEvaluate(hc.Storage, brk), "/v1/evaluate/"))
	mux.Handle("/v1/history/", otelhttp.NewHandler(handleHistory(meta), "/v1/history/"))
	mux.Handle("/v1/catalog", otelhttp.NewHandler(handleCatalog(meta), "/v1/catalog"))
	mux.Handle("/v1/catalog/", otelhttp.NewHandler(handleCatalog(meta), "/v1/catalog/"))
	mux.Handle("/v1/auth/me", otelhttp.NewHandler(handleMe(authn), "/v1/auth/me"))
	if authn != nil {
		mux.Handle("/v1/auth/", otelhttp.NewHandler(authn.Handler(), "/v1/auth/"))
		keys := requireAdmin(handleKeys(authn.Keys()))
		mux.Handle("/v1/keys", otelhttp.NewHandler(keys, "/v1/keys"))
		mux.Handle("/v1/keys/", otelhttp.NewHandler(keys, "/v1/keys/"))
	}
	// Liveness for orchestrators (e.g. Kubernetes probes); deliberately not
	// traced so frequent probes do not flood trace exports. It is public (see
	// publicPaths).
	mux.HandleFunc("GET /healthz", handleHealth)

	timeoutHandler := wrapWithTimeout(withCORS(evaluatePrefix, withPrincipal(authn, mux)), hc.RequestTimeout)
	return wrapCommonHandler(timeoutHandler, logger)
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

// isWatchRequest reports whether r asks to stream changes (?watch=true). Such
// requests are exempt from the request timeout.
func isWatchRequest(r *http.Request) bool {
	return r.URL.Query().Get("watch") == "true"
}

func wrapCommonHandler(h http.Handler, logger *logging.TracedLogger) http.Handler {
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
		w.Header().Set("Cache-Control", "no-store")

		if hostname != "" {
			w.Header().Set("X-Realm-Hostname", hostname)
		}
		h.ServeHTTP(w, r)
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

func handleError(ctx context.Context, w http.ResponseWriter, status int, resp api.HTTPErrorAndDataResponse) {
	errorCounter.Add(ctx, 1, metric.WithAttributes(attribute.Int("http.status_code", status)))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = utils.WriteInterfaceWith(w, resp, true)
}

// unwrapper is implemented by storage wrappers that transform reads (such as
// inheritance) and can expose the storage they wrap.
type unwrapper interface {
	Unwrap() storage.Storage
}

// rawStorage returns the innermost storage beneath any transforming wrappers,
// so metadata and ETags reflect exactly what is stored at a path.
func rawStorage(s storage.Storage) storage.Storage {
	for {
		u, ok := s.(unwrapper)
		if !ok {
			return s
		}
		s = u.Unwrap()
	}
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
			if err := utils.ReadRequiredInterfaceWith(r.Body, &body); err != nil {
				if errors.Is(err, io.EOF) {
					err = errors.New("request body must not be empty")
				} else {
					// surface validation errors (bad types, clauses, removed fields)
					err = fmt.Errorf("invalid chamber: %w", err)
				}
				fail(http.StatusBadRequest, err)
				return
			}
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

		case PatchOperation, DeleteOperation:
			commit(http.StatusOK)
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

// streamChamber holds an SSE connection open and pushes the chamber at path
// whenever it changes.
func streamChamber(w http.ResponseWriter, r *http.Request, strg storage.Storage, brk *broker, path string) {
	streamEvents(w, r, brk, path, "chamber", func(ctx context.Context) ([]byte, error) {
		entry, err := strg.Get(ctx, path)
		if err != nil {
			return nil, err
		}
		return entry.Value, nil
	})
}

// streamEvents holds an SSE connection open and sends the payload produced by
// current as a single event: once at the start, then again whenever the broker
// signals a change at or above path and the payload differs from the last one
// sent. It emits periodic heartbeats so dead connections and idle
// intermediaries are handled. Payloads must be single-line (compact JSON).
//
// A payload whose source does not exist yet is not fatal: the stream waits for
// it to be created. Any other error closes the stream.
func streamEvents(w http.ResponseWriter, r *http.Request, brk *broker, path, event string, current func(context.Context) ([]byte, error)) {
	ctx := r.Context()
	logger := logging.Ctx(ctx)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Bool("realm.server.watch", true))

	flusher, ok := w.(http.Flusher)
	if !ok {
		handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{"streaming is not supported"}))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Commit the headers now, so clients see the stream open even while
	// they wait for the payload's source to be created.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	signals, unsubscribe := brk.Subscribe(path)
	defer unsubscribe()

	var last []byte
	// send writes the current payload unless it is unchanged. It returns
	// false when the connection should be torn down.
	send := func() bool {
		data, err := current(ctx)
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
		case <-signals:
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

// handleHealth reports that the server is up and serving requests.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
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
