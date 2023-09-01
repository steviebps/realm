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
)

type HandlerConfig struct {
	Logger         hclog.Logger
	Storage        storage.Storage
	RequestTimeout time.Duration
}

func NewHandler(config HandlerConfig) (http.Handler, error) {
	if config.Storage == nil {
		return nil, fmt.Errorf("storage cannot be nil")
	}
	if config.Logger == nil {
		config.Logger = hclog.Default().Named("realm")
	}
	return handle(config), nil
}

func handle(hc HandlerConfig) http.Handler {
	logger := hc.Logger.Named("http")
	mux := http.NewServeMux()

	mux.Handle("/ui/", gziphandler.GzipHandler(http.StripPrefix("/ui/", http.FileServer(webFS()))))

	mux.Handle("/v1/chambers/", handleChambers(hc.Storage, logger))

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
func createResponse(data json.RawMessage) *api.HTTPResponse {
	response := &api.HTTPResponse{}
	if data != nil {
		response.Data = data
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

func handleOkWithStatus(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	utils.WriteInterfaceWith(w, body, true)
}

func handleError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	resp := &api.HTTPErrorResponse{Errors: make([]string, 0, 1)}
	if err != nil {
		resp.Errors = append(resp.Errors, err.Error())
	}
	w.WriteHeader(status)
	utils.WriteInterfaceWith(w, resp, true)
}

func handleChambers(strg storage.Storage, logger hclog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		requestLogger := logger.With("method", r.Method, "path", r.URL.Path)
		loggerCtx := hclog.WithContext(ctx, requestLogger)

		req := buildAgentRequest(r)

		switch req.Operation {
		case GetOperation:
			entry, err := strg.Get(loggerCtx, req.Path)
			if err != nil {
				requestLogger.Error(err.Error())

				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					err = nfError
				}

				handleError(w, http.StatusNotFound, err)
				return
			}

			handleOk(w, createResponse(entry.Value))
			return

		case PutOperation:
			var c realm.Chamber

			// ensure data is in correct format
			if err := utils.ReadInterfaceWith(r.Body, &c); err != nil {
				requestLogger.Error(err.Error())
				if errors.Is(err, io.EOF) {
					err = errors.New("request body must not be empty")
				} else {
					err = errors.New(http.StatusText(http.StatusBadRequest))
				}
				handleError(w, http.StatusBadRequest, err)
				return
			}

			b, err := json.Marshal(&c)
			if err != nil {
				requestLogger.Error(err.Error())
				err = errors.New(http.StatusText(http.StatusInternalServerError))
				handleError(w, http.StatusInternalServerError, err)
			}

			// store the entry if the format is correct
			entry := storage.StorageEntry{Key: req.Path, Value: b}
			if err := strg.Put(loggerCtx, entry); err != nil {
				requestLogger.Error(err.Error())
				handleError(w, http.StatusInternalServerError, err)
				return
			}

			handleOkWithStatus(w, http.StatusCreated, nil)
			return

		case DeleteOperation:
			if err := strg.Delete(loggerCtx, req.Path); err != nil {
				requestLogger.Error(err.Error())

				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					handleError(w, http.StatusNotFound, nfError)
					return
				}

				handleError(w, http.StatusInternalServerError, err)
				return
			}
			handleOk(w, nil)
			return

		case ListOperation:
			names, err := strg.List(loggerCtx, req.Path)
			if err != nil {
				requestLogger.Error(err.Error())
				if errors.Is(err, os.ErrNotExist) {
					handleError(w, http.StatusNotFound, errors.New(http.StatusText(http.StatusNotFound)))
					return
				}
				handleError(w, http.StatusInternalServerError, err)
				return
			}
			raw, err := json.Marshal(names)
			if err != nil {
				handleError(w, http.StatusInternalServerError, err)
				return
			}

			handleOk(w, createResponse(raw))
			return

		default:
			handleError(w, http.StatusMethodNotAllowed, errors.New(http.StatusText(http.StatusMethodNotAllowed)))
		}
	})

}
