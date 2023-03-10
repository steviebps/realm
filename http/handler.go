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
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
)

type OperationResponse struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

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
	strg := hc.Storage
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		requestLogger := logger.With("method", r.Method, "path", r.URL.Path)
		loggerCtx := hclog.WithContext(ctx, requestLogger)

		path := strings.TrimPrefix(r.URL.Path, "/v1")
		switch r.Method {
		case http.MethodGet:
			if path == "/" {
				errStr := fmt.Sprintf("path cannot be %q", path)
				requestLogger.Error(errStr)
				handleResponse(w, http.StatusNotFound, nil, errStr)
				return
			}

			entry, err := strg.Get(loggerCtx, path)
			if err != nil {
				msg := err.Error()
				requestLogger.Error(msg)

				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					msg = http.StatusText(http.StatusNotFound)
				}

				handleResponse(w, http.StatusNotFound, nil, msg)
				return
			}

			var c realm.Chamber
			if err := json.Unmarshal(entry.Value, &c); err != nil {
				requestLogger.Error(err.Error())
				handleResponse(w, http.StatusInternalServerError, nil, http.StatusText(http.StatusInternalServerError))
				return
			}

			handleResponse(w, http.StatusOK, c, "")
			return

		case http.MethodPost:
			var c realm.Chamber
			buf := new(bytes.Buffer)
			tr := io.TeeReader(r.Body, buf)

			// ensure data is in correct format
			if err := utils.ReadInterfaceWith(tr, &c); err != nil {
				requestLogger.Error(err.Error())
				msg := http.StatusText(http.StatusBadRequest)
				if errors.Is(err, io.EOF) {
					msg = "Request body must not be empty"
				}
				handleResponse(w, http.StatusBadRequest, nil, msg)
				return
			}

			// store the entry if the format is correct
			entry := storage.StorageEntry{Key: utils.EnsureTrailingSlash(path) + c.Name, Value: buf.Bytes()}
			if err := strg.Put(loggerCtx, entry); err != nil {
				requestLogger.Error(err.Error())
				handleResponse(w, http.StatusInternalServerError, nil, err.Error())
				return
			}

			handleResponse(w, http.StatusCreated, nil, "")
			return

		case http.MethodDelete:
			if err := strg.Delete(loggerCtx, path); err != nil {
				requestLogger.Error(err.Error())

				var nfError *storage.NotFoundError
				if errors.As(err, &nfError) {
					handleResponse(w, http.StatusNotFound, nil, http.StatusText(http.StatusNotFound))
					return
				}

				handleResponse(w, http.StatusInternalServerError, nil, err.Error())
				return
			}
			handleResponse(w, http.StatusOK, nil, "")
			return

		case "LIST":
			names, err := strg.List(loggerCtx, path)
			if err != nil {
				requestLogger.Error(err.Error())
				if errors.Is(err, os.ErrNotExist) {
					handleResponse(w, http.StatusNotFound, nil, http.StatusText(http.StatusNotFound))
					return
				}
				handleResponse(w, http.StatusInternalServerError, nil, err.Error())
				return
			}

			handleResponse(w, http.StatusOK, names, "")
			return

		default:
			handleResponse(w, http.StatusMethodNotAllowed, nil, http.StatusText(http.StatusMethodNotAllowed))
		}
	})

	return wrapWithTimeout(mux, hc.RequestTimeout)
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

func handleResponse(w http.ResponseWriter, statusCode int, data any, error string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	response := OperationResponse{}
	if data != nil {
		response.Data = data
	}
	if error != "" {
		response.Error = error
	}

	if err := utils.WriteInterfaceWith(w, response, true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
