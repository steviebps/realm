package storage

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// PostgresStorage stores entries in PostgreSQL. It is Transactional and
// Watchable, so any number of servers can share one database: writes are
// version-checked in the database, and every server learns of every change
// through LISTEN/NOTIFY.
//
// Every write gets the next value of one database-wide revision counter.
// Taking the counter locks it until the write commits, so revisions commit in
// order and a reader that has seen revision N has seen everything before it.
type PostgresStorage struct {
	pool   *pgxpool.Pool
	tracer trace.Tracer

	// ctx is canceled by Close, stopping every Watch.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// mu guards watchers, the running Watches, which Health and the feed age
	// metric inspect.
	mu       sync.Mutex
	watchers map[*watcher]struct{}
	// metrics is the registration of the feed age callback, if any.
	metrics metric.Registration
}

var (
	_ Transactional  = (*PostgresStorage)(nil)
	_ Watchable      = (*PostgresStorage)(nil)
	_ HealthReporter = (*PostgresStorage)(nil)
)

const (
	// notifyChannel is the LISTEN/NOTIFY channel that announces commits.
	notifyChannel = "realm_changes"
	// lockClass namespaces realm's advisory locks: the first key of the
	// two-key pg_advisory_xact_lock form.
	lockClass = 0x7265616c // "real"
	// migrationLock serializes schema creation when servers start together.
	migrationLock = 0
	// changeRetention is how many revisions of change records are kept for
	// servers catching up after a lost connection.
	changeRetention = 10000
	// maxCatchUp is the most changes a Watch replays one by one; beyond it,
	// it reports a change to "/" instead.
	maxCatchUp = 1000
)

var (
	// watchPollInterval is how often a Watch checks for changes when no
	// notification arrives, in case one was lost.
	watchPollInterval = 5 * time.Second
	// watchRetryInterval is how long a Watch first waits before reconnecting;
	// the wait doubles up to maxWatchRetryInterval.
	watchRetryInterval    = time.Second
	maxWatchRetryInterval = 30 * time.Second
	// pruneInterval is how often a Watch deletes old change records.
	pruneInterval = 10 * time.Minute
	// feedTimeout bounds every round trip on a Watch's connection (LISTEN, a
	// catch-up, a prune) and every attempt to connect it. A connection that
	// does not answer in time is treated as dead and replaced, so a database
	// or network that stops responding without closing the connection cannot
	// stall the feed.
	feedTimeout = 10 * time.Second
	// feedStaleAfter is how long a Watch may go without catching up with the
	// database before Health reports it.
	feedStaleAfter = 30 * time.Second
)

// keepAlive probes idle connections, so the operating system notices a
// database host that has gone away within about half a minute.
var keepAlive = net.KeepAliveConfig{Enable: true, Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3}

// sessionDefaults are settings realm gives its sessions when nothing else has
// set them (the server's configuration, the database or role, or the
// connection string). They bound how long a session that died in the middle
// of a transaction, or a statement waiting on a lock, can hold the locks that
// every write needs, such as the revision counter.
var sessionDefaults = [][2]string{
	{"idle_in_transaction_session_timeout", "30s"},
	{"lock_timeout", "15s"},
	{"statement_timeout", "30s"},
}

const schema = `
CREATE TABLE IF NOT EXISTS realm_entries (
	key   text COLLATE "C" PRIMARY KEY,
	value bytea NOT NULL,
	rev   bigint NOT NULL
);
CREATE TABLE IF NOT EXISTS realm_changes (
	rev bigint NOT NULL,
	key text COLLATE "C" NOT NULL,
	PRIMARY KEY (rev, key)
);
CREATE TABLE IF NOT EXISTS realm_revision (
	id  boolean PRIMARY KEY DEFAULT true CHECK (id),
	rev bigint NOT NULL
);
` + seedRevision

// seedRevision creates the revision counter's row.
const seedRevision = `INSERT INTO realm_revision (id, rev) VALUES (true, 0) ON CONFLICT DO NOTHING;`

