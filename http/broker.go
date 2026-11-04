package http

import (
	"context"
	"strings"
	"sync"

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
}

type subscription struct {
	// path is the watched chamber path, normalized with a trailing slash.
	path string
	// ch carries change signals. It has a buffer of one and sends are
	// non-blocking, so it holds at most one pending "stale" signal.
	ch chan struct{}
}

// newBroker returns a broker holding at most max subscriptions (zero means
// unlimited).
func newBroker(max int) *broker {
	return &broker{subs: make(map[int]*subscription), max: max}
}

// Subscribe registers a watcher for watchPath and returns a channel that
// receives a signal whenever a change affects that path, along with a function
// that unregisters the watcher. ok is false, and nothing is registered, when
// the broker already holds its maximum number of subscriptions.
func (b *broker) Subscribe(watchPath string) (_ <-chan struct{}, _ func(), ok bool) {
	sub := &subscription{
		path: utils.EnsureTrailingSlash(watchPath),
		ch:   make(chan struct{}, 1),
	}

	b.mu.Lock()
	if b.max > 0 && len(b.subs) >= b.max {
		b.mu.Unlock()
		return nil, nil, false
	}
	id := b.next
	b.next++
	b.subs[id] = sub
	activeStreams.Add(context.Background(), 1)
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			activeStreams.Add(context.Background(), -1)
		})
	}

	return sub.ch, unsubscribe, true
}

// Notify wakes every subscriber whose watched path is at or below changedPath.
//
// A watcher on "/a/b/" is affected by a write to "/a/" (its inherited view may
// change) but not by a write to "/a/b/c/". Matching is therefore a prefix test:
// the watched path must start with the changed path.
func (b *broker) Notify(changedPath string) {
	changed := utils.EnsureTrailingSlash(changedPath)

	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		if !strings.HasPrefix(sub.path, changed) {
			continue
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
