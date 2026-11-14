package http

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestBrokerTreeSubscription(t *testing.T) {
	tests := []struct {
		name    string
		changed string
		woken   bool
	}{
		{"the root", "/a/b/", true},
		{"above the root", "/a/", true},
		{"the top", "/", true},
		{"below the root", "/a/b/c/", true},
		{"far below the root", "/a/b/c/d/e/", true},
		{"a sibling", "/a/c/", false},
		{"a name sharing a prefix", "/a/bc/", false},
		{"elsewhere", "/x/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBroker(0)
			sub, unsub, ok := b.SubscribeTree("/a/b")
			if !ok {
				t.Fatal("subscription refused")
			}
			defer unsub()
			b.Notify(tt.changed)
			if got := received(sub.C()); got != tt.woken {
				t.Fatalf("woken = %v, want %v", got, tt.woken)
			}
			changed, overflow := sub.Take()
			var want []string
			if tt.woken {
				want = []string{tt.changed}
			}
			if overflow || !slices.Equal(changed, want) {
				t.Errorf("Take() = %v, %v; want %v, false", changed, overflow, want)
			}
		})
	}
}

func TestBrokerTreeSubscriptionCoalesces(t *testing.T) {
	b := newBroker(0)
	sub, unsub, _ := b.SubscribeTree("/a/")
	defer unsub()

	for range 3 {
		b.Notify("/a/x/")
		b.Notify("/a/y")
	}
	if !received(sub.C()) || received(sub.C()) {
		t.Fatal("expected one pending signal")
	}
	changed, overflow := sub.Take()
	slices.Sort(changed)
	if overflow || !slices.Equal(changed, []string{"/a/x/", "/a/y/"}) {
		t.Errorf("Take() = %v, %v; want each path once", changed, overflow)
	}
	if changed, overflow := sub.Take(); len(changed) != 0 || overflow {
		t.Errorf("second Take() = %v, %v; want nothing", changed, overflow)
	}
}

func TestBrokerTreeSubscriptionOverflows(t *testing.T) {
	old := maxDirtyPaths
	maxDirtyPaths = 3
	t.Cleanup(func() { maxDirtyPaths = old })

	b := newBroker(0)
	sub, unsub, _ := b.SubscribeTree("/a/")
	defer unsub()
	for i := range 4 {
		b.Notify(fmt.Sprintf("/a/%d/", i))
	}
	if changed, overflow := sub.Take(); !overflow || len(changed) != 0 {
		t.Errorf("Take() = %v, %v; want an overflow", changed, overflow)
	}
	// and recording starts again
	b.Notify("/a/x/")
	if changed, overflow := sub.Take(); overflow || !slices.Equal(changed, []string{"/a/x/"}) {
		t.Errorf("Take() after an overflow = %v, %v", changed, overflow)
	}
}

func TestBrokerTreeSubscriptionsCount(t *testing.T) {
	b := newBroker(1)
	_, unsub, ok := b.SubscribeTree("/a/")
	if !ok {
		t.Fatal("first subscription refused")
	}
	if _, _, ok := b.Subscribe("/b/", false); ok {
		t.Fatal("subscription beyond the cap was accepted")
	}
	unsub()
	if _, _, ok := b.Subscribe("/b/", false); !ok {
		t.Fatal("subscription refused after the tree subscription ended")
	}
}

func received(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(100 * time.Millisecond):
		return false
	}
}

func TestBrokerNotifyExactPath(t *testing.T) {
	b := newBroker(0)
	ch, unsub, _ := b.Subscribe("root/", false)
	defer unsub()

	b.Notify("root/")
	if !received(ch) {
		t.Fatal("expected a signal for an exact path match")
	}
}

func TestBrokerNotifyParentWakesChild(t *testing.T) {
	b := newBroker(0)
	// A watcher on the child must be notified when the parent changes, because
	// under inheritable storage the child's merged view changes too.
	ch, unsub, _ := b.Subscribe("a/b/", false)
	defer unsub()

	b.Notify("a/")
	if !received(ch) {
		t.Fatal("expected a parent write to wake a child watcher")
	}
}

func TestBrokerNotifyChildDoesNotWakeParent(t *testing.T) {
	b := newBroker(0)
	ch, unsub, _ := b.Subscribe("a/", false)
	defer unsub()

	b.Notify("a/b/c/")
	if received(ch) {
		t.Fatal("a deeper write should not wake an ancestor watcher")
	}
}

func TestBrokerNotifyUnrelatedPath(t *testing.T) {
	b := newBroker(0)
	ch, unsub, _ := b.Subscribe("a/b/", false)
	defer unsub()

	b.Notify("x/y/")
	if received(ch) {
		t.Fatal("an unrelated write should not wake the watcher")
	}
}

func TestBrokerUnsubscribeStopsSignals(t *testing.T) {
	b := newBroker(0)
	ch, unsub, _ := b.Subscribe("root/", false)
	unsub()

	b.Notify("root/")
	if received(ch) {
		t.Fatal("expected no signal after unsubscribe")
	}
}

func TestBrokerNotifyIsNonBlockingAndCoalesces(t *testing.T) {
	b := newBroker(0)
	ch, unsub, _ := b.Subscribe("root/", false)
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
	b := newBroker(0)
	// Subscribe without a trailing slash; Notify without one either.
	ch, unsub, _ := b.Subscribe("root", false)
	defer unsub()

	b.Notify("root")
	if !received(ch) {
		t.Fatal("expected trailing-slash normalization to match paths")
	}
}

func TestBrokerMaxSubscriptions(t *testing.T) {
	b := newBroker(1)
	_, unsub, ok := b.Subscribe("a/", false)
	if !ok {
		t.Fatal("first subscription refused")
	}
	if _, _, ok := b.Subscribe("b/", false); ok {
		t.Fatal("subscription beyond the cap was accepted")
	}
	unsub()
	unsub() // idempotent: must not free a second slot
	if _, _, ok := b.Subscribe("b/", false); !ok {
		t.Fatal("subscription refused after a slot was freed")
	}
	if _, _, ok := b.Subscribe("c/", false); ok {
		t.Fatal("double unsubscribe freed two slots")
	}
}

// TestBrokerReservesStreamsForSDKs verifies that public subscriptions cannot
// take the whole cap: a quarter stays free for the others.
func TestBrokerReservesStreamsForSDKs(t *testing.T) {
	b := newBroker(8)
	var unsubs []func()
	for i := range 6 {
		_, unsub, ok := b.Subscribe("web/", true)
		if !ok {
			t.Fatalf("public subscription %d refused", i+1)
		}
		unsubs = append(unsubs, unsub)
	}
	if _, _, ok := b.Subscribe("web/", true); ok {
		t.Fatal("a public subscription took a reserved slot")
	}
	for i := range 2 {
		if _, _, ok := b.Subscribe("api/", false); !ok {
			t.Fatalf("subscription %d refused while public ones were capped", i+1)
		}
	}
	if _, _, ok := b.Subscribe("api/", false); ok {
		t.Fatal("subscription beyond the cap was accepted")
	}
	unsubs[0]()
	if _, _, ok := b.Subscribe("web/", true); !ok {
		t.Fatal("public subscription refused after one ended")
	}
}
