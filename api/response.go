package api

import "encoding/json"

type HTTPErrorResponse struct {
	Errors []string `json:"errors"`
}

type HTTPResponse struct {
	Data json.RawMessage `json:"data"`
}

type HTTPErrorAndDataResponse struct {
	Data   json.RawMessage `json:"data,omitempty"`
	Errors []string        `json:"errors,omitempty"`
}

// RevisionHeader names the response header that carries the storage revision
// of a chamber read or an evaluation: the highest revision among the chamber
// and the parents its inherited view merges. Watch streams carry the same
// revision as each event's id. It is absent when the storage does not track
// revisions (only transactional backends do).
const RevisionHeader = "X-Realm-Revision"