// NewPostgresStorage creates PostgreSQL-backed storage, creating its tables
// if needed. Options:
//
//	url        connection string (postgres://user@host:5432/db?sslmode=…);
//	           when empty, the standard PG* environment variables are used
//	max_conns  optional maximum size of the connection pool
//
// Prefer PGPASSWORD or a .pgpass file to putting a password in the url.
// Watching needs one connection that is not behind a transaction-pooling
// proxy (such as PgBouncer in transaction mode), because LISTEN holds a
// session.
//
// Creating the tables needs the CREATE privilege on the schema; once they
// exist, a role with SELECT, INSERT, UPDATE, and DELETE on them suffices.
// Sessions get the timeouts in sessionDefaults unless the server, database,
// role, or url sets them.
func NewPostgresStorage(conf map[string]string) (Storage, error) {
	cfg, err := pgxpool.ParseConfig(conf["url"])
	if err != nil {
		return nil, fmt.Errorf("invalid postgres 'url': %w", err)
	}
	if v := conf["max_conns"]; v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("'max_conns' must be a positive integer")
		}
		cfg.MaxConns = int32(n)
	}
	// keep connect_timeout, which ParseConfig gives the dialer it replaces
	dialer := &net.Dialer{Timeout: cfg.ConnConfig.ConnectTimeout, KeepAliveConfig: keepAlive}
	cfg.ConnConfig.DialFunc = dialer.DialContext
	cfg.AfterConnect = applySessionDefaults

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("could not connect to postgres: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("could not create postgres tables: %w", err)
	}

	sctx, scancel := context.WithCancel(context.Background())
	p := &PostgresStorage{
		pool:     pool,
		tracer:   otel.Tracer("github.com/steviebps/realm"),
		ctx:      sctx,
		cancel:   scancel,
		watchers: map[*watcher]struct{}{},
	}
	// without a working meter, the feed simply goes unmeasured
	p.metrics, _ = otel.Meter("github.com/steviebps/realm").RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if age, ok := p.feedAge(); ok {
			o.ObserveFloat64(feedAgeGauge, age.Seconds())
		}
		return nil
	}, feedAgeGauge)
	return p, nil
}

// applySessionDefaults gives a new connection each of sessionDefaults that
// nothing else has set. It is best effort: a server that refuses it (one
// that is only compatible with Postgres, say) runs without the timeouts.
func applySessionDefaults(ctx context.Context, conn *pgx.Conn) error {
	names := make([]string, len(sessionDefaults))
	values := make([]string, len(sessionDefaults))
	for i, d := range sessionDefaults {
		names[i], values[i] = d[0], d[1]
	}
	_, _ = conn.Exec(ctx, `SELECT set_config(s.name, d.value, false)
		FROM pg_settings s JOIN unnest($1::text[], $2::text[]) AS d(name, value) ON s.name = d.name
		WHERE s.source = 'default'`, names, values)
	return nil
}

// migrate creates realm's tables. When they already exist, as on every start
// but the first, it changes no schema, so a role without the CREATE privilege
// can run realm.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	var exist bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('realm_entries') IS NOT NULL
		AND to_regclass('realm_changes') IS NOT NULL
		AND to_regclass('realm_revision') IS NOT NULL`).Scan(&exist); err != nil {
		return err
	}
	if exist {
		var seeded bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM realm_revision)").Scan(&seeded); err != nil || seeded {
			return err
		}
		_, err := pool.Exec(ctx, seedRevision)
		return err
	}
	return pgx.BeginTxFunc(ctx, pool, readCommitted, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)", lockClass, migrationLock); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
}

// readCommitted is the isolation of every transaction: commit relies on its
// re-reading the revision counter after waiting for the counter's lock, which
// REPEATABLE READ and SERIALIZABLE (should the database default to them)
// refuse with serialization failures.
var readCommitted = pgx.TxOptions{IsoLevel: pgx.ReadCommitted}

// key normalizes a storage key: every key ends in a slash, like the paths the
// server uses.
func key(k string) string {
	return utils.EnsureTrailingSlash(k)
}

func (p *PostgresStorage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := p.tracer.Start(ctx, "PostgresStorage Get", trace.WithAttributes(attribute.String("realm.postgres.logicalPath", logicalPath)))
	defer span.End()

	logging.Ctx(ctx).DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return nil, err
	}

	e := &StorageEntry{Key: logicalPath}
	err := p.pool.QueryRow(ctx, "SELECT value, rev FROM realm_entries WHERE key = $1", key(logicalPath)).Scan(&e.Value, &e.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &NotFoundError{Key: logicalPath}
	}
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	return e, nil
}

func (p *PostgresStorage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := p.tracer.Start(ctx, "PostgresStorage Put", trace.WithAttributes(attribute.String("realm.postgres.entry.key", e.Key)))
	defer span.End()

	logging.Ctx(ctx).DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	if err := ValidatePath(e.Key); err != nil {
		recordError(span, err)
		return err
	}
	value := e.Value
	if value == nil {
		// a nil Value deletes in a transaction; Put always stores
		value = []byte{}
	}
	err := p.commit(ctx, nil, []Op{{Key: e.Key, Value: value}}, false)
	if err != nil {
		recordError(span, err)
	}
	return err
}

func (p *PostgresStorage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := p.tracer.Start(ctx, "PostgresStorage Delete", trace.WithAttributes(attribute.String("realm.postgres.logicalPath", logicalPath)))
	defer span.End()

	logging.Ctx(ctx).DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := ValidatePath(logicalPath); err != nil {
		recordError(span, err)
		return err
	}
	err := p.commit(ctx, nil, []Op{{Key: logicalPath}}, true)
	if err != nil {
		recordError(span, err)
	}
	return err
}

// List returns the names directly beneath prefix: "." when an entry exists at
// prefix itself, and "<child>/" for each child that has entries.
func (p *PostgresStorage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := p.tracer.Start(ctx, "PostgresStorage List", trace.WithAttributes(attribute.String("realm.postgres.prefix", prefix)))
	defer span.End()

	logging.Ctx(ctx).DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := ValidatePath(prefix); err != nil {
		recordError(span, err)
		return nil, err
	}

	lower := key(prefix)
	// keys beginning with lower sort below lower with its final "/"
	// replaced by the next byte, "0" (the column uses byte order)
	upper := strings.TrimSuffix(lower, "/") + "0"
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT CASE WHEN key = $1 THEN '.'
		ELSE split_part(substr(key, length($1) + 1), '/', 1) || '/' END AS name
		FROM realm_entries WHERE key >= $1 AND key < $2 ORDER BY name`, lower, upper)
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		recordError(span, err)
		return nil, err
	}
	if names == nil {
		names = []string{}
	}
	return names, nil
}

