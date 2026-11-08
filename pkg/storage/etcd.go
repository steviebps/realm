package storage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/utils"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// EtcdStorage stores entries in an etcd cluster. It is Transactional and
// Watchable, so any number of servers can share one cluster: writes are
// version-checked by etcd, and every server learns of every change through an
// etcd watch.
//
// An entry's Version is the etcd revision that last modified it. etcd issues
// one revision per committed write, cluster-wide, so revisions increase but
// skip numbers when other clients share the cluster.
type EtcdStorage struct {
	client *clientv3.Client
	// prefix namespaces realm's keys; an entry at key k is stored at
	// prefix + etcdPath(k).
	prefix string
	tracer trace.Tracer

	// ctx is canceled by Close, stopping every Watch.
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once

	// mu guards watchers, the running Watches, which Health and the feed age
	// metric inspect.
	mu       sync.Mutex
	watchers map[*etcdWatcher]struct{}
	metrics  metric.Registration
}

var (
	_ Transactional  = (*EtcdStorage)(nil)
	_ Watchable      = (*EtcdStorage)(nil)
	_ HealthReporter = (*EtcdStorage)(nil)
)

var (
	// etcdTimeout bounds connecting, the checks NewEtcdStorage makes, and
	// every request a Watch makes of its own.
	etcdTimeout = 10 * time.Second
	// etcdListPage is how many keys List reads per request.
	etcdListPage int64 = 512
	// etcdProgressInterval is how often a Watch checks that it is current
	// (see etcdWatcher.probe), which is how it notices a feed that stopped
	// because the cluster is unreachable.
	etcdProgressInterval = 5 * time.Second
	// etcdFeedStaleAfter is how long a Watch may go without being found
	// current before Health reports it.
	etcdFeedStaleAfter = 30 * time.Second
	// etcdRetryInterval is how long a Watch first waits before restarting
	// after etcd ends it; the wait doubles up to etcdMaxRetryInterval.
	etcdRetryInterval    = time.Second
	etcdMaxRetryInterval = 30 * time.Second
)

// NewEtcdStorage creates etcd-backed storage. Options:
//
//	endpoints  comma-separated client URLs, such as https://etcd-0:2379
//	prefix     the key prefix realm's entries live under (default /realm),
//	           so that several realm installations, or other applications,
//	           can share a cluster; "/" for none
//	username   optional user for etcd's authentication
//	password   its password; it may name an environment variable as ${NAME}
//	ca_file    optional CA bundle to verify the servers' certificates with,
//	           instead of the system's
//	cert_file, key_file
//	           optional client certificate and key, for etcd's
//	           --client-cert-auth
//
// TLS is used when an endpoint is an https:// URL or any of the files are
// set. It checks that the cluster answers before returning.
//
// A transaction is limited by the cluster's --max-txn-ops (128 by default):
// deleting a chamber tree takes two or three operations per chamber. Writes
// are limited by --max-request-bytes (1.5 MiB by default).
func NewEtcdStorage(conf map[string]string) (Storage, error) {
	var endpoints []string
	for e := range strings.SplitSeq(conf["endpoints"], ",") {
		if e = strings.TrimSpace(e); e != "" {
			endpoints = append(endpoints, e)
		}
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("'endpoints' must be set")
	}
	prefix := conf["prefix"]
	if prefix == "" {
		prefix = "/realm"
	}
	// "/" places entries at the root of the cluster's keys
	prefix = strings.TrimRight(prefix, "/")
	if err := ValidatePath(prefix); err != nil {
		return nil, fmt.Errorf("invalid 'prefix': %w", err)
	}

	tlsConf, err := etcdTLS(endpoints, conf)
	if err != nil {
		return nil, err
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: etcdTimeout,
		// ping idle connections, so a member that went away is noticed in
		// about 20 seconds rather than the operating system's minutes
		DialKeepAliveTime:    15 * time.Second,
		DialKeepAliveTimeout: 5 * time.Second,
		PermitWithoutStream:  true,
		TLS:                  tlsConf,
		Username:             conf["username"],
		Password:             utils.ExpandEnvRef(conf["password"]),
		// realm reports what goes wrong itself
		Logger: zap.NewNop(),
	})
	if err != nil {
		return nil, fmt.Errorf("could not connect to etcd: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), etcdTimeout)
	defer cancel()
	if _, err := client.Get(ctx, prefix+"/", clientv3.WithCountOnly()); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("could not reach etcd at %s: %w", strings.Join(endpoints, ", "), err)
	}

	sctx, scancel := context.WithCancel(context.Background())
	s := &EtcdStorage{
		client:   client,
		prefix:   prefix,
		tracer:   otel.Tracer("github.com/steviebps/realm"),
		ctx:      sctx,
		cancel:   scancel,
		watchers: map[*etcdWatcher]struct{}{},
	}
	// without a working meter, the feed simply goes unmeasured
	s.metrics, _ = otel.Meter("github.com/steviebps/realm").RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if age, ok := s.feedAge(); ok {
			o.ObserveFloat64(etcdFeedAgeGauge, age.Seconds())
		}
		return nil
	}, etcdFeedAgeGauge)
	return s, nil
}

