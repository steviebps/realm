package http

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel/metric"
)

// broker is an in-process publish/subscribe hub that notifies chamber watchers
// when a chamber at (or above) their watched path changes.
//
// It is deliberately small: subscribers receive a coalesced "you are stale"
// signal rather than the changed data, so a slow subscriber can never block a
// writer and at most one pending signal is ever queued per subscriber.
//
// The broker is process-local. When the storage is Watchable, its change feed
// (which includes other replicas' writes) drives Notify, so watchers on every
// replica hear of every change; otherwise the handler notifies it after its own
// writes only (see docs/ARCHITECTURE.md).
type broker struct {
	mu   sync.Mutex
	subs map[int]*subscription
	next int
	// max caps len(subs); zero means unlimited.
	max int
	// public counts the subscriptions in subs made with public set.
	public int
	// gen counts Notify calls (see Generation).
	gen atomic.Uint64
}

// publicMax returns how many public subscriptions the broker holds at most:
// three quarters of max, so that streams anyone can open (evaluation streams,
// opened with evaluate keys published in web pages) can never take the slots
// that server SDKs need to hear about changes. Zero means unlimited.
func (b *broker) publicMax() int {
	return b.max - b.max/4
}

// Generation returns the number of changes announced so far. Notify advances
// it before it signals any subscriber, so a subscriber woken by a change reads
// a generation at least as new as that change.
func (b *broker) Generation() uint64 {
	return b.gen.Load()
}

type subscription struct {
	// path is the watched chamber path, normalized with a trailing slash.
	path string
	// ch carries change signals. It has a buffer of one and sends are
	// non-blocking, so it holds at most one pending "stale" signal.
	ch chan struct{}
	// tree marks a subtree subscription (see SubscribeTree), which records
	// the paths that changed in dirty.
	tree bool

	mu sync.Mutex
	// dirty holds the paths changed since the subscriber last took them.
	dirty map[string]struct{}
	// overflow is set once more paths changed than dirty may hold before
	// the subscriber took them (see maxDirtyPaths).
	overflow bool
}

// maxDirtyPaths bounds the changed paths a subtree subscription records
// between two takes; past it, the subscriber treats every view as changed.
var maxDirtyPaths = 1024

// affectedBy reports whether a change at changed (normalized with a trailing
// slash) affects the subscriber. A chamber's view changes with a change at or
// above it, so a watcher is affected when its path starts with changed. A
// subtree watcher is also affected by every change below its root.
func (s *subscription) affectedBy(changed string) bool {
	if strings.HasPrefix(s.path, changed) {
		return true
	}
	return s.tree && strings.HasPrefix(changed, s.path)
}

// record notes, for a subtree subscription, that path changed.
func (s *subscription) record(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.overflow {
		return
	}
	if _, ok := s.dirty[path]; ok {
		return
	}
	if len(s.dirty) >= maxDirtyPaths {
		// the subscriber will look at everything: the paths no longer matter
		s.overflow = true
		clear(s.dirty)
		return
	}
	s.dirty[path] = struct{}{}
}

// treeSubscription is a subscription to a subtree: a root and every chamber
// below it (see SubscribeTree).
type treeSubscription struct {
	sub *subscription
}

// C returns the channel that receives a signal whenever a change affects the
// subtree. Signals coalesce, so the subscriber takes the changed paths with
// Take after each one.
func (t *treeSubscription) C() <-chan struct{} {
	return t.sub.ch
}

// Take returns the paths changed since the last Take, each once and in no
// particular order, and forgets them. overflow reports that more paths
// changed than were recorded (see maxDirtyPaths): the subscriber should
// treat every view as changed.
func (t *treeSubscription) Take() (changed []string, overflow bool) {
	s := t.sub
	s.mu.Lock()
	defer s.mu.Unlock()
	changed = make([]string, 0, len(s.dirty))
	for p := range s.dirty {
		changed = append(changed, p)
	}
	overflow = s.overflow
	clear(s.dirty)
	s.overflow = false
	return changed, overflow
}

// newBroker returns a broker holding at most max subscriptions (zero means
// unlimited).
func newBroker(max int) *broker {
	return &broker{subs: make(map[int]*subscription), max: max}
}

// Subscribe registers a watcher for watchPath and returns a channel that
// receives a signal whenever a change affects that path, along with a function
// that unregisters the watcher. public marks a watcher anyone may open (see
// publicMax). ok is false, and nothing is registered, when the broker already
// holds its maximum number of subscriptions, or of public ones.
func (b *broker) Subscribe(watchPath string, public bool) (_ <-chan struct{}, _ func(), ok bool) {
	sub := &subscription{
		path: utils.EnsureTrailingSlash(watchPath),
		ch:   make(chan struct{}, 1),
	}
	unsubscribe, ok := b.add(sub, public)
	if !ok {
		return nil, nil, false
	}
	return sub.ch, unsubscribe, true
}

// SubscribeTree registers a watcher for root and every chamber below it, and
// returns its subscription along with a function that unregisters it. The
// subscription is signaled by every change at, above, or below root, and
// records which paths changed (see treeSubscription.Take). ok is false, and
// nothing is registered, when the broker already holds its maximum number of
// subscriptions.
func (b *broker) SubscribeTree(root string) (_ *treeSubscription, _ func(), ok bool) {
	sub := &subscription{
		path:  utils.EnsureTrailingSlash(root),
		ch:    make(chan struct{}, 1),
		tree:  true,
		dirty: map[string]struct{}{},
	}
	unsubscribe, ok := b.add(sub, false)
	if !ok {
		return nil, nil, false
	}
	return &treeSubscription{sub: sub}, unsubscribe, true
}

// add registers sub unless the broker is full (see Subscribe), and returns
// the function that unregisters it.
func (b *broker) add(sub *subscription, public bool) (_ func(), ok bool) {
	b.mu.Lock()
	if b.max > 0 && (len(b.subs) >= b.max || (public && b.public >= b.publicMax())) {
		b.mu.Unlock()
		return nil, false
	}
	id := b.next
	b.next++
	b.subs[id] = sub
	if public {
		b.public++
	}
	activeStreams.Add(context.Background(), 1)
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			if public {
				b.public--
			}
			b.mu.Unlock()
			activeStreams.Add(context.Background(), -1)
		})
	}
	return unsubscribe, true
}

// Notify wakes every subscriber whose watched path is at or below changedPath,
// and every subtree subscriber whose root is above it. Subtree subscribers
// also record changedPath (see treeSubscription.Take).
//
// A watcher on "/a/b/" is affected by a write to "/a/" (its inherited view may
// change) but not by a write to "/a/b/c/". Matching is therefore a prefix test:
// the watched path must start with the changed path. A subtree watcher on
// "/a/b/" is affected by both.
func (b *broker) Notify(changedPath string) {
	changed := utils.EnsureTrailingSlash(changedPath)

	b.gen.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		if !sub.affectedBy(changed) {
			continue
		}
		if sub.tree {
			sub.record(changed)
		}
		// Non-blocking: if a signal is already pending, coalesce into it.
		select {
		case sub.ch <- struct{}{}:
		default:
		}
	}
}

var activeStreams, _ = meter.Int64UpDownCounter(
	"realm.server.streams.active",
	metric.WithDescription("Watch (SSE) streams currently open."),
	metric.WithUnit("{stream}"))