// Txn applies ops atomically if every check holds.
func (p *PostgresStorage) Txn(ctx context.Context, checks []Check, ops []Op) error {
	ctx, span := p.tracer.Start(ctx, "PostgresStorage Txn", trace.WithAttributes(attribute.Int("realm.postgres.checks", len(checks)), attribute.Int("realm.postgres.ops", len(ops))))
	defer span.End()

	for _, k := range append(checkKeys(checks), opKeys(ops)...) {
		if err := ValidatePath(k); err != nil {
			recordError(span, err)
			return err
		}
	}
	err := p.commit(ctx, checks, ops, false)
	if err != nil && !errors.Is(err, ErrConflict) {
		recordError(span, err)
	}
	return err
}

// commit runs one write transaction in two round trips (plus BEGIN and
// COMMIT): one that locks the keys and reads the checked versions, and one
// that writes. When mustExist is set, deleting a key that does not exist
// returns a NotFoundError (Storage.Delete's contract).
func (p *PostgresStorage) commit(ctx context.Context, checks []Check, ops []Op, mustExist bool) error {
	return conflictError(pgx.BeginTxFunc(ctx, p.pool, readCommitted, func(tx pgx.Tx) error {
		// Lock every key involved, in a fixed order so transactions cannot
		// deadlock. Advisory locks also cover keys that do not exist yet,
		// which row locks cannot. A batch runs its statements in order.
		batch := &pgx.Batch{}
		for _, id := range lockOrder(normalize(append(checkKeys(checks), opKeys(ops)...))) {
			batch.Queue("SELECT pg_advisory_xact_lock($1, $2)", lockClass, int32(id))
		}
		current := map[string]uint64{}
		if len(checks) > 0 {
			batch.Queue("SELECT key, rev FROM realm_entries WHERE key = ANY($1)", normalize(checkKeys(checks))).Query(func(rows pgx.Rows) error {
				var k string
				var v uint64
				_, err := pgx.ForEachRow(rows, []any{&k, &v}, func() error {
					current[k] = v
					return nil
				})
				return err
			})
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		for _, c := range checks {
			if current[key(c.Key)] != c.Version {
				return ErrConflict
			}
		}
		if len(ops) == 0 {
			return nil
		}

		// Taking the next revision locks the counter until commit, so
		// revisions commit in order. Later statements read it back.
		const rev = "(SELECT rev FROM realm_revision)"
		batch = &pgx.Batch{}
		batch.Queue("UPDATE realm_revision SET rev = rev + 1")
		for _, op := range ops {
			if op.Value == nil {
				q := batch.Queue("DELETE FROM realm_entries WHERE key = $1", key(op.Key))
				if mustExist {
					q.Exec(func(tag pgconn.CommandTag) error {
						if tag.RowsAffected() == 0 {
							return &NotFoundError{Key: op.Key}
						}
						return nil
					})
				}
				continue
			}
			batch.Queue(`INSERT INTO realm_entries (key, value, rev) VALUES ($1, $2, `+rev+`)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value, rev = excluded.rev`, key(op.Key), []byte(op.Value))
		}
		batch.Queue("INSERT INTO realm_changes (rev, key) SELECT "+rev+", k FROM unnest($1::text[]) AS k", slices.Compact(slices.Sorted(slices.Values(normalize(opKeys(ops))))))
		batch.Queue("SELECT pg_notify($1, rev::text) FROM realm_revision", notifyChannel)
		return tx.SendBatch(ctx, batch).Close()
	}))
}

// conflictError maps the errors with which Postgres aborts one of two
// transactions that got in each other's way (a serialization failure or a
// deadlock) to ErrConflict, so that Update retries them.
func conflictError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && (pe.Code == "40001" || pe.Code == "40P01") {
		return fmt.Errorf("%w: %w", ErrConflict, err)
	}
	return err
}

