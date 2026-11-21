package storage

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"path"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/utils"
)

// Layer is one chamber that a path's inherited view is built from.
type Layer struct {
	// Path is the chamber's canonical path, with a trailing slash.
	Path string
	// Chamber is the chamber as stored; nil when there is none at Path or it
	// cannot be parsed. It may be shared with other reads (see
	// InheritableStorage), so it must not be modified.
	Chamber *realm.Chamber
	// Err says why the chamber cannot be parsed. The inherited view cannot be
	// built while any layer has one.
	Err error
	// Version is the storage revision that last wrote the chamber (see
	// StorageEntry.Version); 0 when there is none at Path or the storage
	// does not track revisions.
	Version uint64
}

// Lineage is what the inherited view of a path is built from: the chamber
// itself, then each ancestor the view consults, nearest first.
type Lineage struct {
	// Inheritable reports whether the storage applies inheritance. When it is
	// false, Layers holds only the chamber itself.
	Inheritable bool
	// Layers holds the chamber, then each consulted ancestor, nearest first,
	// including ancestors with no chamber.
	Layers []Layer
}

// Layered is implemented by storage whose Get applies inheritance, so that
// callers can see the chambers an inherited view is built from.
type Layered interface {
	// Layers returns the lineage of the chamber at logicalPath, or a
	// NotFoundError when there is no chamber there.
	Layers(ctx context.Context, logicalPath string) (*Lineage, error)
	// Ancestry returns what the ancestors of logicalPath say about the flags
	// a chamber there may define, whether or not one exists there.
	Ancestry(ctx context.Context, logicalPath string) (*Ancestry, error)
}

// Ancestry is what the chambers above a path say about the flags a chamber
// there may define: which flags they seal, and which they constrain.
// Ancestors that are missing or cannot be parsed say nothing: one that cannot
// be parsed fails every inherited read below it until it is fixed.
type Ancestry struct {
	// Seals maps each flag an ancestor seals to the path of the farthest
	// ancestor that seals it, as Lineage.Merge resolves seals: a chamber at
	// the path cannot define those flags.
	Seals map[string]string
	// Constraints maps each flag that ancestors define with constraints to
	// those definitions, farthest first: a definition at the path must have
	// the type of each, and values each one's constraints allow.
	Constraints map[string][]Constrained
}

// Constrained is an ancestor's definition of a flag that has constraints.
type Constrained struct {
	// Path is the ancestor's path.
	Path string
	// Flag is the ancestor's definition. It may be shared with other reads,
	// so it must not be modified.
	Flag *realm.Flag
}

// ancestryOf returns what layers, the ancestors of a path farthest first,
// say about the flags a chamber at the path may define.
func ancestryOf(layers []Layer) *Ancestry {
	a := &Ancestry{Seals: map[string]string{}, Constraints: map[string][]Constrained{}}
	for _, l := range layers {
		if l.Chamber == nil || l.Err != nil {
			continue
		}
		for key, flag := range l.Chamber.Rules {
			if flag == nil {
				continue
			}
			if _, ok := a.Seals[key]; flag.Sealed && !ok {
				a.Seals[key] = l.Path
			}
			if len(flag.Constraints) > 0 {
				a.Constraints[key] = append(a.Constraints[key], Constrained{Path: l.Path, Flag: flag})
			}
		}
	}
	return a
}

// Merge builds the inherited view: the chamber's flags, plus each flag it
// lacks from the nearest layer that defines it. A flag an ancestor seals is
// served from the farthest ancestor that seals it, whatever nearer layers
// define. It never mutates the layers. It fails with the chamber's own Err,
// else the farthest ancestor's Err.
func (l *Lineage) Merge() (*realm.Chamber, error) {
	return l.merge(nil)
}

// Resolution says where each flag of a lineage's inherited view comes from.
type Resolution struct {
	// Sources maps each flag in the view to the path of the layer whose
	// definition it serves.
	Sources map[string]string
	// SealedBy maps each flag that an ancestor seals to the path of the
	// farthest ancestor that seals it: the chamber cannot define that flag.
	SealedBy map[string]string
	// Hidden maps each sealed flag that nearer layers (the chamber included)
	// define anyway to their paths, nearest first. A seal hides those
	// definitions: they were written before it, as the server refuses them
	// after.
	Hidden map[string][]string
}

// Resolve returns where each flag of Merge's view comes from. Its maps are
// non-nil whenever err is nil.
func (l *Lineage) Resolve() (*Resolution, error) {
	res := &Resolution{Sources: map[string]string{}, SealedBy: map[string]string{}, Hidden: map[string][]string{}}
	if _, err := l.merge(res); err != nil {
		return nil, err
	}
	return res, nil
}

