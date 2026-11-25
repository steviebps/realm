package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// checkBelow refuses next, the chamber a write would store at root, when a
// chamber below root serves a definition of one of keys (from
// constraintChanges) with another type, or with values next's constraints
// for it refuse. Each chamber below is judged by the view it would serve
// with next stored: definitions a seal hides are not served, so they are not
// checked, and neither are chambers whose view cannot be built (they, or one
// they inherit from, cannot be parsed), which fail every read until fixed.
// Seals above root are not consulted: the write has already been refused if
// it changes a flag one seals. strg holds chambers as stored (see
// storage.Raw).
//
// The chambers below are read before the write rather than with it, so a
// definition written below concurrently may escape the check, as it may
// escape a seal; it is checked against these constraints when it next
// changes.
func checkBelow(ctx context.Context, strg storage.Storage, root string, next *realm.Chamber, keys []string) error {
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

	schemas := make(map[string]*realm.Schema, len(keys))
	for _, key := range keys {
		s, err := next.Rules[key].ParseConstraints()
		if err != nil {
			// ParseChamber has checked them
			return err
		}
		schemas[key] = s
	}

	// the chambers as they would be with next stored at root
	read := func(_ context.Context, p string) (storage.Layer, error) {
		if p == root {
			return storage.Layer{Path: root, Chamber: next}, nil
		}
		if l, ok := layers[p]; ok {
			return l, nil
		}
		return storage.Layer{Path: p}, nil
	}
	offenders := map[string][]string{}
	totals := map[string]int{}
	for _, p := range paths[1:] {
		own := layers[p].Chamber
		if own == nil {
			continue
		}
		lin, err := storage.BuildLineage(ctx, read, p, true)
		if err != nil {
			return err
		}
		res, err := lin.Resolve()
		if err != nil {
			continue // its view cannot be built, so it serves nothing
		}
		for _, key := range keys {
			def := own.Rules[key]
			if def == nil || slices.Contains(res.Hidden[key], p) {
				continue
			}
			f := next.Rules[key]
			var why string
			if def.Type != f.Type {
				why = fmt.Sprintf("a %s flag", def.Type)
			} else if err := def.CheckValues(schemas[key]); err != nil {
				why = err.Error()
			} else {
				continue
			}
			totals[key]++
			if len(offenders[key]) < maxOffenders {
				offenders[key] = append(offenders[key], fmt.Sprintf("%s (%s)", p, why))
			}
		}
	}

	var reasons []string
	for _, key := range keys {
		if totals[key] == 0 {
			continue
		}
		list := strings.Join(offenders[key], ", ")
		if more := totals[key] - len(offenders[key]); more > 0 {
			list += fmt.Sprintf(", and %d more", more)
		}
		reasons = append(reasons, fmt.Sprintf("flag %q: chambers below define it outside its new constraints: %s", key, list))
	}
	if len(reasons) > 0 {
		return &constraintError{reasons: reasons}
	}
	return nil
}