func checkKeys(checks []Check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Key
	}
	return out
}

func opKeys(ops []Op) []string {
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = op.Key
	}
	return out
}

func normalize(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = key(k)
	}
	return out
}

// Watch reports every change committed after it is called, by any server
// sharing the database. Notifications wake it immediately; it also checks
// every few seconds and after reconnecting, and reports a change to "/" when
// it cannot tell exactly what changed while it was disconnected, or when the
// database went back to an earlier revision (restored from a backup, or
// failed over to a replica that missed commits).
func (p *PostgresStorage) Watch(ctx context.Context) <-chan Change {
	out := make(chan Change, 64)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)

	w := &watcher{p: p, out: out}
	// a new feed has feedStaleAfter to catch up before Health reports it
	w.caughtUp.Store(time.Now().UnixNano())
	// start from the current revision; on failure, the first successful
	// check reports that anything may have changed
	qctx, qcancel := context.WithTimeout(ctx, feedTimeout)
	if err := p.pool.QueryRow(qctx, "SELECT rev FROM realm_revision").Scan(&w.last); err != nil {
		w.lost = true
	}
	qcancel()

	p.mu.Lock()
	p.watchers[w] = struct{}{}
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(out)
		defer stop()
		defer cancel()
		defer func() {
			p.mu.Lock()
			delete(p.watchers, w)
			p.mu.Unlock()
		}()
		w.run(ctx)
	}()
	return out
}

type watcher struct {
	p    *PostgresStorage
	out  chan<- Change
	last uint64
	// lost is set when changes may have been missed.
	lost      bool
	lastPrune time.Time
	// caughtUp is when the watcher last caught up with the database (or
	// started), in Unix nanoseconds.
	caughtUp atomic.Int64
}

func (w *watcher) run(ctx context.Context) {
	logger := logging.Ctx(ctx)
	delay := watchRetryInterval
	for ctx.Err() == nil {
		began := time.Now()
		err := w.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		// a connection that held for a while starts the backoff over
		if time.Since(began) > maxWatchRetryInterval {
			delay = watchRetryInterval
		}
		feedReconnects.Add(ctx, 1)
		logger.WarnCtx(ctx).Str("error", fmt.Sprint(err)).Dur("retry_in", delay).Msg("postgres change feed disconnected; reconnecting")
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		delay = min(delay*2, maxWatchRetryInterval)
	}
}

var (
	feedReconnects, _ = otel.Meter("github.com/steviebps/realm").Int64Counter(
		"realm.postgres.feed.reconnects",
		metric.WithDescription("Times the Postgres change feed lost its LISTEN connection."),
		metric.WithUnit("{reconnect}"))
	feedAgeGauge, _ = otel.Meter("github.com/steviebps/realm").Float64ObservableGauge(
		"realm.postgres.feed.age",
		metric.WithDescription("Seconds since the Postgres change feed last caught up with the database; it grows while the feed cannot reach the database."),
		metric.WithUnit("s"))
)

// listen holds one LISTEN connection, catching up whenever a notification
// arrives or the poll interval passes, until the connection fails or a round
// trip on it takes longer than feedTimeout.
func (w *watcher) listen(ctx context.Context) error {
	actx, cancel := context.WithTimeout(ctx, feedTimeout)
	pooled, err := w.p.pool.Acquire(actx)
	cancel()
	if err != nil {
		return err
	}
	// the connection carries a LISTEN, so it never returns to the pool
	conn := pooled.Hijack()
	defer func() {
		// bounded, in case the connection stopped answering
		cctx, cancel := context.WithTimeout(context.Background(), feedTimeout)
		defer cancel()
		_ = conn.Close(cctx)
	}()

	lctx, cancel := context.WithTimeout(ctx, feedTimeout)
	_, err = conn.Exec(lctx, "LISTEN "+notifyChannel)
	cancel()
	if err != nil {
		return err
	}
	for {
		// catch up first: notifications sent before LISTEN took effect, or
		// while disconnected, were missed
		if err := w.catchUp(ctx, conn); err != nil {
			return err
		}
		for {
			wctx, cancel := context.WithTimeout(ctx, watchPollInterval)
			n, err := conn.WaitForNotification(wctx)
			cancel()
			// a timeout leaves the connection usable: poll, then wait again
			if err != nil && (ctx.Err() != nil || !pgconn.Timeout(err)) {
				return err
			}
			// a revision an earlier catch-up already reported needs none
			if err != nil || !w.seen(n.Payload) {
				break
			}
		}
	}
}

