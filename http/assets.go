package http

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed realm-ui/dist/static/*
var content embed.FS

// webFS is a http Filesystem
func webFS() http.FileSystem {
	f, err := fs.Sub(content, "realm-ui/dist/static")
	if err != nil {
		panic(err)
	}
	return http.FS(f)
}
