package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/steviebps/realm/utils"
)

type Operation string

const (
	PutOperation    Operation = "put"
	PatchOperation  Operation = "patch"
	GetOperation    Operation = "get"
	DeleteOperation Operation = "delete"
	ListOperation   Operation = "list"
)

type AgentRequest struct {
	*http.Request
	ID        string
	Operation Operation
	Path      string
}

// logicalPath returns the chamber path addressed by a request under the given
// route prefix (for example "/v1/chambers"), in canonical form: rooted, with a
// trailing slash, and without empty or "." segments. The router has already
// unescaped the URL path once; it is not unescaped again, so an encoded
// "%2F" cannot smuggle in a path (such as "//_realm/") that bypasses the
// reserved-prefix check and is only cleaned up later by a storage backend.
// ".." segments are kept, for storage to reject.
func logicalPath(req *http.Request, routePrefix string) string {
	return utils.CanonicalPath(strings.TrimPrefix(req.URL.Path, routePrefix))
}

func buildAgentRequest(req *http.Request) *AgentRequest {
	var op Operation

	switch req.Method {
	case http.MethodGet:
		op = GetOperation
		listStr := req.URL.Query().Get("list")
		if listStr != "" {
			list, _ := strconv.ParseBool(listStr)
			if list {
				op = ListOperation
			}
		}
	case http.MethodPost:
		op = PutOperation
	case http.MethodPatch:
		op = PatchOperation
	case http.MethodDelete:
		op = DeleteOperation
	case "LIST":
		op = ListOperation
	}

	return &AgentRequest{
		Request:   req,
		ID:        uuid.New().String(),
		Operation: op,
		Path:      logicalPath(req, "/v1/chambers"),
	}
}

// routeParam returns the remainder of the (already unescaped) request path
// after routePrefix, without surrounding slashes ("/v1/keys/abc/" → "abc").
func routeParam(req *http.Request, routePrefix string) string {
	return strings.Trim(strings.TrimPrefix(req.URL.Path, routePrefix), "/")
}
