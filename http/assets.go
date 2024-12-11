//go:build !slim
// +build !slim

package http

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
)

//go:embed realm-ui/dist/*
var content embed.FS

// webFS is a http Filesystem
func webFS() http.FileSystem {
	f, err := fs.Sub(content, "realm-ui/dist")
	if err != nil {
		panic(err)
	}
	return &UIAssetWrapper{FileSystem: http.FS(f)}
}

type UIAssetWrapper struct {
	FileSystem http.FileSystem
}

func (fsw *UIAssetWrapper) Open(name string) (http.File, error) {
	file, err := fsw.FileSystem.Open(name)
	if err == nil {
		return file, nil
	}
	// serve index.html instead of 404ing
	if errors.Is(err, fs.ErrNotExist) {
		file, err := fsw.FileSystem.Open("index.html")
		return file, err
	}
	return nil, err
}
