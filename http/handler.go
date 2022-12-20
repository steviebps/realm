package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
)

func Handler(storage storage.Storage) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		switch r.Method {
		case http.MethodGet:
			c, err := storage.Get(ctx, strings.TrimPrefix(r.URL.Path, "/v1"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			utils.WriteInterfaceWith(w, c, true)
			return

		case http.MethodPost:
			var c *realm.Chamber
			if err := json.NewDecoder(r.Body).Decode(c); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := storage.Put(ctx, c); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return

		case "LIST":
			names, err := storage.List(ctx, strings.TrimPrefix(r.URL.Path, "/v1"))
			if err != nil {
				http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := utils.WriteInterfaceWith(w, names, true); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
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