// etcdTLS returns the TLS configuration for endpoints, or nil when they are
// reached without TLS.
func etcdTLS(endpoints []string, conf map[string]string) (*tls.Config, error) {
	caFile, certFile, keyFile := conf["ca_file"], conf["cert_file"], conf["key_file"]
	secure := caFile != "" || certFile != "" || keyFile != ""
	for _, e := range endpoints {
		secure = secure || strings.HasPrefix(e, "https://")
	}
	if !secure {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("could not read 'ca_file': %w", err)
		}
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("'ca_file' %s holds no PEM certificates", caFile)
		}
	}
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("'cert_file' and 'key_file' must be set together")
	}
	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("could not load the etcd client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// etcdPath normalizes a storage key: every key begins and ends with a slash,
// like the paths the server uses.
func etcdPath(k string) string {
	k = utils.EnsureTrailingSlash(k)
	if !strings.HasPrefix(k, "/") {
		k = "/" + k
	}
	return k
}

// key returns where the entry at k is stored in etcd.
func (s *EtcdStorage) key(k string) string {
	return s.prefix + etcdPath(k)
}

func (s *EtcdStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := s.tracer.Start(ctx, "EtcdStorage Get", trace.WithAttributes(attribute.String("realm.etcd.logicalPath", logicalPath)))
	defer span.End()

	logging.Ctx(ctx).DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return nil, err
	}
	resp, err := s.client.Get(ctx, s.key(logicalPath))
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, &NotFoundError{Key: logicalPath}
	}
	kv := resp.Kvs[0]
	return &StorageEntry{Key: logicalPath, Value: kv.Value, Version: uint64(kv.ModRevision)}, nil
}

func (s *EtcdStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := s.tracer.Start(ctx, "EtcdStorage Put", trace.WithAttributes(attribute.String("realm.etcd.entry.key", e.Key)))
	defer span.End()

	logging.Ctx(ctx).DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	if err := ValidatePath(e.Key); err != nil {
		recordError(span, err)
		return err
	}
	if _, err := s.client.Put(ctx, s.key(e.Key), string(e.Value)); err != nil {
		err = etcdLimitError(err, 1)
		recordError(span, err)
		return err
	}
	return nil
}

func (s *EtcdStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := s.tracer.Start(ctx, "EtcdStorage Delete", trace.WithAttributes(attribute.String("realm.etcd.logicalPath", logicalPath)))
	defer span.End()

	logging.Ctx(ctx).DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return err
	}
	resp, err := s.client.Delete(ctx, s.key(logicalPath))
	if err != nil {
		recordError(span, err)
		return err
	}
	if resp.Deleted == 0 {
		return &NotFoundError{Key: logicalPath}
	}
	return nil
}

