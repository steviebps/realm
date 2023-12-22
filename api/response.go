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