// seen reports whether a notification's revision was already reported.
func (w *watcher) seen(payload string) bool {
	rev, err := strconv.ParseUint(payload, 10, 64)
	return err == nil && rev <= w.last
}

// catchUp reports the changes after w.last.
func (w *watcher) catchUp(ctx context.Context, conn *pgx.Conn) error {
	head, changes, err := w.readChanges(ctx, conn)
	if err != nil {
		return err
	}

	switch {
	case head < w.last:
		// The database went back to an earlier revision: it was restored, or
		// failed over to a replica that missed commits. Its revisions no longer
		// match the ones reported, so anything may have changed.
		changes = []Change{{Rev: head, Key: "/"}}
	case head > w.last:
		// Revisions are contiguous, so a missing first revision means its
		// records were pruned; too many to replay are summarized too.
		if w.lost || len(changes) == 0 || changes[0].Rev != w.last+1 || len(changes) > maxCatchUp {
			changes = []Change{{Rev: head, Key: "/"}}
		}
	default:
		changes = nil
	}
	for _, c := range changes {
		select {
		case w.out <- c:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w.last = head
	w.lost = false
	w.caughtUp.Store(time.Now().UnixNano())

	if time.Since(w.lastPrune) > pruneInterval && head > changeRetention {
		w.lastPrune = time.Now()
		pctx, cancel := context.WithTimeout(ctx, feedTimeout)
		_, err := conn.Exec(pctx, "DELETE FROM realm_changes WHERE rev <= $1", head-changeRetention)
		cancel()
		if err != nil {
			// a connection that stopped answering is replaced; anything else
			// is retried at the next prune
			if conn.IsClosed() || pgconn.Timeout(err) {
				return err
			}
			logging.Ctx(ctx).WarnCtx(ctx).Str("error", err.Error()).Msg("could not prune postgres change records")
		}
	}
	return nil
}

// readChanges returns the head revision and up to maxCatchUp+1 changes after
// w.last, from one snapshot, within feedTimeout.
func (w *watcher) readChanges(ctx context.Context, conn *pgx.Conn) (head uint64, changes []Change, err error) {
	ctx, cancel := context.WithTimeout(ctx, feedTimeout)
	defer cancel()
	// one statement, so the head and the changes come from one snapshot
	rows, err := conn.Query(ctx, `SELECT h.rev, c.rev, c.key FROM realm_revision h
		LEFT JOIN LATERAL (SELECT rev, key FROM realm_changes WHERE rev > $1 ORDER BY rev, key LIMIT $2) c ON true`,
		w.last, maxCatchUp+1)
	if err != nil {
		return 0, nil, err
	}
	var rev *uint64
	var k *string
	_, err = pgx.ForEachRow(rows, []any{&head, &rev, &k}, func() error {
		if rev != nil {
			changes = append(changes, Change{Rev: *rev, Key: *k})
		}
		return nil
	})
	return head, changes, err
}

// feedAge returns how long ago the running Watch furthest behind last caught
// up with the database; ok is false when no Watch is running.
func (p *PostgresStorage) feedAge() (age time.Duration, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for w := range p.watchers {
		if a := now.Sub(time.Unix(0, w.caughtUp.Load())); !ok || a > age {
			age, ok = a, true
		}
	}
	return age, ok
}

// Health implements HealthReporter. It fails while a Watch has gone longer
// than feedStaleAfter without catching up with the database: the server may
// be missing other servers' changes, and its streams with them.
func (p *PostgresStorage) Health() error {
	if age, ok := p.feedAge(); ok && age > feedStaleAfter {
		return fmt.Errorf("the postgres change feed has not caught up with the database for %v", age.Truncate(time.Second))
	}
	return nil
}

// Close stops every Watch and closes the connection pool.
func (p *PostgresStorage) Close(ctx context.Context) error {
	p.cancel()
	p.wg.Wait()
	if p.metrics != nil {
		_ = p.metrics.Unregister()
	}
	p.pool.Close()
	return nil
}
