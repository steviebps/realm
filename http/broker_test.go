package http

import (
	"testing"
	"time"
)

func received(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(100 * time.Millisecond):
		return false
	}
}

func TestBrokerNotifyExactPath(t *testing.T) {
	b := newBroker()
	ch, unsub := b.Subscribe("root/")
	defer unsub()

	b.Notify("root/")
	if !received(ch) {
		t.Fatal("expected a signal for an exact path match")
	}
}

func TestBrokerNotifyParentWakesChild(t *testing.T) {
	b := newBroker()
	// A watcher on the child must be notified when the parent changes, because
	// under inheritable storage the child's merged view changes too.
	ch, unsub := b.Subscribe("a/b/")
	defer unsub()

	b.Notify("a/")
	if !received(ch) {
		t.Fatal("expected a parent write to wake a child watcher")
	}
}

func TestBrokerNotifyChildDoesNotWakeParent(t *testing.T) {
	b := newBroker()
	ch, unsub := b.Subscribe("a/")
	defer unsub()

	b.Notify("a/b/c/")
	if received(ch) {
		t.Fatal("a deeper write should not wake an ancestor watcher")
	}
}

func TestBrokerNotifyUnrelatedPath(t *testing.T) {
	b := newBroker()
	ch, unsub := b.Subscribe("a/b/")
	defer unsub()

	b.Notify("x/y/")
	if received(ch) {
		t.Fatal("an unrelated write should not wake the watcher")
	}
}

func TestBrokerUnsubscribeStopsSignals(t *testing.T) {
	b := newBroker()
	ch, unsub := b.Subscribe("root/")
	unsub()

	b.Notify("root/")
	if received(ch) {
		t.Fatal("expected no signal after unsubscribe")
	}
}

func TestBrokerNotifyIsNonBlockingAndCoalesces(t *testing.T) {
	b := newBroker()
	ch, unsub := b.Subscribe("root/")
	defer unsub()

	// Many notifies with no reader must not block; they coalesce into the
	// single-slot buffer.
	for i := 0; i < 100; i++ {
		b.Notify("root/")
	}

	if !received(ch) {
		t.Fatal("expected at least one pending signal")
	}
	if received(ch) {
		t.Fatal("expected signals to coalesce into a single pending slot")
	}
}

func TestBrokerNormalizesTrailingSlash(t *testing.T) {
	b := newBroker()
	// Subscribe without a trailing slash; Notify without one either.
	ch, unsub := b.Subscribe("root")
	defer unsub()

	b.Notify("root")
	if !received(ch) {
		t.Fatal("expected trailing-slash normalization to match paths")
	}
}
