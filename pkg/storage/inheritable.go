package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/hashicorp/go-hclog"
	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/utils"
)

type InheritableStorage struct {
	source Storage
}

var (
	_ Storage = (*InheritableStorage)(nil)
)

// NewInheritableStorage returns a InheritableStorage with the source Storage
func NewInheritableStorage(source Storage) (Storage, error) {
	return &InheritableStorage{
		source: source,
	}, nil
}

func (s *InheritableStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	logger := hclog.FromContext(ctx).ResetNamed("inheritable")
	logger.Debug("get operation", "logicalPath", logicalPath)

	if err := ValidatePath(logicalPath); err != nil {
		return nil, err
	}

	// ensure the leaf of the path exists before retrieving its parents
	leafEntry, err := s.source.Get(ctx, logicalPath)
	if err != nil {
		return nil, err
	}

	leaf := &realm.Chamber{}
	if err := json.Unmarshal([]byte(leafEntry.Value), leaf); err != nil {
		return nil, err
	}

	clean := path.Clean(logicalPath)
	dir := path.Dir(clean)

	// does the leaf contain parents?
	if dir != "/" {
		cur := "/"
		pathChunks := strings.Split(strings.TrimPrefix(dir, "/"), "/")

		c := &realm.Chamber{Rules: map[string]*realm.OverrideableRule{}}
		for _, v := range pathChunks {
			cur += utils.EnsureTrailingSlash(v)
			entry, err := s.source.Get(ctx, cur)
			if err != nil {
				continue
			}

			curChamber := &realm.Chamber{Rules: map[string]*realm.OverrideableRule{}}
			if err := json.Unmarshal([]byte(entry.Value), curChamber); err != nil {
				continue
			}
			inheritWith(curChamber, c)
			c = curChamber
		}

		// inherit all of the parents
		inheritWith(leaf, c)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	buf := new(bytes.Buffer)
	if err := utils.WriteInterfaceWith(buf, leaf, false); err != nil {
		return nil, err
	}

	return &StorageEntry{Key: logicalPath, Value: buf.Bytes()}, nil
}

func (s *InheritableStorage) Put(ctx context.Context, e StorageEntry) error {
	logger := hclog.FromContext(ctx).ResetNamed("inheritable")
	logger.Debug("put operation", "logicalPath", e.Key)

	if err := ValidatePath(e.Key); err != nil {
		return err
	}

	if err := s.source.Put(ctx, e); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	return nil
}

func (s *InheritableStorage) Delete(ctx context.Context, logicalPath string) error {
	logger := hclog.FromContext(ctx).ResetNamed("inheritable")
	logger.Debug("delete operation", "logicalPath", logicalPath)

	if err := ValidatePath(logicalPath); err != nil {
		return err
	}

	if err := s.source.Delete(ctx, logicalPath); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	return nil
}

func (s *InheritableStorage) List(ctx context.Context, prefix string) ([]string, error) {
	logger := hclog.FromContext(ctx).ResetNamed("inheritable")
	logger.Debug("list operation", "prefix", prefix)

	if err := ValidatePath(prefix); err != nil {
		return nil, err
	}

	names, err := s.source.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	return names, nil
}

func inheritWith(base *realm.Chamber, inheritedFrom *realm.Chamber) {
	for key := range inheritedFrom.Rules {
		if _, ok := base.Rules[key]; !ok {
			base.Rules[key] = inheritedFrom.Rules[key]
		}
	}
}
