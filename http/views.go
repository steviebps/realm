package http

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/steviebps/realm/auth"
	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/pkg/storage"
)

// builtView is a view as subtree streams send it.
type builtView struct {
	storage.View
	// data is the chamber event's data: the view's rules, or why it cannot
	// be built.
	data []byte
	// id is the chamber event's id: the view's revision, or 0 (no id) when
	// it cannot be built.
	id uint64
	// sum is data's hash, which streams compare with what they last sent.
	sum [sha256.Size]byte
	// hidden and hiddenSum are data and sum for principals that may not be
	// told why the view cannot be built (see visibleError); nil when they
	// may all be.
	hidden    []byte
	hiddenSum [sha256.Size]byte
}

// event returns the chamber event p is sent for the view, and its hash.
func (b builtView) event(p *auth.Principal) ([]byte, [sha256.Size]byte) {
	if b.hidden != nil && !mayKnowWhy(p, b.Err) {
		return b.hidden, b.hiddenSum
	}
	return b.data, b.sum
}

// sharedViewsTTL is how long the views and chambers read for the latest
// change are kept for streams that have yet to ask for them.
var sharedViewsTTL = 30 * time.Second

// sharedViews builds views for subtree streams. A write high in the tree
// wakes every subtree stream below it at once, and can change thousands of
// views; rather than each stream reading the chambers and merging the views
// itself, the streams on a replica share the reads, so each stored chamber is
// read at most once per change, and the views built from them.
//
// Chamber and evaluation streams share them too (see chamberReader).
//
// A stream shares only what was read after the latest change the broker
// announced before it asked, so a stream woken by a change never receives a
// view read before that change. Ordinary requests read storage directly: a
// change made through another replica of a Watchable backend is announced
// only after it is read back from the change feed, so a shared read could
// miss a client's own write.
type sharedViews struct {
	// raw holds chambers as written (see storage.Raw).
	raw         storage.Storage
	inheritable bool
	brk         *broker
	// timeout bounds each shared read.
	timeout time.Duration

	mu sync.Mutex
	// cur holds what was read since the latest change; nil when nothing
	// was read since, or it expired.
	cur *viewGeneration
	// expiry forgets cur sharedViewsTTL after the latest change.
	expiry *time.Timer
}

// newSharedViews returns the shared views of strg's chambers.
func newSharedViews(strg storage.Storage, brk *broker, timeout time.Duration) *sharedViews {
	return &sharedViews{raw: storage.Raw(strg), inheritable: storage.IsInheritable(strg), brk: brk, timeout: timeout}
}

// viewGeneration holds what was read since one change.
type viewGeneration struct {
	gen      uint64
	layers   map[string]*sharedCall[storage.Layer]
	views    map[string]*sharedCall[builtView]
	chambers map[string]*sharedCall[encodedChamber]
	// scans holds, by root, the scans that filled layers (see prefill).
	scans map[string]*sharedCall[struct{}]
}

// sharedCall is one read that callers share.
type sharedCall[T any] struct {
	// done is closed once val and err are set.
	done chan struct{}
	val  T
	err  error
}

// generation returns what was read since the latest change the broker
// announced, starting afresh after a newer one. The caller holds s.mu.
func (s *sharedViews) generation() *viewGeneration {
	gen := s.brk.Generation()
	if s.cur != nil && s.cur.gen >= gen {
		return s.cur
	}
	s.cur = &viewGeneration{gen: gen, layers: map[string]*sharedCall[storage.Layer]{}, views: map[string]*sharedCall[builtView]{}, chambers: map[string]*sharedCall[encodedChamber]{}, scans: map[string]*sharedCall[struct{}]{}}
	if s.expiry == nil {
		s.expiry = time.AfterFunc(sharedViewsTTL, func() {
			s.mu.Lock()
			s.cur = nil
			s.mu.Unlock()
		})
	} else {
		s.expiry.Reset(sharedViewsTTL)
	}
	return s.cur
}

