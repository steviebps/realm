package http

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/helper/logging"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Layer statuses reported by GET /v1/chambers/<path>?inherit=layers.
const (
	// layerOK: the layer's rules hold every flag the chamber defines.
	layerOK = "ok"
	// layerMissing: there is no chamber at the layer's path; it contributes nothing.
	layerMissing = "missing"
	// layerInvalid: the chamber cannot be parsed; error says why.
	layerInvalid = "invalid"
)

// layerView is one chamber in a layers response.
type layerView struct {
	Path   string                 `json:"path"`
	Status string                 `json:"status"`
	Rules  map[string]*realm.Flag `json:"rules,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

// layersResponse is the body of GET /v1/chambers/<path>?inherit=layers: the
// chamber and each parent chamber its inherited view consults, nearest
// first, and which of them serves each flag.
type layersResponse struct {
	// Inheritable reports whether the server applies inheritance.
	Inheritable bool `json:"inheritable"`
	// Layers holds the chamber itself, then each consulted parent, nearest first.
	Layers []layerView `json:"layers"`
	// Sources maps each flag served at the path to the path of the chamber
	// whose definition is served. It is null exactly when Error is set.
	Sources map[string]string `json:"sources"`
	// Error says why the inherited view cannot be built: the error the
	// inherited GET of the path returns.
	Error string `json:"error,omitempty"`
}

// layersView describes lin for a layers response.
func layersView(lin *storage.Lineage) layersResponse {
	resp := layersResponse{
		Inheritable: lin.Inheritable,
		Layers:      make([]layerView, 0, len(lin.Layers)),
	}
	if sources, err := lin.Sources(); err != nil {
		resp.Error = err.Error()
	} else {
		resp.Sources = sources
	}
	for _, l := range lin.Layers {
		v := layerView{Path: l.Path}
		switch {
		case l.Err != nil:
			v.Status, v.Error = layerInvalid, l.Err.Error()
		case l.Chamber == nil:
			v.Status = layerMissing
		default:
			v.Status, v.Rules = layerOK, l.Chamber.Rules
		}
		resp.Layers = append(resp.Layers, v)
	}
	return resp
}

// serveLayers serves GET /v1/chambers/<path>?inherit=layers, responding to
// errors with fail. The caller has already checked that p may read path and
// that path is not reserved.
func serveLayers(w http.ResponseWriter, r *http.Request, strg storage.Storage, p *auth.Principal, path string, fail func(int, error)) {
	ctx := r.Context()
	logger := logging.Ctx(ctx)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("realm.server.inherit", "layers"))

	if isWatchRequest(r) {
		fail(http.StatusBadRequest, errors.New("inherit=layers cannot be combined with watch=true"))
		return
	}
	// a client mistake, not a storage failure
	if err := storage.ValidatePath(path); err != nil {
		fail(http.StatusBadRequest, err)
		return
	}

	lin, err := storage.ReadLineage(ctx, strg, path)
	var nf *storage.NotFoundError
	switch {
	case errors.As(err, &nf):
		fail(http.StatusNotFound, nf)
		return
	case err != nil:
		// the cause is logged, not sent: it can name storage internals
		span.RecordError(err)
		logger.ErrorCtx(ctx).Err(err).Str("path", path).Msg("could not read the chamber's parents")
		fail(http.StatusInternalServerError, errors.New("could not read the chamber's parent chambers"))
		return
	}

	// The layers show each parent's own flags, including ones overridden
	// below it, so they need read access to every parent they list. The
	// list depends only on the path, never on what is stored.
	for _, l := range lin.Layers[1:] {
		if !p.CanRead(l.Path) {
			forbidden(w, r, fmt.Sprintf("not allowed to read the parent chambers of %s", path))
			return
		}
	}

	resp := layersView(lin)
	if resp.Error != "" {
		logger.WarnCtx(ctx).Str("path", path).Str("error", resp.Error).Msg("the inherited view cannot be built")
	}
	handleOkJSON(ctx, w, resp)
}
