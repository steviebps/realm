package storage

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"slices"
	"strings"

	realm "github.com/steviebps/realm/pkg"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ErrSubtreeTooLarge is wrapped by the error ListTree returns for a subtree
// with more paths than its limit allows.
var ErrSubtreeTooLarge = errors.New("the subtree holds too many chambers")

// ListTree returns root and every path below it that holds or leads to a
// chamber, as s lists them, in the order a subtree is read: root first, then
// parents before children, with siblings sorted by path (a pre-order walk).
// root must be canonical (rooted, with a trailing slash), and so are the
// paths returned. At "/", realm's reserved paths (see ReservedPrefix) are left
// out.
//
// When limit is positive and more than limit paths lie below root, ListTree
// stops listing and returns an error wrapping ErrSubtreeTooLarge. A path that
// only leads to chambers below it counts toward the limit too: which paths
// hold a chamber is known only once they are read.
func ListTree(ctx context.Context, s Storage, root string, limit int) ([]string, error) {
	ctx, span := otel.Tracer("github.com/steviebps/realm").Start(ctx, "ListTree", trace.WithAttributes(attribute.String("realm.subtree.root", root)))
	defer span.End()

	paths := []string{root}
	for i := 0; i < len(paths); i++ {
		names, err := s.List(ctx, paths[i])
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			recordError(span, err)
			return nil, err
		}
		for _, name := range names {
			// backends name a child "a" or "a/", and list a chamber's own
			// entry as "."
			name = strings.TrimSuffix(name, "/")
			if name == "" || name == "." {
				continue
			}
			p := paths[i] + name + "/"
			// realm's metadata lives only beneath the root; a chamber named
			// "_realm" anywhere else is an ordinary one
			if paths[i] == "/" && IsReservedPath(p) {
				continue
			}
			paths = append(paths, p)
			if limit > 0 && len(paths)-1 > limit {
				err := fmt.Errorf("%w: %s has more than %d chambers below it", ErrSubtreeTooLarge, root, limit)
				recordError(span, err)
				return nil, err
			}
		}
	}
	// Canonical paths end with "/", which sorts after every character a
	// name may hold but those below it ("-", ".", ...), so sorting the whole
	// paths puts each parent right before its children.
	slices.Sort(paths[1:])
	span.SetAttributes(attribute.Int("realm.subtree.paths", len(paths)))
	return paths, nil
}

// LayerReader returns the chamber stored at the canonical path p as a Layer,
// as ReadLayer does. Readers may share what they read, for example among the
// watch streams that a change wakes at once.
type LayerReader func(ctx context.Context, p string) (Layer, error)

// ReadLayer reads the chamber stored at the canonical path p from s, which
// should hold chambers as written (see Raw). When no chamber is stored there,
// the layer has neither Chamber nor Err; when it cannot be parsed, Err is a
// ChamberError. A failed read is returned as an error.
func ReadLayer(ctx context.Context, s Storage, p string) (Layer, error) {
	entry, err := s.Get(ctx, p)
	if err != nil {
		if isNotFoundErr(err) {
			return Layer{Path: p}, nil
		}
		return Layer{}, err
	}
	return parseLayer(p, entry), nil
}

// RawLayers returns a LayerReader that reads each chamber from Raw(s).
func RawLayers(s Storage) LayerReader {
	raw := Raw(s)
	return func(ctx context.Context, p string) (Layer, error) {
		return ReadLayer(ctx, raw, p)
	}
}

// IsInheritable reports whether s applies inheritance to the chambers it
// reads (see Layered).
func IsInheritable(s Storage) bool {
	_, ok := s.(Layered)
	return ok
}

// View is the view of one path of a subtree: the chamber a plain read of the
// path returns.
type View struct {
	// Path is the view's canonical path.
	Path string
	// Stored reports whether a chamber is stored at Path. Without one, the
	// view is built from the chambers above Path alone.
	Stored bool
	// Chamber is the view: the chamber at Path with the flags it inherits
	// merged in, or the chamber as stored when the storage does not apply
	// inheritance. It is nil when Err is set.
	Chamber *realm.Chamber
	// Revision is the view's revision (see Lineage.Revision); 0 when the
	// storage does not track revisions.
	Revision uint64
	// Err says why the view cannot be built: the chamber, or one it
	// inherits from, cannot be parsed. It is the error InheritableStorage.Get
	// returns for Path, and wraps a ChamberError.
	Err error
}

// BuildView builds the view of the canonical path p from the chambers read
// returns. With inheritable, it merges p's chamber with every ancestor it
// consults, exactly as InheritableStorage.Get does; otherwise the view is
// p's chamber alone. When no chamber is stored at p, the view is built from
// its ancestors alone (Stored is false), and is empty without any.
//
// A chamber that cannot be parsed sets the view's Err; a failed read, or a
// path storage refuses (see ValidatePath), is returned as an error.
func BuildView(ctx context.Context, read LayerReader, p string, inheritable bool) (View, error) {
	if err := ValidatePath(p); err != nil {
		return View{}, err
	}
	own, err := read(ctx, p)
	if err != nil {
		return View{}, err
	}
	lin := Lineage{Inheritable: inheritable, Layers: []Layer{own}}
	if inheritable {
		// read farthest first, as InheritableStorage.Get does, so a failed
		// read reports the same ancestor
		for _, a := range consultedAncestors(p) {
			layer, err := read(ctx, a)
			if err != nil {
				return View{}, err
			}
			if layer.Err != nil {
				layer.Err = fmt.Errorf("could not parse parent chamber %s: %w", a, layer.Err)
			}
			lin.Layers = append(lin.Layers, layer)
		}
		slices.Reverse(lin.Layers[1:])
	}
	v := View{Path: p, Stored: own.Chamber != nil || own.Err != nil, Revision: lin.Revision()}
	v.Chamber, v.Err = lin.Merge()
	return v, nil
}

// WalkTree yields the view of each path in paths, which ListTree returned:
// the root's view always, then the view of each path below it that holds a
// chamber, parents before children. It reads each chamber through read at
// most once, keeping those above the root and along the path being walked,
// and merges every view from them in memory. A view that cannot be built is
// yielded with its Err, and the walk carries on; a failed read ends it with
// that error.
func WalkTree(ctx context.Context, read LayerReader, inheritable bool, paths []string) iter.Seq2[View, error] {
	return func(yield func(View, error) bool) {
		if len(paths) == 0 {
			return
		}
		ctx, span := otel.Tracer("github.com/steviebps/realm").Start(ctx, "WalkTree", trace.WithAttributes(attribute.String("realm.subtree.root", paths[0]), attribute.Int("realm.subtree.paths", len(paths))))
		defer span.End()

		root := paths[0]
		// known holds the layers read so far that later paths may need: those
		// above the root, and those along chain, the path walked to now
		known := map[string]Layer{}
		var chain []string
		cached := func(ctx context.Context, p string) (Layer, error) {
			if l, ok := known[p]; ok {
				return l, nil
			}
			l, err := read(ctx, p)
			if err == nil {
				known[p] = l
			}
			return l, err
		}
		for _, p := range paths {
			for len(chain) > 0 && !strings.HasPrefix(p, chain[len(chain)-1]) {
				delete(known, chain[len(chain)-1])
				chain = chain[:len(chain)-1]
			}
			v, err := BuildView(ctx, cached, p, inheritable)
			if err != nil {
				recordError(span, err)
				yield(View{Path: p}, err)
				return
			}
			chain = append(chain, p)
			if p != root && !v.Stored {
				continue // a path that only leads to chambers below it
			}
			if !yield(v, nil) {
				return
			}
		}
	}
}