// List returns the names directly beneath prefix: "." when an entry exists at
// prefix itself, and "<child>/" for each child that has entries, in order.
// It reads one snapshot, a page of keys at a time, skipping the rest of a
// child's keys once it has seen the child.
func (s *EtcdStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := s.tracer.Start(ctx, "EtcdStorage List", trace.WithAttributes(attribute.String("realm.etcd.prefix", prefix)))
	defer span.End()

	logging.Ctx(ctx).DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := ValidatePath(prefix); err != nil {
		recordError(span, err)
		return nil, err
	}

	lower := s.key(prefix)
	end := clientv3.GetPrefixRangeEnd(lower)
	names := []string{}
	start := lower
	var rev int64
	for {
		opts := []clientv3.OpOption{clientv3.WithRange(end), clientv3.WithKeysOnly(), clientv3.WithLimit(etcdListPage)}
		if rev != 0 {
			opts = append(opts, clientv3.WithRev(rev))
		}
		resp, err := s.client.Get(ctx, start, opts...)
		if err != nil {
			recordError(span, err)
			return nil, err
		}
		rev = resp.Header.Revision
		for _, kv := range resp.Kvs {
			name := "."
			if rel := string(kv.Key[len(lower):]); rel != "" {
				child, _, _ := strings.Cut(rel, "/")
				name = child + "/"
			}
			if len(names) == 0 || names[len(names)-1] != name {
				names = append(names, name)
			}
		}
		if !resp.More || len(resp.Kvs) == 0 {
			break
		}
		// every key of the last child seen sorts below its name followed by
		// "0", the byte after "/"
		if last := names[len(names)-1]; last == "." {
			start = lower + "\x00"
		} else {
			start = lower + strings.TrimSuffix(last, "/") + "0"
		}
	}
	slices.Sort(names)
	return names, nil
}

// Txn applies ops atomically if every check holds. A later op on a key
// replaces an earlier one, since etcd refuses a transaction that names a key
// twice.
func (s *EtcdStorage) Txn(ctx context.Context, checks []Check, ops []Op) error {
	ctx, span := s.tracer.Start(ctx, "EtcdStorage Txn", trace.WithAttributes(attribute.Int("realm.etcd.checks", len(checks)), attribute.Int("realm.etcd.ops", len(ops))))
	defer span.End()

	for _, k := range append(checkKeys(checks), opKeys(ops)...) {
		if err := ValidatePath(k); err != nil {
			recordError(span, err)
			return err
		}
	}
	if len(checks) == 0 && len(ops) == 0 {
		return nil
	}

	cmps := make([]clientv3.Cmp, len(checks))
	for i, c := range checks {
		// a missing key compares as revision 0
		cmps[i] = clientv3.Compare(clientv3.ModRevision(s.key(c.Key)), "=", int64(c.Version))
	}
	final := map[string]Op{}
	for _, op := range ops {
		final[s.key(op.Key)] = op
	}
	writes := make([]clientv3.Op, 0, len(final))
	// in key order, so the changes of one transaction are reported in order
	for _, k := range slices.Sorted(maps.Keys(final)) {
		if v := final[k].Value; v != nil {
			writes = append(writes, clientv3.OpPut(k, string(v)))
		} else {
			writes = append(writes, clientv3.OpDelete(k))
		}
	}

	resp, err := s.client.Txn(ctx).If(cmps...).Then(writes...).Commit()
	if err != nil {
		err = etcdLimitError(err, max(len(cmps), len(writes)))
		recordError(span, err)
		return err
	}
	if !resp.Succeeded {
		return ErrConflict
	}
	return nil
}

// etcdLimitError explains the errors with which etcd refuses a request that
// exceeds the cluster's limits, wrapping ErrTooLarge.
func etcdLimitError(err error, ops int) error {
	switch {
	case errors.Is(err, rpctypes.ErrTooManyOps):
		return fmt.Errorf("%w: it needs %d operations in one transaction, more than the etcd cluster allows (its --max-txn-ops, 128 by default): %w", ErrTooLarge, ops, err)
	case errors.Is(err, rpctypes.ErrRequestTooLarge):
		return fmt.Errorf("%w: it is more than the etcd cluster accepts in one request (its --max-request-bytes, 1.5 MiB by default): %w", ErrTooLarge, err)
	}
	return err
}

