package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/steviebps/realm/api"
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
	return handle(ctx, config), nil
}

func handle(ctx context.Context, hc HandlerConfig) http.Handler {
	logger := logging.Ctx(ctx)
	mux := http.NewServeMux()
	brk := newBroker()

	if uiExists {
		mux.Handle("/ui/", otelhttp.NewHandler(gziphandler.GzipHandler(http.StripPrefix("/ui/", http.FileServer(webFS()))), "/ui/"))
	} else {
		mux.Handle("/ui/", otelhttp.NewHandler(handleUIEmpty(), "/ui/"))
	}

	meta := &metaStore{strg: rawStorage(hc.Storage)}
	locks := &pathLocks{}

	mux.Handle("/v1/chambers/", otelhttp.NewHandler(handleChambers(hc.Storage, meta, locks, brk), "/v1/chambers/"))
	mux.Handle("/v1/history/", otelhttp.NewHandler(handleHistory(meta), "/v1/history/"))
	mux.Handle("/v1/catalog", otelhttp.NewHandler(handleCatalog(meta), "/v1/catalog"))
	mux.Handle("/v1/catalog/", otelhttp.NewHandler(handleCatalog(meta), "/v1/catalog/"))

	timeoutHandler := wrapWithTimeout(mux, hc.RequestTimeout)
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

// isWatchRequest reports whether r is a request to stream chamber changes.
func isWatchRequest(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Query().Get("watch") == "true"
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
	raw, err := json.Marshal(v)
	if err != nil {
		handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
		return
	}
	handleOk(w, createResponseWithErrors(raw, nil))
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

// actorFrom returns the identity recorded in history for a request.
func actorFrom(r *http.Request) string {
	if a := strings.TrimSpace(r.Header.Get(ActorHeader)); a != "" {
		return a
	}
	return defaultActor
}

// hasPrecondition reports whether the request carries If-Match or If-None-Match.
func hasPrecondition(r *http.Request) bool {
	return r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != ""
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

func handleChambers(strg storage.Storage, meta *metaStore, locks *pathLocks, brk *broker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		logger := logging.Ctx(ctx)
		errorLog := logger.ErrorCtx(ctx).Str("method", r.Method).Str("path", r.URL.Path)
		span := trace.SpanFromContext(ctx)

		req := buildAgentRequest(r)
		span.SetAttributes(attribute.String("realm.server.logicalPath", req.Path), attribute.String("realm.server.operation", string(req.Operation)))

		if isReservedPath(req.Path) {
			handleError(ctx, w, http.StatusBadRequest, createResponseWithErrors(nil, []string{fmt.Sprintf("paths under %q are reserved", reservedPrefix)}))
			return
		}

		fail := func(status int, err error) {
			span.SetStatus(codes.Error, err.Error())
			errorLog.Msg(err.Error())
			handleError(ctx, w, status, createResponseWithErrors(nil, []string{err.Error()}))
		}

		// Writes decode and validate their body before taking the path lock.
		var body realm.Chamber
		if req.Operation == PutOperation || req.Operation == PatchOperation {
			if err := utils.ReadInterfaceWith(r.Body, &body); err != nil {
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

		// Writes are serialized per path. The chamber as stored (not its
		// inherited view) is read at most once: for If-Match / If-None-Match
		// and as the PATCH merge base.
		var stored *storage.StorageEntry
		switch req.Operation {
		case PutOperation, PatchOperation, DeleteOperation:
			unlock := locks.lock(req.Path)
			defer unlock()

			if req.Operation == PatchOperation || hasPrecondition(r) {
				var err error
				if stored, err = meta.get(ctx, req.Path); err != nil {
					fail(http.StatusInternalServerError, err)
					return
				}
			}
			current := ""
			if stored != nil {
				current = etagOf(stored.Value)
			}
			if preconditionFailed(r, current) {
				if current != "" {
					w.Header().Set("ETag", current)
				}
				handleError(ctx, w, http.StatusPreconditionFailed, createResponseWithErrors(nil, []string{"the chamber was changed by someone else; reload and try again"}))
				return
			}
		}

		// commit stores value (nil deletes), records history, and notifies
		// watchers. History failures are logged rather than failing the write.
		commit := func(value []byte) bool {
			var err error
			if value == nil {
				err = strg.Delete(ctx, req.Path)
			} else {
				err = strg.Put(ctx, storage.StorageEntry{Key: req.Path, Value: value})
			}
			if err != nil {
				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					fail(http.StatusNotFound, nfError)
					return false
				}
				fail(http.StatusInternalServerError, err)
				return false
			}

			rev := Revision{Time: time.Now().UTC(), Actor: actorFrom(r), Operation: req.Operation}
			if value != nil {
				rev.ETag = etagOf(value)
				rev.Chamber = value
				w.Header().Set("ETag", rev.ETag)
			}
			if err := meta.appendHistory(ctx, req.Path, rev); err != nil {
				span.RecordError(err)
				errorLog.Msg(err.Error())
			}
			brk.Notify(req.Path)
			return true
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
			b, err := json.Marshal(&body)
			if err != nil {
				fail(http.StatusInternalServerError, errors.New(http.StatusText(http.StatusInternalServerError)))
				return
			}
			if commit(b) {
				handleWithStatus(w, http.StatusCreated, nil)
			}
			return

		case PatchOperation:
			// Merge into the chamber as stored, not its inherited view, so
			// inherited rules are never copied into the child.
			if stored == nil {
				fail(http.StatusBadRequest, fmt.Errorf("cannot patch a resource that does not exist: %w", &storage.NotFoundError{Key: req.Path}))
				return
			}
			current := realm.Chamber{}
			if err := json.Unmarshal(stored.Value, &current); err != nil {
				fail(http.StatusInternalServerError, fmt.Errorf("could not unmarshal current chamber while patching: %w", err))
				return
			}
			current.OverwriteFrom(&body)
			b, err := json.Marshal(&current)
			if err != nil {
				fail(http.StatusInternalServerError, fmt.Errorf("could not patch while merging chambers: %w", err))
				return
			}
			if commit(b) {
				handleOk(w, nil)
			}
			return

		case DeleteOperation:
			if commit(nil) {
				handleOk(w, nil)
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

// streamChamber holds an SSE connection open and pushes the chamber at path
// whenever it changes. It sends the current chamber immediately, then re-sends
// on every change signal from the broker, and emits periodic heartbeats so dead
// connections and idle intermediaries are handled.
func streamChamber(w http.ResponseWriter, r *http.Request, strg storage.Storage, brk *broker, path string) {
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

	signals, unsubscribe := brk.Subscribe(path)
	defer unsubscribe()

	// send writes the current chamber as a single SSE event. Stored chamber
	// values are compact JSON (single line), so they are safe as SSE data.
	// It returns false when the connection should be torn down.
	send := func() bool {
		entry, err := strg.Get(ctx, path)
		if err != nil {
			// A not-yet-existing chamber is not fatal for a stream; wait for it
			// to be created rather than closing the connection.
			var nfError *storage.NotFoundError
			if errors.As(err, &nfError) {
				return true
			}
			logger.ErrorCtx(ctx).Str("path", path).Msg(err.Error())
			return false
		}
		if _, err := fmt.Fprintf(w, "event: chamber\ndata: %s\n\n", entry.Value); err != nil {
			return false
		}
		flusher.Flush()
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
