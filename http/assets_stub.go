//go:build !ui
// +build !ui

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
