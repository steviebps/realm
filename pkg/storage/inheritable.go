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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type InheritableStorage struct {
	source Storage
	tracer trace.Tracer
}

var (
	_ Storage = (*InheritableStorage)(nil)
)

// NewInheritableStorage returns a InheritableStorage with the source Storage
func NewInheritableStorage(source Storage) (Storage, error) {
	return &InheritableStorage{
		source: source,
		tracer: otel.Tracer("github.com/steviebps/realm"),
	}, nil
}

func (s *InheritableStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	logger := hclog.FromContext(ctx).ResetNamed("inheritable")
	ctx, span := s.tracer.Start(ctx, "InheritableStorage Get", trace.WithAttributes(attribute.String("realm.inheritable.logicalPath", logicalPath)))
	defer span.End()
	logger.Debug("get operation", "logicalPath", logicalPath)

	if err := ValidatePath(logicalPath); err != nil {
		span.RecordError(err)
		return nil, err
	}

	// ensure the last entry of the path exists before retrieving its parents
	leafEntry, err := s.source.Get(ctx, logicalPath)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	leaf := &realm.Chamber{}
	if err := json.Unmarshal(leafEntry.Value, leaf); err != nil {
		span.RecordError(err)
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
				span.RecordError(err)
				continue
			}

			curChamber := &realm.Chamber{Rules: map[string]*realm.OverrideableRule{}}
			if err := json.Unmarshal(entry.Value, curChamber); err != nil {
				span.RecordError(err)
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
		span.RecordError(ctx.Err())
		return nil, ctx.Err()
	default:
	}

	buf := new(bytes.Buffer)
	if err := utils.WriteInterfaceWith(buf, leaf, false); err != nil {
		span.RecordError(err)
		return nil, err
	}

	return &StorageEntry{Key: logicalPath, Value: buf.Bytes()}, nil
}

func (s *InheritableStorage) Put(ctx context.Context, e StorageEntry) error {
	logger := hclog.FromContext(ctx).ResetNamed("inheritable")
	ctx, span := s.tracer.Start(ctx, "InheritableStorage Put", trace.WithAttributes(attribute.String("realm.inheritable.entry.key", e.Key)))
	defer span.End()
	logger.Debug("put operation", "logicalPath", e.Key)

	if err := ValidatePath(e.Key); err != nil {
		span.RecordError(err)
		return err
	}

	if err := s.source.Put(ctx, e); err != nil {
		span.RecordError(err)
		return err
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return ctx.Err()
	default:
	}

	return nil
}

func (s *InheritableStorage) Delete(ctx context.Context, logicalPath string) error {
	logger := hclog.FromContext(ctx).ResetNamed("inheritable")
	ctx, span := s.tracer.Start(ctx, "InheritableStorage Delete", trace.WithAttributes(attribute.String("realm.inheritable.logicalPath", logicalPath)))
	defer span.End()
	logger.Debug("delete operation", "logicalPath", logicalPath)

	if err := ValidatePath(logicalPath); err != nil {
		span.RecordError(err)
		return err
	}

	if err := s.source.Delete(ctx, logicalPath); err != nil {
		span.RecordError(err)
		return err
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		return ctx.Err()
	default:
	}

	return nil
}

func (s *InheritableStorage) List(ctx context.Context, prefix string) ([]string, error) {
	logger := hclog.FromContext(ctx).ResetNamed("inheritable")
	ctx, span := s.tracer.Start(ctx, "InheritableStorage List", trace.WithAttributes(attribute.String("realm.inheritable.logicalPath", prefix)))
	defer span.End()
	logger.Debug("list operation", "prefix", prefix)

	if err := ValidatePath(prefix); err != nil {
		span.RecordError((err))
		return nil, err
	}

	names, err := s.source.List(ctx, prefix)
	if err != nil {
		span.RecordError((err))
		return nil, err
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
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
