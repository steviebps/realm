package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeSession is a feedSession whose runs a test scripts: each run calls
// step with its number (from 0), and returns step's error.
type fakeSession struct {
	mu     sync.Mutex
	starts []time.Time
	step   func(ctx context.Context, n int, w *feedWatch) error
}

func (s *fakeSession) run(ctx context.Context, w *feedWatch) error {
	s.mu.Lock()
	n := len(s.starts)
	s.starts = append(s.starts, time.Now())
	s.mu.Unlock()
	return s.step(ctx, n, w)
}

func (s *fakeSession) runs() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.starts...)
}

// testFeed returns a changeFeed with short durations.
func testFeed(t *testing.T, retry, maxRetry, stale time.Duration) *changeFeed {
	t.Helper()
	f := newChangeFeed(feedConfig{name: "test", retry: &retry, maxRetry: &maxRetry, staleAfter: &stale})
	t.Cleanup(func() { _ = f.close(func() error { return nil }) })
	return f
}

// TestChangeFeedRestartsWithBackoff checks that a session that fails is run
// again, after a wait that doubles up to the maximum, and that what each run
// sends reaches the Watch.
func TestChangeFeedRestartsWithBackoff(t *testing.T) {
	f := testFeed(t, 20*time.Millisecond, 80*time.Millisecond, time.Minute)
	sess := &fakeSession{step: func(ctx context.Context, n int, w *feedWatch) error {
		if err := w.send(ctx, Change{Rev: uint64(n), Key: "/"}); err != nil {
			return err
		}
		if n == 5 {
			<-ctx.Done()
			return ctx.Err()
		}
		return errors.New("connection lost")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	out := f.watch(ctx, func(context.Context, *feedWatch) feedSession { return sess })

	for want := range uint64(6) {
		select {
		case c := <-out:
			if c.Rev != want {
				t.Fatalf("change %d = %+v", want, c)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("change %d never arrived", want)
		}
	}
	starts := sess.runs()
	// waits before runs 1..5: 20, 40, 80, 80, 80 ms
	for i, want := range []time.Duration{20, 40, 80, 80, 80} {
		if gap := starts[i+1].Sub(starts[i]); gap < want*time.Millisecond {
			t.Errorf("wait before run %d = %v, want at least %v", i+1, gap, want*time.Millisecond)
		}
	}
	cancel()
	select {
	case _, open := <-out:
		if open {
			t.Error("a change after the Watch was canceled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Watch's channel was not closed after it was canceled")
	}
}

// TestChangeFeedBackoffStartsOverAfterAHeldSession checks that a session
// that held longer than the maximum wait resets the backoff.
func TestChangeFeedBackoffStartsOverAfterAHeldSession(t *testing.T) {
	f := testFeed(t, 30*time.Millisecond, 60*time.Millisecond, time.Minute)
	sess := &fakeSession{step: func(ctx context.Context, n int, _ *feedWatch) error {
		switch {
		case n == 2:
			time.Sleep(100 * time.Millisecond) // held longer than the maximum
		case n >= 4:
			<-ctx.Done()
			return ctx.Err()
		}
		return errors.New("ended")
	}}
	_ = f.watch(context.Background(), func(context.Context, *feedWatch) feedSession { return sess })
	eventually(t, 5*time.Second, "five runs", func() bool { return len(sess.runs()) >= 5 })
	starts := sess.runs()
	// after the held run (2, which ended at starts[3] minus the wait), the
	// wait is back to 30ms rather than 60ms
	end2 := starts[2].Add(100 * time.Millisecond)
	if gap := starts[3].Sub(end2); gap >= 60*time.Millisecond {
		t.Errorf("wait after a held session = %v, want the first wait again (30ms)", gap)
	}
}

// TestChangeFeedHealth checks that Health fails while a Watch has not caught
// up within staleAfter, and recovers once it does.
func TestChangeFeedHealth(t *testing.T) {
	f := testFeed(t, 10*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond)
	if err := f.health(); err != nil {
		t.Fatalf("Health without a Watch = %v", err)
	}
	catchUp := make(chan struct{})
	sess := &fakeSession{step: func(ctx context.Context, _ int, w *feedWatch) error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-catchUp:
				w.caughtUp()
			}
		}
	}}
	_ = f.watch(context.Background(), func(context.Context, *feedWatch) feedSession { return sess })
	if err := f.health(); err != nil {
		t.Fatalf("Health of a new Watch = %v", err)
	}
	eventually(t, 2*time.Second, "Health failing for a feed that does not catch up", func() bool { return f.health() != nil })
	catchUp <- struct{}{}
	eventually(t, 2*time.Second, "Health recovering once the feed catches up", func() bool { return f.health() == nil })
}

// TestChangeFeedClose checks that close stops every Watch, waits for them,
// and releases the backend once however often it is called.
func TestChangeFeedClose(t *testing.T) {
	retry := 10 * time.Millisecond
	f := newChangeFeed(feedConfig{name: "test", retry: &retry, maxRetry: &retry, staleAfter: &retry})
	ended := make(chan struct{})
	sess := &fakeSession{step: func(ctx context.Context, _ int, _ *feedWatch) error {
		<-ctx.Done()
		close(ended)
		return ctx.Err()
	}}
	out := f.watch(context.Background(), func(context.Context, *feedWatch) feedSession { return sess })
	eventually(t, 2*time.Second, "the session running", func() bool { return len(sess.runs()) == 1 })

	releases := 0
	release := func() error {
		select {
		case <-ended:
		default:
			t.Error("released before the Watch ended")
		}
		releases++
		return errors.New("released")
	}
	if err := f.close(release); err == nil || err.Error() != "released" {
		t.Errorf("close = %v, want release's error", err)
	}
	if err := f.close(release); err != nil || releases != 1 {
		t.Errorf("a second close = %v after %d releases, want nil after 1", err, releases)
	}
	if _, open := <-out; open {
		t.Error("the Watch's channel is open after close")
	}
	if _, ok := f.age(); ok {
		t.Error("a Watch is still registered after close")
	}
}
