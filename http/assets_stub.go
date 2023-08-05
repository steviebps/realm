//go:build !ui

package http

import (
	"net/http"
)

// webFS is a stub
func webFS() http.FileSystem {
	return nil
}
