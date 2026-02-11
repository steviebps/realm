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

func buildAgentRequest(req *http.Request) *AgentRequest {
	p, _ := url.PathUnescape(strings.TrimPrefix(req.URL.Path, "/v1/chambers"))
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

	Path := utils.EnsureTrailingSlash(p)

	return &AgentRequest{
		Request:   req,
		ID:        uuid.New().String(),
		Operation: op,
		Path:      Path,
	}
}
