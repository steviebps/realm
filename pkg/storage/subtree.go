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
// When the innermost storage (see Raw) is a Scanner, ListTree reads every key
// below root with one ScanKeys and derives the paths that only lead to
// chambers from them; otherwise it lists each level of the tree with List.
// The paths are the same either way.
//
// When limit is positive and more than limit paths lie below root, ListTree
// stops listing and returns an error wrapping ErrSubtreeTooLarge. A path that
// only leads to chambers below it counts toward the limit too: which paths
// hold a chamber is known only once they are read.
func ListTree(ctx context.Context, s Storage, root string, limit int) ([]string, error) {
	ctx, span := otel.Tracer("github.com/steviebps/realm").Start(ctx, "ListTree", trace.WithAttributes(attribute.String("realm.subtree.root", root)))
	defer span.End()

	var paths []string
	var err error
	if sc, ok := Raw(s).(Scanner); ok {
		span.SetAttributes(attribute.Bool("realm.subtree.scan", true))
		paths, err = scanTree(ctx, sc, root, limit)
	} else {
		paths, err = listTree(ctx, s, root, limit)
	}
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("realm.subtree.paths", len(paths)))
	return paths, nil
}

// subtreeTooLarge returns the error ListTree returns when more than limit
// paths lie below root.
func subtreeTooLarge(root string, limit int) error {
	return fmt.Errorf("%w: %s has more than %d chambers below it", ErrSubtreeTooLarge, root, limit)
}

// scanTree lists the subtree at root from the keys one ScanKeys yields (see
// ListTree). Keys arrive in byte order, which is the order ListTree returns:
// a path that leads to a key is a prefix of it, so it sorts before the key,
// and after every earlier key it does not lead to. So each path a key leads
// to is new exactly when it sorts after the last path listed.
//
// A key's segments are read as List names them: an empty or "." segment
// (which listTree skips, with everything below it) ends the key's paths. So
// does a final segment without a trailing slash, which realm never writes:
// the paths derived from a key are prefixes of it, which the order relies on.
func scanTree(ctx context.Context, sc Scanner, root string, limit int) ([]string, error) {
	paths := []string{root}
	for key, err := range sc.ScanKeys(ctx, root) {
		if err != nil {
			return nil, err
		}
		rest, ok := strings.CutPrefix(key, root)
		if !ok || (root == "/" && IsReservedPath(key)) {
			continue
		}
		p := root
		for {
			seg, more, nested := strings.Cut(rest, "/")
			if !nested || seg == "" || seg == "." {
				break
			}
			p += seg + "/"
			rest = more
			if p > paths[len(paths)-1] {
				paths = append(paths, p)
				if limit > 0 && len(paths)-1 > limit {
					return nil, subtreeTooLarge(root, limit)
				}
			}
		}
	}
	return paths, nil
}

// listTree lists the subtree at root one level at a time (see ListTree).
func listTree(ctx context.Context, s Storage, root string, limit int) ([]string, error) {
	paths := []string{root}
	for i := 0; i < len(paths); i++ {
		names, err := s.List(ctx, paths[i])
		if err != nil && !errors.Is(err, os.ErrNotExist) {
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
				return nil, subtreeTooLarge(root, limit)
			}
		}
	}
	// Canonical paths end with "/", which sorts after every character a
	// name may hold but those below it ("-", ".", ...), so sorting the whole
	// paths puts each parent right before its children.
	slices.Sort(paths[1:])
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
	return walkTree(ctx, read, inheritable, paths, nil)
}

// ReadTree yields the view of each path in paths, which ListTree returned for
// s, as WalkTree does with RawLayers(s): the same views, with the same
// errors and revisions, in the same order.
//
// When the innermost storage (see Raw) is a Scanner, ReadTree reads the
// chambers at and below the root with one Scan instead of a Get each, once it
// has read the root's ancestors, which lie outside the scan, with Get. It
// matches the scanned entries to paths as they arrive (both are in byte
// order), and keeps only the chambers above the root and along the path
// being walked, as WalkTree does; how much of the subtree the scan itself
// holds at once is up to the backend (see each one's Scan). As with WalkTree,
// a chamber created after ListTree listed the paths is left out, and one
// deleted since is not Stored. A failed read of the root's ancestors is
// reported before a failed scan.
func ReadTree(ctx context.Context, s Storage, paths []string) iter.Seq2[View, error] {
	var scan func(context.Context, string) iter.Seq2[*StorageEntry, error]
	if sc, ok := Raw(s).(Scanner); ok {
		scan = sc.Scan
	}
	return walkTree(ctx, RawLayers(s), IsInheritable(s), paths, scan)
}

// walkTree is WalkTree, reading the chambers at and below the root with scan
// when it is not nil (see ReadTree).
func walkTree(ctx context.Context, read LayerReader, inheritable bool, paths []string, scan func(context.Context, string) iter.Seq2[*StorageEntry, error]) iter.Seq2[View, error] {
	return func(yield func(View, error) bool) {
		if len(paths) == 0 {
			return
		}
		ctx, span := otel.Tracer("github.com/steviebps/realm").Start(ctx, "WalkTree", trace.WithAttributes(attribute.String("realm.subtree.root", paths[0]), attribute.Int("realm.subtree.paths", len(paths)), attribute.Bool("realm.subtree.scan", scan != nil)))
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
		fail := func(p string, err error) {
			recordError(span, err)
			yield(View{Path: p}, err)
		}

		// scanned returns the layer at p from the scan, when there is one
		var scanned func(p string) (Layer, error)
		if scan != nil {
			// Read the ancestors before scanning: a scan may hold the
			// connection another read would wait for until it ends.
			if err := ValidatePath(root); err != nil {
				fail(root, err)
				return
			}
			if inheritable {
				for _, a := range consultedAncestors(root) {
					if _, err := cached(ctx, a); err != nil {
						fail(root, err)
						return
					}
				}
			}
			next, stop := iter.Pull2(scan(ctx, root))
			defer stop()
			var cur *StorageEntry
			done := false
			scanned = func(p string) (Layer, error) {
				// entries sorting before p have no path: they were written
				// after the paths were listed
				for !done && (cur == nil || cur.Key < p) {
					e, err, ok := next()
					switch {
					case !ok:
						done, cur = true, nil
					case err != nil:
						return Layer{}, err
					default:
						cur = e
					}
				}
				if cur != nil && cur.Key == p {
					return parseLayer(p, cur), nil
				}
				return Layer{Path: p}, nil
			}
		}

		for i, p := range paths {
			for len(chain) > 0 && !strings.HasPrefix(p, chain[len(chain)-1]) {
				delete(known, chain[len(chain)-1])
				chain = chain[:len(chain)-1]
			}
			// a path out of order (not as ListTree returns them) is read on
			// its own, through read
			if scanned != nil && (i == 0 || p > paths[i-1]) {
				l, err := scanned(p)
				if err != nil {
					fail(p, err)
					return
				}
				known[p] = l
			}
			v, err := BuildView(ctx, cached, p, inheritable)
			if err != nil {
				fail(p, err)
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
