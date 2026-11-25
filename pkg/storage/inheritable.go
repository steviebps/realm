package storage

import (
	"bytes"
	"context"
	"path"
	"strings"

	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type InheritableStorage struct {
	source Storage
	tracer trace.Tracer
	// cache keeps the chambers it parsed and the views it encoded, so that
	// reads of unchanged chambers do neither again.
	cache *viewCache
}

var (
	_ Storage = (*InheritableStorage)(nil)
	_ Layered = (*InheritableStorage)(nil)
)

// NewInheritableStorage returns a InheritableStorage with the source Storage
func NewInheritableStorage(source Storage) (Storage, error) {
	return &InheritableStorage{
		source: source,
		tracer: otel.Tracer("github.com/steviebps/realm"),
		cache:  newViewCache(maxParsedBytes, maxViewBytes),
	}, nil
}

// Get gets the entry at the specified logical path, inheriting all parent
// chambers if they exist. The entry's Version is the revision of the
// inherited view (see Lineage.Revision): the highest among the chamber and
// the parents it merges.
func (s *InheritableStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := s.tracer.Start(ctx, "InheritableStorage Get", trace.WithAttributes(attribute.String("realm.inheritable.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	lin, err := s.lineage(ctx, logicalPath)
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	// a view merged from the very chambers just read encodes the same
	if value, ok := s.cache.view(lin); ok {
		span.SetAttributes(attribute.Bool("realm.inheritable.cached", true))
		return &StorageEntry{Key: logicalPath, Value: bytes.Clone(value), Version: lin.Revision()}, nil
	}
	merged, err := lin.Merge()
	if err != nil {
		recordError(span, err)
		return nil, err
	}

	buf := new(bytes.Buffer)
	if err := utils.WriteInterfaceWith(buf, merged, false); err != nil {
		recordError(span, err)
		return nil, err
	}
	s.cache.storeView(lin, bytes.Clone(buf.Bytes()))

	return &StorageEntry{Key: logicalPath, Value: buf.Bytes(), Version: lin.Revision()}, nil
}

// Layers returns the chamber at logicalPath and each ancestor its inherited
// view consults, nearest first. Get is built from the same lineage, so the
// two cannot disagree.
func (s *InheritableStorage) Layers(ctx context.Context, logicalPath string) (*Lineage, error) {
	ctx, span := s.tracer.Start(ctx, "InheritableStorage Layers", trace.WithAttributes(attribute.String("realm.inheritable.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("layers operation")

	lin, err := s.lineage(ctx, logicalPath)
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("realm.inheritable.layers", len(lin.Layers)))
	return lin, nil
}

// consultedAncestors returns the ancestors of logicalPath whose flags its
// inherited view includes, farthest first: the root chamber "/", then each
// chamber along the path. The root chamber itself has none.
func consultedAncestors(logicalPath string) []string {
	clean := path.Clean(logicalPath)
	if clean == "/" {
		return nil
	}
	ancestors := []string{"/"}
	dir := path.Dir(clean)
	if dir == "/" {
		return ancestors
	}
	cur := "/"
	for v := range strings.SplitSeq(strings.TrimPrefix(dir, "/"), "/") {
		cur += utils.EnsureTrailingSlash(v)
		ancestors = append(ancestors, cur)
	}
	return ancestors
}

// lineage reads the chamber at logicalPath and each ancestor its inherited
// view consults (see storedLineage), reusing the chambers it parsed before.
// It returns a NotFoundError, without reading the ancestors, when there is no
// chamber at logicalPath.
func (s *InheritableStorage) lineage(ctx context.Context, logicalPath string) (*Lineage, error) {
	return storedLineage(ctx, logicalPath, s.source.Get, s.cache.parseLayer, s.readLayer, true)
}

// readLayer reads the chamber at p from the source as ReadLayer does,
// reusing the chamber last parsed at p when it is unchanged.
func (s *InheritableStorage) readLayer(ctx context.Context, p string) (Layer, error) {
	return readLayerWith(ctx, s.source, p, s.cache.parseLayer)
}

// Put puts the entry at the specified logical path, creating or overwriting it
func (s *InheritableStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := s.tracer.Start(ctx, "InheritableStorage Put", trace.WithAttributes(attribute.String("realm.inheritable.entry.key", e.Key)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	if err := ValidatePath(e.Key); err != nil {
		recordError(span, err)
		return err
	}

	if err := s.source.Put(ctx, e); err != nil {
		recordError(span, err)
		return err
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	return nil
}

// Delete deletes the entry at the specified logical path
func (s *InheritableStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := s.tracer.Start(ctx, "InheritableStorage Delete", trace.WithAttributes(attribute.String("realm.inheritable.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return err
	}

	if err := s.source.Delete(ctx, logicalPath); err != nil {
		recordError(span, err)
		return err
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return ctx.Err()
	default:
	}

	return nil
}

// List lists all keys with the specified prefix
func (s *InheritableStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := s.tracer.Start(ctx, "InheritableStorage List", trace.WithAttributes(attribute.String("realm.inheritable.logicalPath", prefix)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := ValidatePath(prefix); err != nil {
		recordError(span, err)
		return nil, err
	}

	names, err := s.source.List(ctx, prefix)
	if err != nil {
		recordError(span, err)
		return nil, err
	}

	select {
	case <-ctx.Done():
		recordError(span, ctx.Err())
		return nil, ctx.Err()
	default:
	}

	return names, nil
}

// Unwrap returns the source storage, which holds chambers exactly as written
// (without inheritance applied).
func (s *InheritableStorage) Unwrap() Storage {
	return s.source
}

// Close closes the source storage
func (s *InheritableStorage) Close(ctx context.Context) error {
	return s.source.Close(ctx)
}
