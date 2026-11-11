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
	// cannot be parsed.
	Chamber *realm.Chamber
	// Err says why the chamber cannot be parsed. The inherited view cannot be
	// built while any layer has one.
	Err error
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
	// AncestorSeals maps each flag that an ancestor of logicalPath seals to
	// the path of the farthest ancestor that seals it: the flags a chamber
	// at logicalPath cannot define, whether or not one exists there.
	AncestorSeals(ctx context.Context, logicalPath string) (map[string]string, error)
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

// ReadAncestorSeals returns s.AncestorSeals when s applies inheritance, and
// nil otherwise: without inheritance, nothing is sealed.
func ReadAncestorSeals(ctx context.Context, s Storage, logicalPath string) (map[string]string, error) {
	if layered, ok := s.(Layered); ok {
		return layered.AncestorSeals(ctx, logicalPath)
	}
	return nil, nil
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
	return &Lineage{Layers: []Layer{parseLayer(canonicalChamberPath(logicalPath), entry.Value)}}, nil
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
func parseLayer(p string, value []byte) Layer {
	c := &realm.Chamber{}
	if err := json.Unmarshal(value, c); err != nil {
		return Layer{Path: p, Err: &ChamberError{Path: p, Err: err}}
	}
	return Layer{Path: p, Chamber: c}
}

// canonicalChamberPath returns logicalPath cleaned, with a trailing slash.
func canonicalChamberPath(logicalPath string) string {
	return utils.EnsureTrailingSlash(path.Clean(logicalPath))
}
