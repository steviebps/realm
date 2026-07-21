package http

import (
	"strings"
	"sync"

	"github.com/steviebps/realm/utils"
)

// broker is an in-process publish/subscribe hub that notifies chamber watchers
// when a chamber at (or above) their watched path changes.
//
// It is deliberately small: subscribers receive a coalesced "you are stale"
// signal rather than the changed data, so a slow subscriber can never block a
// writer and at most one pending signal is ever queued per subscriber.
//
// The broker is process-local. With multiple server replicas, a write handled
// by one replica does not notify watchers connected to another; those watchers
// converge on their next reconnect or poll. Cross-replica fanout is a follow-up
// (see docs/ARCHITECTURE.md).
type broker struct {
	mu   sync.Mutex
	subs map[int]*subscription
	next int
}

type subscription struct {
	// path is the watched chamber path, normalized with a trailing slash.
	path string
	// ch carries change signals. It has a buffer of one and sends are
	// non-blocking, so it holds at most one pending "stale" signal.
	ch chan struct{}
}

func newBroker() *broker {
	return &broker{subs: make(map[int]*subscription)}
}

// Subscribe registers a watcher for watchPath and returns a channel that
// receives a signal whenever a change affects that path, along with a function
// that unregisters the watcher and closes the channel.
func (b *broker) Subscribe(watchPath string) (<-chan struct{}, func()) {
	sub := &subscription{
		path: utils.EnsureTrailingSlash(watchPath),
		ch:   make(chan struct{}, 1),
	}

	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = sub
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}

	return sub.ch, unsubscribe
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
