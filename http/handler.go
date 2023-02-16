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
	Method string
	Data   any
}

type HandlerConfig struct {
	Realm   *realm.Realm
	Storage storage.Storage
}

func NewHandler(config HandlerConfig) (http.Handler, error) {
	if config.Realm == nil {
		return nil, fmt.Errorf("handler requires a non-nil Realm")
	}
	if config.Storage == nil {
		return nil, fmt.Errorf("storage cannot be nil")
	}
	return handle(config.Storage, config.Realm.Logger().Named("http")), nil
}

func handle(stg storage.Storage, logger hclog.Logger) http.Handler {
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
				http.Error(w, errStr, http.StatusNotFound)
				return
			}

			entry, err := stg.Get(loggerCtx, path)
			if err != nil {
				requestLogger.Error(err.Error())
				if errors.Is(err, os.ErrNotExist) {
					http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
					return
				}

				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}

			var c realm.Chamber
			if err := json.Unmarshal(entry.Value, &c); err != nil {
				requestLogger.Error(err.Error())
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			response := OperationResponse{
				Method: "GET",
				Data:   entry.Value,
			}
			utils.WriteInterfaceWith(w, response, true)
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
				http.Error(w, msg, http.StatusBadRequest)
				return
			}

			// store the entry if the format is correct
			entry := storage.StorageEntry{Key: utils.EnsureTrailingSlash(path) + c.Name, Value: buf.Bytes()}
			if err := stg.Put(loggerCtx, entry); err != nil {
				requestLogger.Error(err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return

		case http.MethodDelete:
			if err := stg.Delete(loggerCtx, path); err != nil {
				requestLogger.Error(err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return

		case "LIST":
			names, err := stg.List(loggerCtx, path)
			if err != nil {
				requestLogger.Error(err.Error())
				if errors.Is(err, os.ErrNotExist) {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
				return
			}

			response := OperationResponse{
				Method: "LIST",
				Data:   names,
			}
			w.Header().Set("Content-Type", "application/json")
			if err := utils.WriteInterfaceWith(w, response, true); err != nil {
				requestLogger.Error(err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return

		default:
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		}
	})

	return wrapWithTimeout(mux, 1*time.Second)
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
