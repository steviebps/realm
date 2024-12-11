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
	"github.com/hashicorp/go-hclog"
	"github.com/steviebps/realm/api"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const DefaultHandlerTimeout = 10 * time.Second

var uiExists = true

type HandlerConfig struct {
	Logger         hclog.Logger
	Storage        storage.Storage
	RequestTimeout time.Duration
}

func RealmHandler(rlm *realm.Realm, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := r.WithContext(rlm.NewContext(r.Context()))
		h.ServeHTTP(w, req)
	})
}

func NewHandler(config HandlerConfig) (http.Handler, error) {
	if config.Storage == nil {
		return nil, fmt.Errorf("storage cannot be nil")
	}
	if config.Logger == nil {
		config.Logger = hclog.Default().Named("realm")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = DefaultHandlerTimeout
	}
	return handle(config), nil
}

func handle(hc HandlerConfig) http.Handler {
	logger := hc.Logger.Named("http")
	mux := http.NewServeMux()

	if uiExists {
		mux.Handle("/ui/", otelhttp.NewHandler(otelhttp.WithRouteTag("/ui/", gziphandler.GzipHandler(http.StripPrefix("/ui/", http.FileServer(webFS())))), "/ui/"))
	} else {
		mux.Handle("/ui/", otelhttp.NewHandler(handleUIEmpty(), "/ui/"))
	}

	mux.Handle("/v1/chambers/", otelhttp.NewHandler(handleChambers(hc.Storage, logger), "/v1/chambers/"))

	timeoutHandler := wrapWithTimeout(mux, hc.RequestTimeout)
	return wrapCommonHandler(timeoutHandler)
}

func wrapWithTimeout(h http.Handler, t time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var cancelFunc context.CancelFunc
		ctx, cancelFunc = context.WithTimeout(ctx, t)
		r = r.WithContext(ctx)
		h.ServeHTTP(w, r)
		cancelFunc()
	})
}
func wrapCommonHandler(h http.Handler) http.Handler {
	hostname, _ := os.Hostname()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		utils.WriteInterfaceWith(w, body, true)
	}
}

func handleWithStatus(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	utils.WriteInterfaceWith(w, body, true)
}

func handleError(w http.ResponseWriter, status int, resp api.HTTPErrorAndDataResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	utils.WriteInterfaceWith(w, resp, true)
}

func handleChambers(strg storage.Storage, logger hclog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestLogger := logger.With("method", r.Method, "path", r.URL.Path)
		ctx := hclog.WithContext(r.Context(), requestLogger)
		span := trace.SpanFromContext(ctx)

		req := buildAgentRequest(r)
		span.SetAttributes(attribute.String("realm.server.logicalPath", req.Path), attribute.String("realm.server.operation", string(req.Operation)))

		switch req.Operation {
		case GetOperation:
			entry, err := strg.Get(ctx, req.Path)
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
				requestLogger.Error(err.Error())

				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					err = nfError
				}

				handleError(w, http.StatusNotFound, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			handleOk(w, createResponseWithErrors(entry.Value, nil))
			return

		case PutOperation:
			var c realm.Chamber

			// ensure data is in correct format
			if err := utils.ReadInterfaceWith(r.Body, &c); err != nil {
				span.SetStatus(codes.Error, err.Error())
				requestLogger.Error(err.Error())
				if errors.Is(err, io.EOF) {
					err = errors.New("request body must not be empty")
				} else {
					err = errors.New(http.StatusText(http.StatusBadRequest))
				}
				handleError(w, http.StatusBadRequest, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			b, err := json.Marshal(&c)
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
				requestLogger.Error(err.Error())
				err = errors.New(http.StatusText(http.StatusInternalServerError))
				handleError(w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			// store the entry if the format is correct
			entry := storage.StorageEntry{Key: req.Path, Value: b}
			if err := strg.Put(ctx, entry); err != nil {
				span.SetStatus(codes.Error, err.Error())
				requestLogger.Error(err.Error())
				handleError(w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			handleWithStatus(w, http.StatusCreated, nil)
			return

		case DeleteOperation:
			if err := strg.Delete(ctx, req.Path); err != nil {
				span.SetStatus(codes.Error, err.Error())
				requestLogger.Error(err.Error())

				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					handleError(w, http.StatusNotFound, createResponseWithErrors(nil, []string{nfError.Error()}))
					return
				}

				handleError(w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}
			handleOk(w, nil)
			return

		case ListOperation:
			names, err := strg.List(ctx, req.Path)
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
				requestLogger.Error(err.Error())
				if errors.Is(err, os.ErrNotExist) {
					handleError(w, http.StatusNotFound, createResponseWithErrors(nil, []string{http.StatusText(http.StatusNotFound)}))
					return
				}
				handleError(w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}
			raw, err := json.Marshal(names)
			if err != nil {
				handleError(w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{err.Error()}))
				return
			}

			handleOk(w, createResponseWithErrors(raw, nil))
			return

		default:
			span.SetStatus(codes.Error, "method not allowed")
			handleError(w, http.StatusMethodNotAllowed, createResponseWithErrors(nil, []string{http.StatusText(http.StatusMethodNotAllowed)}))
		}
	})
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
		w.Write([]byte(stubHTML))
	})
}
