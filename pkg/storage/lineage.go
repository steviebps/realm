package storage

import (
	"context"
	"encoding/json"
	"errors"
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

	// Layers are nearest first, so each flag comes from the first layer that
	// defines it, whole: the chamber's own flags, then each ancestor's.
	merged := &realm.Chamber{Rules: map[string]*realm.Flag{}}
	for _, layer := range l.Layers {
		if layer.Chamber == nil {
			continue
		}
		for key, flag := range layer.Chamber.Rules {
			if _, ok := merged.Rules[key]; ok {
				continue
			}
			merged.Rules[key] = flag
			if sources != nil {
				sources[key] = layer.Path
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
