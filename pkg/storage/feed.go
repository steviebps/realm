package storage

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steviebps/realm/helper/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// changeFeed runs the Watches (see Watchable) of a backend that several
// servers share. It owns what every such backend's feed needs alike: the
// running Watches, restarting a session that ends with backoff, when each
// Watch last caught up with the backend, Health, the feed age metric, and
// stopping every Watch on Close. A backend supplies only a feedSession: one
// connection's worth of reading its changes (Postgres: LISTEN and catching
// up; etcd: an etcd watch).
type changeFeed struct {
	cfg feedConfig

	// ctx is canceled by close, stopping every Watch.
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once

	// mu guards watches, the running Watches, which Health and the feed
	// age metric inspect.
	mu      sync.Mutex
	watches map[*feedWatch]struct{}
	// metrics is the registration of the feed age callback, if any.
	metrics metric.Registration
}

// feedConfig describes a backend's feed. Its durations are read each time
// they are used, so that tests can shorten them.
type feedConfig struct {
	// name names the backend in log messages and errors, such as "postgres".
	name string
	// retry is how long the feed first waits before starting a session
	// again after one ends; the wait doubles up to maxRetry, and starts over
	// after a session that held longer than maxRetry.
	retry, maxRetry *time.Duration
	// staleAfter is how long a Watch may go without catching up before
	// Health reports it.
	staleAfter *time.Duration
	// restarts counts sessions that ended; age observes the feed age.
	restarts metric.Int64Counter
	age      metric.Float64ObservableGauge
}

// feedSession is one Watch's reading of a backend's changes.
type feedSession interface {
	// run reads changes, sending them with its Watch's send and calling its
	// caughtUp whenever it has every change the backend committed, until it
	// fails or ctx is done. The feed runs it again, with backoff, after it
	// fails.
	run(ctx context.Context) error
}

// feedWatch is one running Watch.
type feedWatch struct {
	out chan<- Change
	// session reads its changes.
	session feedSession
	// caughtUpAt is when it last caught up with the backend (or started),
	// in Unix nanoseconds.
	caughtUpAt atomic.Int64
}

// send reports c, unless ctx is done first.
func (w *feedWatch) send(ctx context.Context, c Change) error {
	select {
	case w.out <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// caughtUp records that the Watch has every change the backend committed.
func (w *feedWatch) caughtUp() {
	w.caughtUpAt.Store(time.Now().UnixNano())
}

// newChangeFeed returns the feed of a backend, measuring its age with
// cfg.age.
func newChangeFeed(cfg feedConfig) *changeFeed {
	ctx, cancel := context.WithCancel(context.Background())
	f := &changeFeed{cfg: cfg, ctx: ctx, cancel: cancel, watches: map[*feedWatch]struct{}{}}
	if cfg.age != nil {
		// without a working meter, the feed simply goes unmeasured
		f.metrics, _ = otel.Meter("github.com/steviebps/realm").RegisterCallback(func(_ context.Context, o metric.Observer) error {
			if age, ok := f.age(); ok {
				o.ObserveFloat64(cfg.age, age.Seconds())
			}
			return nil
		}, cfg.age)
	}
	return f
}

// watch starts a Watch: start returns its session, given the Watch (whose
// clock starts now, so a new feed has staleAfter to catch up before Health
// reports it). The Watch runs the session, again with backoff after each
// failure, until ctx is done or the feed is closed, then closes the channel
// it returns.
func (f *changeFeed) watch(ctx context.Context, start func(ctx context.Context, w *feedWatch) feedSession) <-chan Change {
	out := make(chan Change, 64)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(f.ctx, cancel)

	w := &feedWatch{out: out}
	w.caughtUp()
	w.session = start(ctx, w)

	f.mu.Lock()
	f.watches[w] = struct{}{}
	f.mu.Unlock()
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		defer close(out)
		defer stop()
		defer cancel()
		defer func() {
			f.mu.Lock()
			delete(f.watches, w)
			f.mu.Unlock()
		}()
		f.run(ctx, w)
	}()
	return out
}

// run runs w's session until ctx is done, starting it again after each
// failure: after retry at first, doubling up to maxRetry, and from retry
// again after a session that held longer than maxRetry.
func (f *changeFeed) run(ctx context.Context, w *feedWatch) {
	logger := logging.Ctx(ctx)
	delay := *f.cfg.retry
	for ctx.Err() == nil {
		began := time.Now()
		err := w.session.run(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(began) > *f.cfg.maxRetry {
			delay = *f.cfg.retry
		}
		if f.cfg.restarts != nil {
			f.cfg.restarts.Add(ctx, 1)
		}
		logger.WarnCtx(ctx).Str("error", fmt.Sprint(err)).Dur("retry_in", delay).Msgf("%s change feed ended; restarting it", f.cfg.name)
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		delay = min(delay*2, *f.cfg.maxRetry)
	}
}

// age returns how long ago the running Watch furthest behind last caught
// up; ok is false when no Watch is running.
func (f *changeFeed) age() (age time.Duration, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for w := range f.watches {
		if a := now.Sub(time.Unix(0, w.caughtUpAt.Load())); !ok || a > age {
			age, ok = a, true
		}
	}
	return age, ok
}

// health fails while a Watch has gone longer than staleAfter without
// catching up: the server may be missing other servers' changes, and its
// streams with them.
func (f *changeFeed) health() error {
	if age, ok := f.age(); ok && age > *f.cfg.staleAfter {
		return fmt.Errorf("the %s change feed has not caught up for %v", f.cfg.name, age.Truncate(time.Second))
	}
	return nil
}

// close stops every Watch and waits for them to end, then runs release,
// once however often it is called. It returns release's error.
func (f *changeFeed) close(release func() error) error {
	var err error
	f.closeOnce.Do(func() {
		f.cancel()
		f.wg.Wait()
		if f.metrics != nil {
			_ = f.metrics.Unregister()
		}
		err = release()
	})
	return err
}