// shared returns the value at key in the map of the latest generation that
// pick selects, loading it with load (given that generation) unless another
// caller already is. Failures are not shared with later callers, which load
// again.
func shared[T any](ctx context.Context, s *sharedViews, pick func(*viewGeneration) map[string]*sharedCall[T], key string, load func(context.Context, *viewGeneration) (T, error)) (T, error) {
	s.mu.Lock()
	g := s.generation()
	m := pick(g)
	c, ok := m[key]
	if !ok {
		c = &sharedCall[T]{done: make(chan struct{})}
		m[key] = c
		go func() {
			defer close(c.done)
			// Detached from the caller: the stream that started the read may
			// end before the others sharing it.
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
			defer cancel()
			c.val, c.err = load(rctx, g)
			if c.err != nil {
				s.mu.Lock()
				if m[key] == c {
					delete(m, key)
				}
				s.mu.Unlock()
			}
		}()
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case <-c.done:
		return c.val, c.err
	}
}

// layer returns the chamber stored at p (see storage.ReadLayer).
func (s *sharedViews) layer(ctx context.Context, p string) (storage.Layer, error) {
	return shared(ctx, s, func(g *viewGeneration) map[string]*sharedCall[storage.Layer] { return g.layers }, p, func(ctx context.Context, _ *viewGeneration) (storage.Layer, error) {
		l, err := storage.ReadLayer(ctx, s.raw, p)
		if err != nil {
			logging.Ctx(ctx).WarnCtx(ctx).Str("path", p).Str("error", err.Error()).Msg("subtree streams could not read the chamber; they will retry")
		}
		return l, err
	})
}

// prefill reads the chambers stored at paths (from ListTree, the root first)
// with one range read when the storage can scan (see storage.ScanLayers), so
// that a snapshot's views are built without a read per chamber. Streams of the
// same root share the scan, once per change; chambers already read since the
// change are kept.
func (s *sharedViews) prefill(ctx context.Context, paths []string) error {
	if !storage.CanScan(s.raw) || len(paths) == 0 {
		return nil
	}
	_, err := shared(ctx, s, func(g *viewGeneration) map[string]*sharedCall[struct{}] { return g.scans }, paths[0], func(ctx context.Context, g *viewGeneration) (struct{}, error) {
		layers, _, err := storage.ScanLayers(ctx, s.raw, paths)
		if err != nil {
			logging.Ctx(ctx).WarnCtx(ctx).Str("root", paths[0]).Str("error", err.Error()).Msg("subtree streams could not scan the subtree; they will retry")
			return struct{}{}, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for p, l := range layers {
			if _, ok := g.layers[p]; ok {
				continue
			}
			c := &sharedCall[storage.Layer]{done: make(chan struct{}), val: l}
			close(c.done)
			g.layers[p] = c
		}
		return struct{}{}, nil
	})
	return err
}

// view returns the view of p, built from shared reads.
func (s *sharedViews) view(ctx context.Context, p string) (builtView, error) {
	return shared(ctx, s, func(g *viewGeneration) map[string]*sharedCall[builtView] { return g.views }, p, func(ctx context.Context, _ *viewGeneration) (builtView, error) {
		v, err := storage.BuildView(ctx, s.layer, p, s.inheritable)
		if err != nil {
			return builtView{}, err
		}
		b := builtView{View: v}
		event := subtreeEvent{Path: p}
		if v.Err != nil {
			event.Error = v.Err.Error()
		} else {
			event.Rules, b.id = v.Chamber.Rules, v.Revision
		}
		if b.data, err = json.Marshal(event); err != nil {
			return builtView{}, err
		}
		b.sum = sha256.Sum256(b.data)
		if v.Err != nil {
			// it names a chamber that cannot be parsed, which some may
			// not read
			event.Error = hiddenChamberError
			if b.hidden, err = json.Marshal(event); err != nil {
				return builtView{}, err
			}
			b.hiddenSum = sha256.Sum256(b.hidden)
		}
		return b, nil
	})
}

// encoded returns the view of p, encoded as Chamber returns it, built from
// shared reads. It returns a NotFoundError when no chamber is stored at p,
// and why the view cannot be built when it cannot.
func (s *sharedViews) encoded(ctx context.Context, p string) (encodedChamber, error) {
	return shared(ctx, s, func(g *viewGeneration) map[string]*sharedCall[encodedChamber] { return g.chambers }, p, func(ctx context.Context, _ *viewGeneration) (encodedChamber, error) {
		v, err := s.view(ctx, p)
		switch {
		case err != nil:
			return encodedChamber{}, err
		case !v.Stored:
			return encodedChamber{}, &storage.NotFoundError{Key: p}
		case v.Err != nil:
			return encodedChamber{}, v.Err
		}
		data, err := encodeChamber(v.Chamber)
		return encodedChamber{data: data, revision: v.Revision}, err
	})
}
