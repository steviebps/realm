package http

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/pkg/storage"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// handleHistory serves chamber change history:
//
//	GET /v1/history/<path>               → revision summaries, newest first
//	GET /v1/history/<path>?revision=<n>  → one revision including its chamber
func handleHistory(meta *metaStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		span := trace.SpanFromContext(ctx)

		if r.Method != http.MethodGet {
			handleError(ctx, w, http.StatusMethodNotAllowed, createResponseWithErrors(nil, []string{http.StatusText(http.StatusMethodNotAllowed)}))
			return
		}

		chamberPath := logicalPath(r, "/v1/history")
		span.SetAttributes(attribute.String("realm.server.logicalPath", chamberPath))
		if err := storage.ValidatePath(chamberPath); err != nil {
			handleError(ctx, w, http.StatusBadRequest, createResponseWithErrors(nil, []string{err.Error()}))
			return
		}
		if !auth.FromContext(ctx).CanRead(chamberPath) {
			forbidden(w, r, fmt.Sprintf("not allowed to read %s", chamberPath))
			return
		}

		var body any
		var err error
		if revStr := r.URL.Query().Get("revision"); revStr != "" {
			rev, convErr := strconv.Atoi(revStr)
			if convErr != nil || rev < 1 {
				handleError(ctx, w, http.StatusBadRequest, createResponseWithErrors(nil, []string{"revision must be a positive integer"}))
				return
			}
			body, err = meta.revision(ctx, chamberPath, rev)
		} else {
			body, err = meta.history(ctx, chamberPath)
		}
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			status := http.StatusInternalServerError
			var nf *storage.NotFoundError
			switch {
			case errors.As(err, &nf):
				status = http.StatusNotFound
			case errors.Is(err, storage.ErrInvalidPath):
				status = http.StatusBadRequest
			}
			noteFailure(ctx, status, err)
			handleError(ctx, w, status, createResponseWithErrors(nil, []string{err.Error()}))
			return
		}

		handleOkJSON(ctx, w, body)
	})
}

// catalogValues is the request and response body for a single attribute.
type catalogValues struct {
	Values map[string]*string `json:"values"`
}

// handleCatalog serves the attribute catalog that maps targeting values to
// human-readable labels for the UI:
//
//	GET    /v1/catalog              → every attribute and its labeled values
//	GET    /v1/catalog/<attribute>  → {"values": {"<value>": "<label>"}}
//	POST   /v1/catalog/<attribute>  → replace (PUT is accepted too)
//	PATCH  /v1/catalog/<attribute>  → upsert labels; a null label removes a value
//	DELETE /v1/catalog/<attribute>  → remove the attribute
func handleCatalog(meta *metaStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		ctx := r.Context()
		span := trace.SpanFromContext(ctx)

		attr := routeParam(r, "/v1/catalog")
		span.SetAttributes(attribute.String("realm.server.catalog.attribute", attr))

		fail := func(status int, err error) {
			span.SetStatus(codes.Error, err.Error())
			noteFailure(ctx, status, err)
			handleError(ctx, w, status, createResponseWithErrors(nil, []string{err.Error()}))
		}
		p := auth.FromContext(ctx)
		if !p.CanReadCatalog() {
			forbidden(w, r, "not allowed to read the catalog")
			return
		}
		if r.Method != http.MethodGet && !p.IsAdmin() {
			forbidden(w, r, "only administrators may change the catalog")
			return
		}
		if attr == "" && r.Method != http.MethodGet {
			fail(http.StatusMethodNotAllowed, errors.New(http.StatusText(http.StatusMethodNotAllowed)))
			return
		}

		readBody := func() (*catalogValues, bool) {
			var body catalogValues
			if err := utils.ReadRequiredInterfaceWith(r.Body, &body); err != nil {
				if errors.Is(err, io.EOF) {
					err = errors.New("request body must not be empty")
				}
				fail(http.StatusBadRequest, err)
				return nil, false
			}
			return &body, true
		}

		switch r.Method {
		case http.MethodGet:
			c, err := meta.catalog(ctx)
			if err != nil {
				fail(http.StatusInternalServerError, err)
				return
			}
			if attr == "" {
				handleOkJSON(ctx, w, c)
				return
			}
			values, ok := c[attr]
			if !ok {
				fail(http.StatusNotFound, fmt.Errorf("attribute %q is not in the catalog", attr))
				return
			}
			handleOkJSON(ctx, w, map[string]any{"values": values})

		case http.MethodPost, http.MethodPut:
			body, ok := readBody()
			if !ok {
				return
			}
			values := make(map[string]string, len(body.Values))
			for v, label := range body.Values {
				if label == nil {
					fail(http.StatusBadRequest, fmt.Errorf("label for %q must not be null when replacing", v))
					return
				}
				values[v] = *label
			}
			if _, err := meta.updateCatalog(ctx, func(c Catalog) error {
				c[attr] = values
				return nil
			}); err != nil {
				fail(http.StatusInternalServerError, err)
				return
			}
			handleOk(w, nil)

		case http.MethodPatch:
			body, ok := readBody()
			if !ok {
				return
			}
			c, err := meta.updateCatalog(ctx, func(c Catalog) error {
				values := c[attr]
				if values == nil {
					values = make(map[string]string, len(body.Values))
				}
				for v, label := range body.Values {
					if label == nil {
						delete(values, v)
						continue
					}
					values[v] = *label
				}
				c[attr] = values
				return nil
			})
			if err != nil {
				fail(http.StatusInternalServerError, err)
				return
			}
			handleOkJSON(ctx, w, map[string]any{"values": c[attr]})

		case http.MethodDelete:
			if _, err := meta.updateCatalog(ctx, func(c Catalog) error {
				if _, ok := c[attr]; !ok {
					return &storage.NotFoundError{Key: attr}
				}
				delete(c, attr)
				return nil
			}); err != nil {
				var nf *storage.NotFoundError
				if errors.As(err, &nf) {
					fail(http.StatusNotFound, err)
					return
				}
				fail(http.StatusInternalServerError, err)
				return
			}
			handleOk(w, nil)

		default:
			fail(http.StatusMethodNotAllowed, errors.New(http.StatusText(http.StatusMethodNotAllowed)))
		}
	})
}
