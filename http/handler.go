package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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

	mux.Handle("/v1/chambers/", otelhttp.NewHandler(handleChambers(hc.Storage, brk), "/v1/chambers/"))

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

func handleChambers(strg storage.Storage, brk *broker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		logger := logging.Ctx(ctx)
		errorLog := logger.ErrorCtx(ctx).Str("method", r.Method).Str("path", r.URL.Path)
		span := trace.SpanFromContext(ctx)

		req := buildAgentRequest(r)
		span.SetAttributes(attribute.String("realm.server.logicalPath", req.Path), attribute.String("realm.server.operation", string(req.Operation)))

		switch req.Operation {
		case GetOperation:
			if isWatchRequest(r) {
				streamChamber(w, r, strg, brk, req.Path)
				return
			}

			entry, err := strg.Get(ctx, req.Path)
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())

				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					err = nfError
				}

				handleError(ctx, w, http.StatusNotFound, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			handleOk(w, createResponseWithErrors(entry.Value, nil))
			return

		case PutOperation:
			var putChamber realm.Chamber

			// ensure data is in correct format
			if err := utils.ReadInterfaceWith(r.Body, &putChamber); err != nil {
				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())
				if errors.Is(err, io.EOF) {
					err = errors.New("request body must not be empty")
				} else {
					err = errors.New(http.StatusText(http.StatusBadRequest))
				}
				handleError(ctx, w, http.StatusBadRequest, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			b, err := json.Marshal(&putChamber)
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())
				err = errors.New(http.StatusText(http.StatusInternalServerError))
				handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			// store the entry if the format is correct
			entry := storage.StorageEntry{Key: req.Path, Value: b}
			if err := strg.Put(ctx, entry); err != nil {
				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())
				handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			brk.Notify(req.Path)
			handleWithStatus(w, http.StatusCreated, nil)
			return

		case PatchOperation:
			var patchChamber realm.Chamber

			// ensure data is in correct format
			if err := utils.ReadInterfaceWith(r.Body, &patchChamber); err != nil {
				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())
				if errors.Is(err, io.EOF) {
					err = errors.New("request body must not be empty")
				} else {
					err = errors.New(http.StatusText(http.StatusBadRequest))
				}
				handleError(ctx, w, http.StatusBadRequest, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			entry, err := strg.Get(ctx, req.Path)
			if err != nil {
				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					err = fmt.Errorf("cannot patch a resource that does not exist: %w", err)
				}

				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())

				handleError(ctx, w, http.StatusBadRequest, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			current := realm.Chamber{}
			if err := json.Unmarshal(entry.Value, &current); err != nil {
				err = fmt.Errorf("could not unmarshal current chamber while patching: %w", err)

				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())

				handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			current.OverwriteFrom(&patchChamber)
			b, err := json.Marshal(&current)
			if err != nil {
				err = fmt.Errorf("could not patch while merging chambers: %w", err)

				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())

				handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			// store the entry if the format is correct
			patchEntry := storage.StorageEntry{Key: req.Path, Value: b}
			if err := strg.Put(ctx, patchEntry); err != nil {
				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())
				handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			brk.Notify(req.Path)
			handleOk(w, nil)
			return

		case DeleteOperation:
			if err := strg.Delete(ctx, req.Path); err != nil {
				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())

				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					handleError(ctx, w, http.StatusNotFound, createResponseWithErrors(nil, []string{nfError.Error()}))
					return
				}

				handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			brk.Notify(req.Path)
			handleOk(w, nil)
			return

		case ListOperation:
			names, err := strg.List(ctx, req.Path)
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
				errorLog.Msg(err.Error())
				if errors.Is(err, os.ErrNotExist) {
					handleError(ctx, w, http.StatusNotFound, createResponseWithErrors(nil, []string{http.StatusText(http.StatusNotFound)}))
					return
				}
				handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}
			raw, err := json.Marshal(names)
			if err != nil {
				handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			handleOk(w, createResponseWithErrors(raw, nil))
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
