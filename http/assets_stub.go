//go:build slim
// +build slim

package http

import (
	"net/http"
)

func init() {
	uiExists = false
}

// webFS is a stub
func webFS() http.FileSystem {
	return nil
}
