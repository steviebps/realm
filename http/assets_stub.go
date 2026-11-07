//go:build !ui
// +build !ui

package http

import "io/fs"

func init() {
	uiExists = false
}

// uiFiles is a stub: the binary was built without the UI.
func uiFiles() fs.FS {
	return nil
}
