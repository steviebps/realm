package storage

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
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
	// InheritedByChildren reports whether the chambers below the path
	// inherit its flags.
	InheritedByChildren bool
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
}

// Merge builds the inherited view: the chamber's flags, plus each flag it
// lacks from the nearest layer that defines it. It never mutates the layers.
// It fails with the chamber's own Err, else the farthest ancestor's Err.
func (l *Lineage) Merge() (*realm.Chamber, error) {
	return l.merge(nil)
}

// Sources returns, for every flag in Merge's view, the path of the layer
// whose definition it serves. The map is non-nil whenever err is nil.
func (l *Lineage) Sources() (map[string]string, error) {
	sources := map[string]string{}
	if _, err := l.merge(sources); err != nil {
		return nil, err
	}
	return sources, nil
}

// merge builds the inherited view, recording in sources (when non-nil) the
// path of the layer each flag is served from.
func (l *Lineage) merge(sources map[string]string) (*realm.Chamber, error) {
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

	self := l.Layers[0]
	merged := &realm.Chamber{Rules: map[string]*realm.Flag{}}
	if self.Chamber != nil && self.Chamber.Rules != nil {
		merged.Rules = maps.Clone(self.Chamber.Rules)
	}
	record := func(from *realm.Chamber, at string) {
		if sources == nil {
			return
		}
		// merged and sources hold the same keys, so a key not yet in
		// sources was just inherited from this layer.
		for key := range from.Rules {
			if _, ok := sources[key]; !ok {
				sources[key] = at
			}
		}
	}
	record(merged, self.Path)
	for _, layer := range l.Layers[1:] {
		if layer.Chamber == nil {
			continue
		}
		merged.InheritFrom(layer.Chamber)
		record(layer.Chamber, layer.Path)
	}
	return merged, nil
}

// ReadLineage returns the lineage of the chamber at logicalPath: s.Layers
// when s applies inheritance, otherwise the chamber alone (Inheritable is
// false). It returns a NotFoundError when there is no chamber at logicalPath.
func ReadLineage(ctx context.Context, s Storage, logicalPath string) (*Lineage, error) {
	if layered, ok := s.(Layered); ok {
		return layered.Layers(ctx, logicalPath)
	}
	if err := ValidatePath(logicalPath); err != nil {
		return nil, err
	}
	entry, err := s.Get(ctx, logicalPath)
	if err != nil {
		return nil, err
	}
	return &Lineage{Layers: []Layer{parseLayer(canonicalChamberPath(logicalPath), entry.Value)}}, nil
}

// parseLayer parses a stored chamber into a layer at p; a chamber that cannot
// be parsed sets the layer's Err.
func parseLayer(p string, value []byte) Layer {
	c := &realm.Chamber{}
	if err := json.Unmarshal(value, c); err != nil {
		return Layer{Path: p, Err: err}
	}
	return Layer{Path: p, Chamber: c}
}

// canonicalChamberPath returns logicalPath cleaned, with a trailing slash.
func canonicalChamberPath(logicalPath string) string {
	return utils.EnsureTrailingSlash(path.Clean(logicalPath))
}
