package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"path"
	"slices"

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
}

// BuildLineage reads the lineage of the canonical path p (see
// cleanChamberPath) through read: the chamber at p, then, when inheritable,
// each ancestor its inherited view consults. Ancestors are read farthest
// first, so a failed read reports the farthest one that fails; the lineage
// holds them nearest first. Every lineage, and so every inherited view,
// Ancestry, and Resolution, is built here.
//
// The chamber at p need not exist (see Lineage.Stored): a lineage without it
// is what a chamber written there would inherit. An ancestor that cannot be
// parsed is recorded with its error, wrapped to name it; an error from read
// is returned as is.
func BuildLineage(ctx context.Context, read LayerReader, p string, inheritable bool) (*Lineage, error) {
	own, err := read(ctx, p)
	if err != nil {
		return nil, err
	}
	lin := &Lineage{Inheritable: inheritable, Layers: []Layer{own}}
	if !inheritable {
		return lin, nil
	}
	for _, a := range consultedAncestors(p) {
		layer, err := read(ctx, a)
		if err != nil {
			return nil, err
		}
		if layer.Err != nil {
			// skipping it would silently drop its flags (kill switches
			// included) from every chamber below it
			layer.Err = fmt.Errorf("could not parse parent chamber %s: %w", a, layer.Err)
		}
		lin.Layers = append(lin.Layers, layer)
	}
	slices.Reverse(lin.Layers[1:])
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return lin, nil
}

// storedLineage builds the lineage of the chamber stored at key, a path as
// given, reading the chamber itself with get at key, as a plain Get would,
// and its ancestors with read. It fails with get's error, a NotFoundError
// when there is no chamber at key, before reading any ancestor.
func storedLineage(ctx context.Context, key string, get func(context.Context, string) (*StorageEntry, error), parse func(string, *StorageEntry) Layer, read LayerReader, inheritable bool) (*Lineage, error) {
	p, err := cleanChamberPath(key)
	if err != nil {
		return nil, err
	}
	withOwn := func(ctx context.Context, q string) (Layer, error) {
		if q != p {
			return read(ctx, q)
		}
		entry, err := get(ctx, key)
		if err != nil {
			return Layer{}, err
		}
		return parse(p, entry), nil
	}
	return BuildLineage(ctx, withOwn, p, inheritable)
}

// Stored reports whether a chamber is stored at the lineage's path, whether
// or not it can be parsed.
func (l *Lineage) Stored() bool {
	return len(l.Layers) > 0 && (l.Layers[0].Chamber != nil || l.Layers[0].Err != nil)
}

// Ancestry is what the chambers above a path say about the flags a chamber
// there may define: which flags they seal, and which they constrain.
// Ancestors that are missing say nothing.
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

// Ancestry returns what the lineage's ancestors say about the flags a chamber
// at its path may define. It fails, as Merge does, with the farthest
// ancestor's Err: a chamber below a broken one serves nothing until it is
// fixed, so what it may define cannot be known either. The chamber's own Err
// does not matter, as a write may replace it.
func (l *Lineage) Ancestry() (*Ancestry, error) {
	if err := l.ancestorErr(); err != nil {
		return nil, err
	}
	a := &Ancestry{Seals: map[string]string{}, Constraints: map[string][]Constrained{}}
	for key, i := range l.sealedAt() {
		a.Seals[key] = l.Layers[i].Path
	}
	for i := len(l.Layers) - 1; i > 0; i-- {
		layer := l.Layers[i]
		if layer.Chamber == nil {
			continue
		}
		for key, flag := range layer.Chamber.Rules {
			if flag != nil && len(flag.Constraints) > 0 {
				a.Constraints[key] = append(a.Constraints[key], Constrained{Path: layer.Path, Flag: flag})
			}
		}
	}
	return a, nil
}

// ancestorErr returns the farthest ancestor's Err: the one a top-down walk
// meets first.
func (l *Lineage) ancestorErr() error {
	for i := len(l.Layers) - 1; i > 0; i-- {
		if err := l.Layers[i].Err; err != nil {
			return err
		}
	}
	return nil
}

// sealedAt maps each flag an ancestor seals to the index of the farthest
// ancestor that seals it.
func (l *Lineage) sealedAt() map[string]int {
	at := map[string]int{}
	for i := len(l.Layers) - 1; i > 0; i-- {
		for key := range sealedIn(l.Layers[i].Chamber) {
			if _, ok := at[key]; !ok {
				at[key] = i
			}
		}
	}
	return at
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
	if err := l.ancestorErr(); err != nil {
		return nil, err
	}

	sealedAt := l.sealedAt()
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

// ReadLineage returns the lineage of the chamber at logicalPath: s.Layers
// when s applies inheritance, otherwise the chamber alone (Inheritable is
// false). It returns a NotFoundError when there is no chamber at logicalPath.
func ReadLineage(ctx context.Context, s Storage, logicalPath string) (*Lineage, error) {
	if layered, ok := s.(Layered); ok {
		return layered.Layers(ctx, logicalPath)
	}
	return storedLineage(ctx, logicalPath, s.Get, ParseLayer, nil, false)
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

// ParseLayer parses a chamber stored at the canonical path p into a layer,
// leniently, as reads do: one that cannot be parsed has no Chamber, and its
// Err is a ChamberError.
func ParseLayer(p string, entry *StorageEntry) Layer {
	c := &realm.Chamber{}
	if err := json.Unmarshal(entry.Value, c); err != nil {
		return Layer{Path: p, Err: &ChamberError{Path: p, Err: err}, Version: entry.Version}
	}
	return Layer{Path: p, Chamber: c, Version: entry.Version}
}

// cleanChamberPath returns the canonical form of logicalPath, cleaned and
// with a trailing slash, once it is valid (see ValidatePath). It validates
// first: cleaning would resolve "..".
func cleanChamberPath(logicalPath string) (string, error) {
	if err := ValidatePath(logicalPath); err != nil {
		return "", err
	}
	return canonicalChamberPath(logicalPath), nil
}

// canonicalChamberPath returns logicalPath cleaned, with a trailing slash.
func canonicalChamberPath(logicalPath string) string {
	return utils.EnsureTrailingSlash(path.Clean(logicalPath))
}