// merge builds the inherited view, recording in res (when non-nil) where
// each flag comes from.
func (l *Lineage) merge(res *Resolution) (*realm.Chamber, error) {
	if len(l.Layers) == 0 {
		return nil, errors.New("lineage has no layers")
	}
	if err := l.Layers[0].Err; err != nil {
		return nil, err
	}
	// The farthest broken ancestor is the one a top-down walk meets first.
	for i := len(l.Layers) - 1; i > 0; i-- {
		if err := l.Layers[i].Err; err != nil {
			return nil, err
		}
	}

	// sealedAt maps each flag an ancestor seals to the index of the
	// farthest such ancestor
	sealedAt := map[string]int{}
	for i := len(l.Layers) - 1; i > 0; i-- {
		for key := range sealedIn(l.Layers[i].Chamber) {
			if _, ok := sealedAt[key]; !ok {
				sealedAt[key] = i
			}
		}
	}
	if res != nil {
		for key, i := range sealedAt {
			res.SealedBy[key] = l.Layers[i].Path
		}
	}

	// Layers are nearest first, so each flag comes from the first layer that
	// defines it, whole: the chamber's own flags, then each ancestor's. A
	// sealed flag comes from the layer that seals it, and the definitions
	// nearer than that are hidden.
	merged := &realm.Chamber{Rules: map[string]*realm.Flag{}}
	for i, layer := range l.Layers {
		if layer.Chamber == nil {
			continue
		}
		for key, flag := range layer.Chamber.Rules {
			if s, ok := sealedAt[key]; ok && i < s {
				if res != nil {
					res.Hidden[key] = append(res.Hidden[key], layer.Path)
				}
				continue
			}
			if _, ok := merged.Rules[key]; ok {
				continue
			}
			merged.Rules[key] = flag
			if res != nil {
				res.Sources[key] = layer.Path
			}
		}
	}
	return merged, nil
}

// Revision returns the highest Version among the layers: the revision of the
// inherited view, which changes whenever any layer it is built from does. It
// is 0 when the storage does not track revisions.
//
// Deleting an ancestor changes the view but takes its revision away, so the
// view's revision can go down: it names a version of the view rather than
// ordering versions.
func (l *Lineage) Revision() uint64 {
	var rev uint64
	for _, layer := range l.Layers {
		rev = max(rev, layer.Version)
	}
	return rev
}

// sealedIn returns the keys of the flags c seals; c may be nil.
func sealedIn(c *realm.Chamber) iter.Seq[string] {
	return func(yield func(string) bool) {
		if c == nil {
			return
		}
		for key, flag := range c.Rules {
			if flag != nil && flag.Sealed && !yield(key) {
				return
			}
		}
	}
}

// ReadAncestry returns s.Ancestry when s applies inheritance, and an empty
// Ancestry otherwise: without inheritance, no chamber is above another.
func ReadAncestry(ctx context.Context, s Storage, logicalPath string) (*Ancestry, error) {
	if layered, ok := s.(Layered); ok {
		return layered.Ancestry(ctx, logicalPath)
	}
	return ancestryOf(nil), nil
}

// ReadLineage returns the lineage of the chamber at logicalPath: s.Layers
// when s applies inheritance, otherwise the chamber alone (Inheritable is
// false). It returns a NotFoundError when there is no chamber at logicalPath.
func ReadLineage(ctx context.Context, s Storage, logicalPath string) (*Lineage, error) {
	if layered, ok := s.(Layered); ok {
		return layered.Layers(ctx, logicalPath)
	}
	entry, err := s.Get(ctx, logicalPath)
	if err != nil {
		return nil, err
	}
	return &Lineage{Layers: []Layer{parseLayer(canonicalChamberPath(logicalPath), entry)}}, nil
}

// ChamberError reports a stored chamber that cannot be parsed. Its message
// describes the chamber, never the storage it came from, so it is safe to
// show to callers who may read the chamber.
type ChamberError struct {
	// Path is the chamber's path.
	Path string
	Err  error
}

func (e *ChamberError) Error() string { return e.Err.Error() }

func (e *ChamberError) Unwrap() error { return e.Err }

// parseLayer parses a stored chamber into a layer at p; a chamber that cannot
// be parsed sets the layer's Err to a ChamberError.
func parseLayer(p string, entry *StorageEntry) Layer {
	c := &realm.Chamber{}
	if err := json.Unmarshal(entry.Value, c); err != nil {
		return Layer{Path: p, Err: &ChamberError{Path: p, Err: err}, Version: entry.Version}
	}
	return Layer{Path: p, Chamber: c, Version: entry.Version}
}

// canonicalChamberPath returns logicalPath cleaned, with a trailing slash.
func canonicalChamberPath(logicalPath string) string {
	return utils.EnsureTrailingSlash(path.Clean(logicalPath))
}
