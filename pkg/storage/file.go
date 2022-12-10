package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/utils"
)

type RealmFile struct {
	rootPath string
}

var (
	_ Storage = (*RealmFile)(nil)
)

func NewRealmFile(path string) (*RealmFile, error) {
	if path == "" {
		return nil, fmt.Errorf("'path' must be set")
	}

	return &RealmFile{
		rootPath: path,
	}, nil
}

func (f *RealmFile) Get(ctx context.Context, k string) (*realm.Chamber, error) {
	if err := f.validatePath(k); err != nil {
		return nil, err
	}
	path, key := f.expandPath(k)
	file, err := os.OpenFile(filepath.Join(path, key), os.O_RDONLY, 0600)
	if err != nil {
		return nil, err
	}
	if file != nil {
		defer file.Close()
	}

	var c realm.Chamber
	if err := utils.ReadInterfaceWith(file, &c); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	return &c, nil
}

func (f *RealmFile) Put(ctx context.Context, c *realm.Chamber) error {
	if err := f.validatePath(c.Name); err != nil {
		return err
	}
	path, key := f.expandPath(c.Name)

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Make the parent tree
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}

	file, err := os.OpenFile(filepath.Join(path, key), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if file != nil {
		defer file.Close()
	}

	return utils.WriteInterfaceWith(file, c, false)
}

func (f *RealmFile) Delete(ctx context.Context, k string) error {
	if err := f.validatePath(k); err != nil {
		return err
	}
	path, key := f.expandPath(k)

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if err := os.Remove(filepath.Join(path, key)); err != nil {
		return err
	}

	return nil
}

func (f *RealmFile) List(ctx context.Context, prefix string) ([]string, error) {
	if err := f.validatePath(prefix); err != nil {
		return nil, err
	}

	path := f.rootPath
	if prefix != "" {
		path = filepath.Join(path, prefix)
	}

	file, err := os.Open(path)
	if file != nil {
		defer file.Close()
	}
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}

	names, err := file.Readdirnames(-1)
	if err != nil {
		return nil, err
	}

	for i, name := range names {
		fi, err := os.Stat(filepath.Join(path, name))
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			names[i] = name + "/"
		} else {
			if name[0] == '_' {
				names[i] = name[1:]
			}
		}
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if len(names) > 0 {
		sort.Strings(names)
	}

	return names, nil
}

func (f *RealmFile) validatePath(path string) error {
	switch {
	case strings.Contains(path, ".."):
		return errors.New("path cannot reference parents")
	}

	return nil
}

func (f *RealmFile) expandPath(k string) (string, string) {
	path := filepath.Join(f.rootPath, k)
	key := filepath.Base(path)
	path = filepath.Dir(path)
	return path, "_" + key
}
