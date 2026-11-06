package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
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
	}, nil
}

// Get gets the entry at the specified logical path, inheriting all parent chambers if they exist
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

	return &StorageEntry{Key: logicalPath, Value: buf.Bytes()}, nil
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
	// probe with a hypothetical child so the rule lives only in consultedAncestors
	self := lin.Layers[0].Path
	lin.InheritedByChildren = slices.Contains(consultedAncestors(self+"_/"), self)
	span.SetAttributes(attribute.Int("realm.inheritable.layers", len(lin.Layers)))
	return lin, nil
}

// consultedAncestors returns the ancestors of logicalPath whose flags its
// inherited view includes, farthest first. Every ancestor is consulted except
// the root chamber "/", which is never inherited.
func consultedAncestors(logicalPath string) []string {
	dir := path.Dir(path.Clean(logicalPath))
	if dir == "/" {
		return nil
	}
	var ancestors []string
	cur := "/"
	for _, v := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		cur += utils.EnsureTrailingSlash(v)
		ancestors = append(ancestors, cur)
	}
	return ancestors
}

// lineage reads the chamber at logicalPath and each ancestor its inherited
// view consults; InheritedByChildren is left for Layers. Ancestors are read
// farthest first; one that does not exist
// is recorded without a chamber, and one that fails to read aborts the read
// so that its flags are never silently dropped. A chamber that cannot be
// parsed is recorded with its error, which Lineage.Merge reports.
func (s *InheritableStorage) lineage(ctx context.Context, logicalPath string) (*Lineage, error) {
	if err := ValidatePath(logicalPath); err != nil {
		return nil, err
	}

	// ensure the last entry of the path exists before retrieving its parents
	leafEntry, err := s.source.Get(ctx, logicalPath)
	if err != nil {
		return nil, err
	}

	layers := []Layer{parseLayer(canonicalChamberPath(logicalPath), leafEntry.Value)}
	for _, p := range consultedAncestors(logicalPath) {
		entry, err := s.source.Get(ctx, p)
		if err != nil {
			// an ancestor that does not exist contributes nothing, but a
			// failed read must not silently drop the rules it holds
			var nfError *NotFoundError
			if errors.As(err, &nfError) {
				layers = append(layers, Layer{Path: p})
				continue
			}
			return nil, err
		}
		layer := parseLayer(p, entry.Value)
		if layer.Err != nil {
			// skipping it would silently drop its flags (kill switches
			// included) from every chamber below it
			layer.Err = fmt.Errorf("could not parse parent chamber %s: %w", p, layer.Err)
		}
		layers = append(layers, layer)
	}
	// read farthest first, as the walk always has; lineages are nearest first
	slices.Reverse(layers[1:])

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return &Lineage{Inheritable: true, Layers: layers}, nil
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
