package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	realm "github.com/steviebps/realm/pkg"
	"github.com/steviebps/realm/pkg/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// chamberWriter is the one place chambers are added, changed, and deleted.
// It enforces everything a write must respect beyond its own body, in order:
// the seals and constraints of the chambers above (refusing a write below a
// chamber that cannot be parsed, as reads fail below one), the constraints
// it adds or changes against the chambers below, and the caller's
// preconditions; it records each change in the chamber's history in the
// same storage.Update, and announces it to the broker. Its interface takes
// no request: handlers turn requests into a writeRequest, and its errors
// into statuses with writeStatus.
type chamberWriter struct {
	// raw holds chambers as stored (see storage.Raw), along with their
	// history.
	raw storage.Storage
	// inheritable reports whether chambers inherit from the chambers above
	// them, so that seals and constraints bind those below.
	inheritable bool
	// notify announces a change of a chamber (see broker.Notify).
	notify func(path string)
}

// newChamberWriter returns the chamberWriter for the server's storage strg,
// announcing changes with notify.
func newChamberWriter(strg storage.Storage, notify func(path string)) *chamberWriter {
	return &chamberWriter{raw: storage.Raw(strg), inheritable: storage.IsInheritable(strg), notify: notify}
}

// writeRequest is one write of a chamber.
type writeRequest struct {
	// Path is the chamber's canonical path.
	Path string
	// Operation is PutOperation, PatchOperation, or DeleteOperation.
	Operation Operation
	// Body is the chamber a PUT stores, or a PATCH merges into the stored
	// one, as realm.ParseChamber returned it.
	Body *realm.Chamber
	// Recursive deletes every chamber below Path too.
	Recursive bool
	// Preconditions are checked against the chamber at Path as stored.
	Preconditions preconditions
	// Actor is recorded in the history as who made the change.
	Actor string
}

// preconditions are a write's conditions on the ETag of the chamber as
// stored (see etagOf): the values of If-Match and If-None-Match, or empty.
type preconditions struct {
	IfMatch     string
	IfNoneMatch string
}

// check refuses with a preconditionError a write the preconditions reject,
// given the chamber as stored (nil when there is none).
func (p preconditions) check(stored *storage.StorageEntry) error {
	current := ""
	if stored != nil {
		current = etagOf(stored.Value)
	}
	if (p.IfMatch != "" && !etagMatches(p.IfMatch, current)) || (p.IfNoneMatch != "" && etagMatches(p.IfNoneMatch, current)) {
		return &preconditionError{current: current}
	}
	return nil
}

// writeResult is what a write did.
type writeResult struct {
	// ETag is the chamber's ETag after a PUT or PATCH.
	ETag string
	// Deleted lists the chambers a recursive delete removed, sorted.
	Deleted []string
}

// Write applies req. It refuses a write before anything is written with an
// error writeStatus maps to a 4xx status; any other error may come after
// part of the write landed. Every change that may have landed is announced.
func (cw *chamberWriter) Write(ctx context.Context, req writeRequest) (writeResult, error) {
	recursive := req.Operation == DeleteOperation && req.Recursive
	var res writeResult
	var err error
	if recursive {
		res, err = cw.deleteTree(ctx, req)
	} else {
		res, err = cw.write(ctx, req)
	}
	if errors.Is(err, storage.ErrConflict) {
		err = &busyError{recursive: recursive}
	}

	// Backends without transactions apply a write's changes one at a time,
	// so part of a failed one may have landed: let streams look.
	landed := writeStatus(err) >= http.StatusInternalServerError
	if (err == nil && !recursive) || landed {
		cw.notify(req.Path)
	}
	// each deleted chamber is announced, as a change feed announces each
	// key: subtree streams learn which went
	for _, p := range res.Deleted {
		cw.notify(p)
	}
	return res, err
}