// Watch reports every change committed after it is called, by any server
// sharing the cluster, from an etcd watch on realm's prefix. It restarts the
// watch where it left off when etcd ends it (when the member it is connected
// to loses the cluster's leader, say), and reports a change to "/" when it
// cannot tell what changed in between: the revisions it needed were
// compacted, or the cluster's revision went back (it was restored from an
// older backup), so that its revisions no longer match the ones reported.
func (s *EtcdStorage) Watch(ctx context.Context) <-chan Change {
	out := make(chan Change, 64)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)

	w := &etcdWatcher{s: s, out: out}
	// a new feed has etcdFeedStaleAfter to catch up before Health reports it
	w.caughtUp.Store(time.Now().UnixNano())
	// start from the current revision; on failure, the watch that starts
	// first reports that anything may have changed
	if head, err := w.head(ctx); err == nil {
		w.last = head
		w.confirmed.Store(head)
	} else {
		w.lost = true
	}

	s.mu.Lock()
	s.watchers[w] = struct{}{}
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(out)
		defer stop()
		defer cancel()
		defer func() {
			s.mu.Lock()
			delete(s.watchers, w)
			s.mu.Unlock()
		}()
		w.run(ctx)
	}()
	return out
}

type etcdWatcher struct {
	s   *EtcdStorage
	out chan<- Change
	// last is the revision up to which every change has been reported.
	last uint64
	// lost is set when changes may have been missed.
	lost bool
	// confirmed is the revision up to which etcd has confirmed delivering
	// every change; probe reads it while watch updates it.
	confirmed atomic.Uint64
	// rewound is set by probe when the cluster's revision went below
	// confirmed.
	rewound atomic.Bool
	// caughtUp is when the watch was last found current (or it started),
	// in Unix nanoseconds.
	caughtUp atomic.Int64
}

var (
	etcdFeedRestarts, _ = otel.Meter("github.com/steviebps/realm").Int64Counter(
		"realm.etcd.feed.restarts",
		metric.WithDescription("Times the etcd change feed's watch ended and was restarted."),
		metric.WithUnit("{restart}"))
	etcdFeedAgeGauge, _ = otel.Meter("github.com/steviebps/realm").Float64ObservableGauge(
		"realm.etcd.feed.age",
		metric.WithDescription("Seconds since the etcd change feed was last found current; it grows while the feed cannot reach the cluster."),
		metric.WithUnit("s"))
)

func (w *etcdWatcher) run(ctx context.Context) {
	logger := logging.Ctx(ctx)
	delay := etcdRetryInterval
	for ctx.Err() == nil {
		began := time.Now()
		err := w.watch(ctx)
		if ctx.Err() != nil {
			return
		}
		// a watch that held for a while starts the backoff over
		if time.Since(began) > etcdMaxRetryInterval {
			delay = etcdRetryInterval
		}
		etcdFeedRestarts.Add(ctx, 1)
		logger.WarnCtx(ctx).Str("error", fmt.Sprint(err)).Dur("retry_in", delay).Msg("etcd change feed ended; restarting it")
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		delay = min(delay*2, etcdMaxRetryInterval)
	}
}

// head returns the cluster's current revision.
func (w *etcdWatcher) head(ctx context.Context) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, etcdTimeout)
	defer cancel()
	resp, err := w.s.client.Get(ctx, w.s.prefix+"/", clientv3.WithCountOnly())
	if err != nil {
		return 0, err
	}
	return uint64(resp.Header.Revision), nil
}

