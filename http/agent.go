package http

import (
	"net/http"
	"net/url"
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
// route prefix (for example "/v1/chambers"), unescaped and with a trailing slash.
func logicalPath(req *http.Request, routePrefix string) string {
	p, _ := url.PathUnescape(strings.TrimPrefix(req.URL.Path, routePrefix))
	return utils.EnsureTrailingSlash(p)
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

// routeParam returns the unescaped remainder of the request path after
// routePrefix, without surrounding slashes ("/v1/keys/abc/" → "abc").
func routeParam(req *http.Request, routePrefix string) string {
	p, _ := url.PathUnescape(strings.Trim(strings.TrimPrefix(req.URL.Path, routePrefix), "/"))
	return p
}