// writeStatus returns the HTTP status of a write that Write refused or failed
// with err: a 4xx when it refused the write before anything was written, 500
// otherwise. It is 200 when err is nil.
func writeStatus(err error) int {
	var pf *preconditionError
	var se *sealedError
	var ce *constraintError
	var nf *storage.NotFoundError
	var broken *storage.ChamberError
	switch {
	case err == nil:
		return http.StatusOK
	case errors.As(err, &pf):
		return http.StatusPreconditionFailed
	case errors.As(err, &se), errors.As(err, &ce), errors.Is(err, errPatchMissing), errors.Is(err, errDeleteEverything), errors.Is(err, storage.ErrInvalidPath):
		return http.StatusBadRequest
	case errors.As(err, &broken), errors.Is(err, storage.ErrConflict):
		// a chamber above is broken (named, as reads do), or the chamber is
		// changing under the write
		return http.StatusConflict
	case errors.As(err, &nf):
		return http.StatusNotFound
	case errors.Is(err, storage.ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}

// busyError refuses a write that kept conflicting with concurrent changes
// (storage.ErrConflict), so the caller may retry it.
type busyError struct {
	recursive bool
}

func (e *busyError) Error() string {
	if e.recursive {
		return "the chambers are being changed by others; try again"
	}
	return "the chamber is being changed by others; try again"
}

func (e *busyError) Unwrap() error { return storage.ErrConflict }

// write applies a PUT, PATCH, or DELETE of req.Path and records it in the
// chamber's history, atomically. Preconditions are checked against the
// chamber as stored, and a PATCH merges into it rather than its inherited
// view, so inherited rules are never copied into the child. A PUT or PATCH
// is checked against the chambers above and below it first (see prepare),
// and again within the write (see writeGuard.check).
func (cw *chamberWriter) write(ctx context.Context, req writeRequest) (writeResult, error) {
	var guard *writeGuard
	switch req.Operation {
	case PutOperation, PatchOperation:
		if req.Body == nil {
			return writeResult{}, errors.New("a PUT or PATCH needs a chamber")
		}
		var err error
		if guard, err = cw.prepare(ctx, req); err != nil {
			return writeResult{}, err
		}
	case DeleteOperation:
	default:
		return writeResult{}, fmt.Errorf("cannot write a chamber with %s", req.Operation)
	}

	var etag string
	err := storage.Update(ctx, cw.raw, append([]string{req.Path}, historyKeys(req.Path)...), func(cur map[string]*storage.StorageEntry) ([]storage.Op, error) {
		stored := cur[req.Path]
		if err := req.Preconditions.check(stored); err != nil {
			return nil, err
		}

		var next *realm.Chamber
		switch req.Operation {
		case PutOperation:
			next = req.Body
			if guard.active() {
				if err := guard.check(storedChamber(req.Path, stored), next); err != nil {
					return nil, err
				}
			}
		case PatchOperation:
			if stored == nil {
				return nil, fmt.Errorf("%w: %w", errPatchMissing, &storage.NotFoundError{Key: req.Path})
			}
			layer := storage.ParseLayer(req.Path, stored)
			if layer.Err != nil {
				return nil, fmt.Errorf("could not unmarshal current chamber while patching: %w", layer.Err)
			}
			next = nextChamber(PatchOperation, layer.Chamber, req.Body)
			if err := guard.check(layer.Chamber, next); err != nil {
				return nil, err
			}
		case DeleteOperation:
			if stored == nil {
				return nil, &storage.NotFoundError{Key: req.Path}
			}
		}

		rev := Revision{Time: time.Now().UTC(), Actor: req.Actor, Operation: req.Operation}
		var value []byte
		etag = ""
		if next != nil {
			var err error
			if value, err = json.Marshal(next); err != nil {
				return nil, err
			}
			etag = etagOf(value)
			rev.ETag = etag
			rev.Chamber = value
		}
		history, err := recordRevision(cur, req.Path, rev)
		if err != nil {
			return nil, err
		}
		return append([]storage.Op{{Key: req.Path, Value: value}}, history...), nil
	})
	return writeResult{ETag: etag}, err
}

// errDeleteEverything refuses a recursive delete of the root chamber.
var errDeleteEverything = errors.New("refusing to recursively delete every chamber; delete the chambers below / instead")

// deleteTree deletes the chamber at req.Path and every chamber below it in
// one Update, recording each deletion in that chamber's history, and returns
// the deleted paths. Preconditions are checked against the chamber at
// req.Path. It returns a NotFoundError when there is no chamber at or below
// req.Path. When the Update fails after deciding what to delete, it returns
// the paths it set out to delete with the error: part of the deletion may
// have landed.
//
// The tree is listed before the Update, so a chamber created below
// concurrently may survive; deleting again removes it.
func (cw *chamberWriter) deleteTree(ctx context.Context, req writeRequest) (writeResult, error) {
	root := req.Path
	if root == "/" {
		return writeResult{}, errDeleteEverything
	}
	paths, err := storage.ListTree(ctx, cw.raw, root, 0)
	if err != nil {
		return writeResult{}, err
	}
	keys := append(slices.Clone(paths), historyKeys(paths...)...)

	var deleted []string
	err = storage.Update(ctx, cw.raw, keys, func(cur map[string]*storage.StorageEntry) ([]storage.Op, error) {
		if err := req.Preconditions.check(cur[root]); err != nil {
			return nil, err
		}
		deleted = deleted[:0]
		var ops []storage.Op
		now := time.Now().UTC()
		for _, p := range paths {
			if cur[p] == nil {
				continue // a path that only leads to chambers below it
			}
			history, err := recordRevision(cur, p, Revision{Time: now, Actor: req.Actor, Operation: DeleteOperation})
			if err != nil {
				return nil, err
			}
			ops = append(ops, storage.Op{Key: p})
			ops = append(ops, history...)
			deleted = append(deleted, p)
		}
		if len(deleted) == 0 {
			return nil, &storage.NotFoundError{Key: root}
		}
		return ops, nil
	})
	if errors.Is(err, storage.ErrTooLarge) {
		err = fmt.Errorf("%w; delete fewer chambers at once", err)
	}
	slices.Sort(deleted)
	return writeResult{Deleted: deleted}, err
}

// writeGuard is what a PUT or PATCH must respect beyond its own body: what
// the chambers above say about the flags it defines, and which flags'
// constraints it checked against the chambers below.
type writeGuard struct {
	// ancestry is read before the write, not with it: a seal that lands
	// meanwhile still hides what this write stores (Lineage.Merge enforces
	// seals; the check explains them), and constraints that land meanwhile
	// check the chambers below them, this one included.
	ancestry *storage.Ancestry
	// scanned reports whether checkBelow ran, and checkedBelow holds the
	// flags whose new constraints it checked against the definitions below.
	scanned      bool
	checkedBelow []string
}

// prepare reads the lineage of req.Path and checks req against it: it
// refuses a write below a chamber that cannot be parsed and, when the write
// adds or changes constraints, checks them against the chambers below (see
// checkBelow), after the chambers above have had their say.
func (cw *chamberWriter) prepare(ctx context.Context, req writeRequest) (*writeGuard, error) {
	ctx, span := otel.Tracer("github.com/steviebps/realm").Start(ctx, "chamberWriter prepare", trace.WithAttributes(attribute.String("realm.server.path", req.Path)))
	defer span.End()

	if !cw.inheritable {
		// without inheritance, no chamber is above another
		return &writeGuard{ancestry: &storage.Ancestry{}}, nil
	}
	lin, err := storage.BuildLineage(ctx, storage.RawLayers(cw.raw), req.Path, true)
	if err != nil {
		return nil, err
	}
	ancestry, err := lin.Ancestry()
	if err != nil {
		return nil, fmt.Errorf("chambers below a parent chamber that cannot be parsed cannot be written until it is fixed: %w", err)
	}
	g := &writeGuard{ancestry: ancestry}
	if !hasConstraints(req.Body) {
		// only a body with constraints can add or change them
		return g, nil
	}
	// Which constraints change is known for certain only within the write;
	// check refuses a write whose changes differ from those checked here.
	before := orEmpty(lin.Layers[0].Chamber)
	next := nextChamber(req.Operation, before, req.Body)
	if err := g.check(before, next); err != nil {
		return nil, err
	}
	g.scanned, g.checkedBelow = true, constraintChanges(before, next)
	if err := checkBelow(ctx, cw.raw, req.Path, next, g.checkedBelow); err != nil {
		return nil, err
	}
	return g, nil
}

// active reports whether check can refuse anything: whether the chambers
// above seal or constrain any flag, or the chambers below were checked.
func (g *writeGuard) active() bool {
	return len(g.ancestry.Seals) > 0 || len(g.ancestry.Constraints) > 0 || g.scanned
}

// check refuses next, the chamber a write would store over before (the
// chamber as stored), for the flags it adds or changes: with a sealedError
// when parents seal any, else with a constraintError when any breaks a
// parent's constraints. A definition stored before the seal or the
// constraints may stay as it is, so the chamber can still be edited. Once
// the chambers below are checked, it also refuses with storage.ErrConflict a
// write whose constraint changes are not those checked (the chamber changed
// since).
func (g *writeGuard) check(before, next *realm.Chamber) error {
	var sealed []sealedFlag
	var reasons []string
	for _, key := range slices.Sorted(maps.Keys(next.Rules)) {
		by, isSealed := g.ancestry.Seals[key]
		defs := g.ancestry.Constraints[key]
		if !isSealed && len(defs) == 0 {
			continue
		}
		if old, had := before.Rules[key]; had && sameFlag(old, next.Rules[key]) {
			continue
		}
		if isSealed {
			sealed = append(sealed, sealedFlag{key: key, by: by})
		} else if reason := breaksParents(key, next.Rules[key], defs); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	switch {
	case len(sealed) > 0:
		return &sealedError{flags: sealed}
	case len(reasons) > 0:
		return &constraintError{reasons: reasons}
	}
	if g.scanned {
		for _, key := range constraintChanges(before, next) {
			if !slices.Contains(g.checkedBelow, key) {
				return storage.ErrConflict
			}
		}
	}
	return nil
}

// storedChamber decodes the chamber stored at p as reads do (see
// storage.ParseLayer): a missing chamber, or one that cannot be parsed,
// keeps no flags, so a write may replace it.
func storedChamber(p string, stored *storage.StorageEntry) *realm.Chamber {
	if stored == nil {
		return orEmpty(nil)
	}
	return orEmpty(storage.ParseLayer(p, stored).Chamber)
}

// orEmpty returns c, or a chamber without flags when c is nil.
func orEmpty(c *realm.Chamber) *realm.Chamber {
	if c == nil {
		return &realm.Chamber{Rules: map[string]*realm.Flag{}}
	}
	return c
}

// hasConstraints reports whether a flag of c has constraints.
func hasConstraints(c *realm.Chamber) bool {
	for _, f := range c.Rules {
		if f != nil && len(f.Constraints) > 0 {
			return true
		}
	}
	return false
}

// nextChamber returns the chamber a PUT or PATCH of body would store over
// before, the chamber as stored, which it leaves as it is.
func nextChamber(op Operation, before, body *realm.Chamber) *realm.Chamber {
	if op != PatchOperation {
		return body
	}
	merged := &realm.Chamber{Rules: maps.Clone(before.Rules)}
	merged.OverwriteFrom(body)
	return merged
}

// sameFlag reports whether a and b are written the same.
func sameFlag(a, b *realm.Flag) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// errPatchMissing is returned when a PATCH targets a chamber that does not exist.
var errPatchMissing = errors.New("cannot patch a resource that does not exist")

// preconditionError is returned when If-Match / If-None-Match reject a write.
// current is the chamber's ETag, or empty when it does not exist.
type preconditionError struct {
	current string
}

func (e *preconditionError) Error() string { return "precondition failed" }

// sealedFlag is a flag a write would define although a parent seals it.
type sealedFlag struct {
	key string
	by  string
}

// sealedError refuses a write that adds or changes flags that parent
// chambers seal.
type sealedError struct {
	flags []sealedFlag
}

func (e *sealedError) Error() string {
	if len(e.flags) == 1 {
		f := e.flags[0]
		return fmt.Sprintf("flag %q is sealed by %s: chambers below it cannot define it", f.key, f.by)
	}
	parts := make([]string, len(e.flags))
	for i, f := range e.flags {
		parts[i] = fmt.Sprintf("%q (by %s)", f.key, f.by)
	}
	return "flags are sealed by parent chambers, which chambers below them cannot define: " + strings.Join(parts, ", ")
}
