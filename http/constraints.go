package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// A flag's constraints bind its definitions in the chambers below too, so
// that whoever may write only below a chamber cannot widen its bounds: a
// definition must have the type of the flag in every parent chamber that
// constrains it, and values every one of their constraints allows. A write
// is checked against its parents' constraints for the flags it adds or
// changes (a definition stored before the constraints may stay as it is, as
// with seals), and a write that adds or changes a flag's constraints is
// checked against every definition below it, so that none is left serving a
// value the flag no longer allows.

// maxOffenders bounds the definitions below a chamber that a constraintError
// names for each flag.
const maxOffenders = 10

// constraintError refuses a write whose values a parent's constraints
// refuse, or whose new constraints refuse what chambers below define.
type constraintError struct {
	reasons []string
}

func (e *constraintError) Error() string {
	return strings.Join(e.reasons, "; ")
}

// breaksParents says why f, the definition of key a write adds or changes,
// breaks the constraints of a parent's definition of it (defs, farthest
// first, from storage.Ancestry): it has another type, or values they refuse.
// It returns "" when f satisfies them all.
func breaksParents(key string, f *realm.Flag, defs []storage.Constrained) string {
	if f == nil || f.Rule == nil {
		return ""
	}
	for _, def := range defs {
		if f.Type != def.Flag.Type {
			return fmt.Sprintf("flag %q is a %s flag in %s, whose constraints bind the chambers below it, so it cannot be a %s flag here", key, def.Flag.Type, def.Path, f.Type)
		}
		s, err := def.Flag.ParseConstraints()
		if err != nil {
			return fmt.Sprintf("flag %q cannot be checked against its constraints in %s: %v", key, def.Path, err)
		}
		if err := f.CheckValues(s); err != nil {
			return fmt.Sprintf("flag %q must satisfy its constraints in %s: %v", key, def.Path, err)
		}
	}
	return ""
}

// constraintChanges returns, sorted, the keys of the flags in next whose
// constraints a write adds or changes, compared with before, the chamber as
// stored: those with constraints that before lacks, or has otherwise, or
// with another type. Their definitions below must be checked against the new
// constraints.
func constraintChanges(before, next *realm.Chamber) []string {
	var keys []string
	for key, f := range next.Rules {
		if f == nil || len(f.Constraints) == 0 {
			continue
		}
		if old := before.Rules[key]; old == nil || old.Type != f.Type || !sameJSON(old.Constraints, f.Constraints) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// sameJSON reports whether a and b are the same JSON value, whatever their
// spacing or the order of object members.
func sameJSON(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// storedChamber decodes the chamber as stored, leniently; a missing chamber,
// or one that cannot be parsed, keeps no flags.
func storedChamber(stored *storage.StorageEntry) *realm.Chamber {
	c := &realm.Chamber{Rules: map[string]*realm.Flag{}}
	if stored != nil {
		_ = json.Unmarshal(stored.Value, c)
	}
	return c
}

// checkBelow refuses next, the chamber a write would store at root, when a
// chamber below root serves a definition of one of keys (from
// constraintChanges) with another type, or with values next's constraints
// for it refuse. Definitions a seal hides are not served, so they are not
// checked; neither are chambers that cannot be parsed, which fail every read
// until they are fixed. Seals above root are not consulted: guardWrite has
// already refused a write that changes a flag one seals. strg holds
// chambers as stored (see storage.Raw).
//
// The chambers below are read before the write rather than with it, so a
// definition written below concurrently may escape the check, as it may
// escape a seal; it is checked against these constraints when it next
// changes.
func checkBelow(ctx context.Context, strg storage.Storage, root string, next *realm.Chamber, keys []string) error {
	// a flag next seals hides every definition below
	keys = slices.DeleteFunc(slices.Clone(keys), func(key string) bool { return next.Rules[key].Sealed })
	if len(keys) == 0 {
		return nil
	}
	ctx, span := otel.Tracer("github.com/steviebps/realm").Start(ctx, "checkBelow", trace.WithAttributes(attribute.String("realm.server.path", root), attribute.StringSlice("realm.server.flags", keys)))
	defer span.End()

	paths, err := storage.ListTree(ctx, strg, root, 0)
	if err != nil {
		return err
	}
	if len(paths) < 2 {
		return nil
	}
	layers, err := storage.ReadLayers(ctx, strg, paths)
	if err != nil {
		return err
	}
	span.SetAttributes(attribute.Int("realm.server.chambers", len(paths)-1))

	var reasons []string
	for _, key := range keys {
		f := next.Rules[key]
		s, err := f.ParseConstraints()
		if err != nil {
			// ParseChamber has checked them
			return err
		}
		var offenders []string
		total := 0
		for _, p := range paths[1:] {
			def := definitionAt(layers, p, key)
			if def == nil || sealedBetween(layers, root, p, key) {
				continue
			}
			var why string
			if def.Type != f.Type {
				why = fmt.Sprintf("a %s flag", def.Type)
			} else if err := def.CheckValues(s); err != nil {
				why = err.Error()
			} else {
				continue
			}
			total++
			if len(offenders) < maxOffenders {
				offenders = append(offenders, fmt.Sprintf("%s (%s)", p, why))
			}
		}
		if total == 0 {
			continue
		}
		list := strings.Join(offenders, ", ")
		if more := total - len(offenders); more > 0 {
			list += fmt.Sprintf(", and %d more", more)
		}
		reasons = append(reasons, fmt.Sprintf("flag %q: chambers below define it outside its new constraints: %s", key, list))
	}
	if len(reasons) > 0 {
		return &constraintError{reasons: reasons}
	}
	return nil
}

// definitionAt returns the definition of key in the chamber at p, or nil.
func definitionAt(layers map[string]storage.Layer, p, key string) *realm.Flag {
	l := layers[p]
	if l.Chamber == nil || l.Err != nil {
		return nil
	}
	return l.Chamber.Rules[key]
}

// sealedBetween reports whether a chamber strictly between root and its
// descendant p seals key, hiding p's definition of it.
func sealedBetween(layers map[string]storage.Layer, root, p, key string) bool {
	for a := parentPath(p); len(a) > len(root); a = parentPath(a) {
		if f := definitionAt(layers, a, key); f != nil && f.Sealed {
			return true
		}
	}
	return false
}

// parentPath returns the canonical path of the chamber above the canonical
// path p.
func parentPath(p string) string {
	dir := path.Dir(strings.TrimSuffix(p, "/"))
	if dir == "/" {
		return dir
	}
	return dir + "/"
}