// watch runs one etcd watch from the revision after w.last, reporting its
// changes, until etcd ends it or ctx is done.
func (w *etcdWatcher) watch(ctx context.Context) error {
	if w.lost {
		head, err := w.head(ctx)
		if err != nil {
			return err
		}
		if err := w.send(ctx, Change{Rev: head, Key: "/"}); err != nil {
			return err
		}
		w.last, w.lost = head, false
		w.confirmed.Store(head)
	}

	// a member that loses the cluster's leader ends the watch, rather than
	// holding it silently while it misses changes
	wctx, cancel := context.WithCancel(clientv3.WithRequireLeader(ctx))
	probed := make(chan struct{})
	defer func() {
		cancel()
		<-probed
	}()
	changes := w.s.client.Watch(wctx, w.s.prefix+"/", clientv3.WithPrefix(), clientv3.WithRev(int64(w.last)+1))
	go func() {
		defer close(probed)
		w.probe(wctx, cancel)
	}()
	for {
		resp, ok := <-changes
		if !ok {
			if err := ctx.Err(); err != nil {
				return err
			}
			if w.rewound.Swap(false) {
				// etcd would hold a watch from a revision it has not
				// reached, skipping the changes below it
				w.lost = true
				return errors.New("the cluster's revision went back; it was restored from an older backup")
			}
			return errors.New("the watch closed")
		}
		if err := resp.Err(); err != nil {
			if errors.Is(err, rpctypes.ErrCompacted) || errors.Is(err, rpctypes.ErrFutureRev) {
				w.lost = true
			}
			return err
		}
		for _, ev := range resp.Events {
			rev := uint64(ev.Kv.ModRevision)
			if err := w.send(ctx, Change{Rev: rev, Key: strings.TrimPrefix(string(ev.Kv.Key), w.s.prefix)}); err != nil {
				return err
			}
			w.last = rev
		}
		if resp.IsProgressNotify() {
			// every change up to the revision etcd was at has been delivered
			w.last = max(w.last, uint64(resp.Header.Revision))
			w.confirmed.Store(w.last)
			w.caughtUp.Store(time.Now().UnixNano())
		} else if w.last > w.confirmed.Load() {
			w.confirmed.Store(w.last)
		}
	}
}

// probe checks, every etcdProgressInterval until ctx is done, that the watch
// is current: that the cluster answers, and that it has committed nothing
// past the revision the watch has confirmed. When it has, probe asks etcd for
// a progress notification, which watch receives once it has every change.
// (etcd answers that request only once its revision has reached the watch's
// next one, so on a quiet cluster the first check is what finds the watch
// current.) When the cluster's revision is below the confirmed one, probe
// sets rewound and ends the watch with stop.
func (w *etcdWatcher) probe(ctx context.Context, stop context.CancelFunc) {
	tick := time.NewTicker(etcdProgressInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		// read before head: confirmed only grows, and a read of the head
		// issued after etcd confirmed a revision cannot return a lower one
		// unless the cluster went back
		confirmed := w.confirmed.Load()
		head, err := w.head(ctx)
		if err != nil {
			continue
		}
		if head < confirmed {
			w.rewound.Store(true)
			stop()
			return
		}
		if head == confirmed {
			w.caughtUp.Store(time.Now().UnixNano())
			continue
		}
		// the same metadata (ctx's) selects this watch's stream
		rctx, cancel := context.WithTimeout(ctx, etcdTimeout)
		_ = w.s.client.RequestProgress(rctx)
		cancel()
	}
}

func (w *etcdWatcher) send(ctx context.Context, c Change) error {
	select {
	case w.out <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// feedAge returns how long ago the running Watch furthest behind was last
// found current; ok is false when no Watch is running.
func (s *EtcdStorage) feedAge() (age time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for w := range s.watchers {
		if a := now.Sub(time.Unix(0, w.caughtUp.Load())); !ok || a > age {
			age, ok = a, true
		}
	}
	return age, ok
}

// Health implements HealthReporter. It fails while a Watch has gone longer
// than etcdFeedStaleAfter without being found current: the server may be
// missing other servers' changes, and its streams with them.
func (s *EtcdStorage) Health() error {
	if age, ok := s.feedAge(); ok && age > etcdFeedStaleAfter {
		return fmt.Errorf("the etcd change feed has not been current for %v", age.Truncate(time.Second))
	}
	return nil
}

// Close stops every Watch and closes the connection to the cluster.
func (s *EtcdStorage) Close(ctx context.Context) error {
	var err error
	s.closeOnce.Do(func() {
		s.cancel()
		s.wg.Wait()
		if s.metrics != nil {
			_ = s.metrics.Unregister()
		}
		err = s.client.Close()
		if errors.Is(err, context.Canceled) {
			err = nil
		}
	})
	return err
}
