//go:build ui
// +build ui

package http

import (
	"embed"
	"io/fs"
)

//go:embed realm-ui/dist/*
var content embed.FS

// uiFiles returns the UI's build output.
func uiFiles() fs.FS {
	f, err := fs.Sub(content, "realm-ui/dist")
	if err != nil {
		panic(err)
	}
	return f
}
