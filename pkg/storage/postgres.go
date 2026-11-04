package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
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
}

var (
	_ Transactional = (*PostgresStorage)(nil)
	_ Watchable     = (*PostgresStorage)(nil)
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
)

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
INSERT INTO realm_revision (id, rev) VALUES (true, 0) ON CONFLICT DO NOTHING;
`

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
	return &PostgresStorage{
		pool:   pool,
		tracer: otel.Tracer("github.com/steviebps/realm"),
		ctx:    sctx,
		cancel: scancel,
	}, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)", lockClass, migrationLock); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
}

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
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
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
	})
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
// it cannot tell exactly what changed while it was disconnected.
func (p *PostgresStorage) Watch(ctx context.Context) <-chan Change {
	out := make(chan Change, 64)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)

	w := &watcher{p: p, out: out}
	// start from the current revision; on failure, the first successful
	// check reports that anything may have changed
	if err := p.pool.QueryRow(ctx, "SELECT rev FROM realm_revision").Scan(&w.last); err != nil {
		w.lost = true
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(out)
		defer stop()
		defer cancel()
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

var feedReconnects, _ = otel.Meter("github.com/steviebps/realm").Int64Counter(
	"realm.postgres.feed.reconnects",
	metric.WithDescription("Times the Postgres change feed lost its LISTEN connection."),
	metric.WithUnit("{reconnect}"))

// listen holds one LISTEN connection, catching up whenever a notification
// arrives or the poll interval passes, until the connection fails.
func (w *watcher) listen(ctx context.Context) error {
	pooled, err := w.p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// the connection carries a LISTEN, so it never returns to the pool
	conn := pooled.Hijack()
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
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
	// one statement, so the head and the changes come from one snapshot
	rows, err := conn.Query(ctx, `SELECT h.rev, c.rev, c.key FROM realm_revision h
		LEFT JOIN LATERAL (SELECT rev, key FROM realm_changes WHERE rev > $1 ORDER BY rev, key LIMIT $2) c ON true`,
		w.last, maxCatchUp+1)
	if err != nil {
		return err
	}
	var head uint64
	var changes []Change
	var rev *uint64
	var k *string
	if _, err := pgx.ForEachRow(rows, []any{&head, &rev, &k}, func() error {
		if rev != nil {
			changes = append(changes, Change{Rev: *rev, Key: *k})
		}
		return nil
	}); err != nil {
		return err
	}

	if head > w.last {
		// Revisions are contiguous, so a missing first revision means its
		// records were pruned; too many to replay are summarized too.
		if w.lost || len(changes) == 0 || changes[0].Rev != w.last+1 || len(changes) > maxCatchUp {
			changes = []Change{{Rev: head, Key: "/"}}
		}
		for _, c := range changes {
			select {
			case w.out <- c:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		w.last = head
	}
	w.lost = false

	if time.Since(w.lastPrune) > pruneInterval && head > changeRetention {
		w.lastPrune = time.Now()
		if _, err := conn.Exec(ctx, "DELETE FROM realm_changes WHERE rev <= $1", head-changeRetention); err != nil {
			logging.Ctx(ctx).WarnCtx(ctx).Str("error", err.Error()).Msg("could not prune postgres change records")
		}
	}
	return nil
}

// Close stops every Watch and closes the connection pool.
func (p *PostgresStorage) Close(ctx context.Context) error {
	p.cancel()
	p.wg.Wait()
	p.pool.Close()
	return nil
}
