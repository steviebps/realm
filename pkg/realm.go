package realm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/helper/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

type Realm struct {
	attributes map[string]string
	path       string
	// lifecycle guards started, ctx, and cancel.
	lifecycle sync.Mutex
	started   bool
	// ctx is the context of the background refresh; Stop cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// mu guards the snapshot (root) and the fields that describe it.
	mu   sync.RWMutex
	root *ChamberEntry
	// version counts the snapshot's replacements, so that a chamber fetched
	// while a newer one arrived (from the stream, say) never replaces it.
	version uint64
	// refreshed is when the snapshot was last replaced.
	refreshed time.Time
	// unsupported lists the snapshot's flags that this SDK cannot fully
	// evaluate, as last logged.
	unsupported []string

	client          *client.HttpClient
	pollingInterval time.Duration
	streaming       bool
	logger          *logging.TracedLogger
	tracer          trace.Tracer

	// loadFailures, streamFailures, streamDown, and badEvents keep a failure
	// that repeats from flooding the log.
	loadFailures, streamFailures, streamDown, badEvents failureLog
}

type RealmConfig struct {
	client             *client.HttpClient
	path               string
	applicationVersion string
	attributes         map[string]string
	// pollingInterval is how often realm will refetch the chamber from the realm server
	pollingInterval time.Duration
	// streaming makes realm keep its chamber current via a server-sent-events
	// stream (with polling as an automatic fallback) instead of only polling
	streaming bool
	logger    *logging.TracedLogger
}

const (
	// DefaultPollingInterval is used as the default polling interval for realm
	DefaultPollingInterval time.Duration = 15 * time.Minute

	// MinPollingInterval is the shortest polling interval realm uses; a
	// shorter one is raised to it.
	MinPollingInterval time.Duration = time.Second

	// DefaultStartTimeout bounds a synchronous chamber load by a repeated
	// Start.
	DefaultStartTimeout = 15 * time.Second
)

type contextKey struct {
	name string
}

var (
	// RequestContextKey is the context key to use with a WithValue function to associate a root chamber value with a context
	// such that rule retrievals will be consistent throughout the client's request
	RequestContextKey = &contextKey{"realm"}

	// EvaluationContextKey is the context key used to associate an
	// EvaluationContext with a context so that rule retrievals can apply
	// per-subject targeting (such as percentage rollouts) consistently
	// throughout the client's request
	EvaluationContextKey = &contextKey{"realm-evaluation"}
)

type RealmOption interface {
	apply(RealmConfig) RealmConfig
}

type realmOptionFunc func(RealmConfig) RealmConfig

func (fn realmOptionFunc) apply(cfg RealmConfig) RealmConfig {
	return fn(cfg)
}

func WithHttpClient(c *client.HttpClient) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		rc.client = c
		return rc
	})
}

func WithPath(path string) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		rc.path = path
		return rc
	})
}

// WithPollingInterval sets how often realm refetches the chamber: always when
// polling (the default), and while the stream is down when streaming. Zero or
// a negative interval means DefaultPollingInterval; one shorter than
// MinPollingInterval is raised to it.
func WithPollingInterval(d time.Duration) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		rc.pollingInterval = d
		return rc
	})
}

// WithVersion sets the application version, exposed to targeting clauses as
// the "version" attribute. It is shorthand for a "version" entry in
// WithAttributes and takes precedence over it.
func WithVersion(version string) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		rc.applicationVersion = version
		return rc
	})
}

// WithAttributes sets process-wide targeting attributes, such as "service",
// "environment", "region", or "buildSha". They apply to every evaluation and
// are overridden by attributes of the same name on a request's
// EvaluationContext. Repeated calls merge.
func WithAttributes(attributes map[string]string) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		merged := maps.Clone(rc.attributes)
		if merged == nil {
			merged = make(map[string]string, len(attributes))
		}
		maps.Copy(merged, attributes)
		rc.attributes = merged
		return rc
	})
}

// WithStreaming enables real-time updates over a server-sent-events stream.
// When enabled, realm applies chamber changes as the server pushes them,
// reconnecting with backoff. While the stream is down (it has delivered
// nothing for a few attempts or about a minute), realm also polls at the
// polling interval; against a server that does not stream, it polls and
// tries the stream again every few minutes. Defaults to false (polling only).
func WithStreaming(streaming bool) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		rc.streaming = streaming
		return rc
	})
}

// WithLogger sets the logger realm reports refresh failures and reconnects
// to. By default realm writes its lines at Info and above to standard error,
// as JSON (human-readable on a terminal; REALM_LOG_FORMAT=json or console
// chooses), without changing zerolog's global settings; pass zerolog.Nop() to
// silence it. Failures are logged when they start, at most every few minutes
// while they last, and when they end.
func WithLogger(logger zerolog.Logger) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		rc.logger = logging.FromZerolog(logger)
		return rc
	})
}

// NewRealm returns a new Realm struct that carries out all of the core features
func NewRealm(options ...RealmOption) (*Realm, error) {
	cfg := RealmConfig{}

	for _, opt := range options {
		cfg = opt.apply(cfg)
	}

	if cfg.client == nil {
		return nil, errors.New("client option must not be nil")
	}

	if cfg.path == "" {
		return nil, errors.New("path must not be empty")
	}

	switch {
	case cfg.pollingInterval <= 0:
		cfg.pollingInterval = DefaultPollingInterval
	case cfg.pollingInterval < MinPollingInterval:
		cfg.pollingInterval = MinPollingInterval
	}

	attributes := maps.Clone(cfg.attributes)
	if cfg.applicationVersion != "" {
		if attributes == nil {
			attributes = make(map[string]string, 1)
		}
		// stored as given, so in/notIn/startsWith clauses see the same value
		// as server-side evaluation; semver clauses use a canonical form
		// cached by the ChamberEntry
		attributes[VersionAttribute] = cfg.applicationVersion
	}

	logger := cfg.logger
	if logger == nil {
		logger = defaultLogger()
	}

	return &Realm{
		tracer:          otel.Tracer("github.com/steviebps/realm"),
		logger:          logger,
		client:          cfg.client,
		path:            cfg.path,
		attributes:      attributes,
		pollingInterval: cfg.pollingInterval,
		streaming:       cfg.streaming,
	}, nil
}

// defaultLogger writes JSON lines to standard error, or human-readable ones
// when it is a terminal (REALM_LOG_FORMAT=json or console chooses).
func defaultLogger() *logging.TracedLogger {
	var w io.Writer = os.Stderr
	console := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
	switch os.Getenv("REALM_LOG_FORMAT") {
	case "json":
	case "console":
		w = console
	default:
		if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			w = console
		}
	}
	return newDefaultLogger(w)
}

// newDefaultLogger returns realm's default logger writing to w: Info and
// above, timestamped, and tagged as realm's, with no caller paths.
func newDefaultLogger(w io.Writer) *logging.TracedLogger {
	return logging.FromZerolog(zerolog.New(w).Level(zerolog.InfoLevel).With().Timestamp().Str("component", "realm").Logger())
}

// errStopped is returned by Start on a stopped Realm that has no chamber.
var errStopped = errors.New("realm is stopped")

// Start loads the chamber, then keeps it current in the background (by
// polling, or streaming with WithStreaming) until Stop.
//
// If the first load fails, Start returns its error but keeps retrying in the
// background, with backoff, so a service can start while realm is
// unreachable: until a load succeeds, flag reads return their defaults along
// with ErrChamberEmpty. Stop cancels a first load still in flight. Calling
// Start again only retries the load synchronously (within
// DefaultStartTimeout), and never replaces a newer snapshot with what it
// fetched. A stopped Realm cannot be restarted.
func (rlm *Realm) Start() error {
	rlm.lifecycle.Lock()
	if rlm.started {
		ctx := rlm.ctx
		rlm.lifecycle.Unlock()
		if rlm.getChamber() != nil {
			return nil
		}
		if ctx.Err() != nil {
			return errStopped
		}
		ctx, cancel := context.WithTimeout(ctx, DefaultStartTimeout)
		defer cancel()
		return rlm.load(ctx)
	}
	rlm.started = true
	ctx, cancel := context.WithCancel(rlm.logger.WithContext(context.Background()))
	rlm.ctx, rlm.cancel = ctx, cancel
	rlm.wg.Add(1)
	rlm.lifecycle.Unlock()

	// The first load runs in the background goroutine, so Stop can cancel it.
	first := make(chan error, 1)
	go func() {
		defer rlm.wg.Done()
		err := rlm.load(ctx)
		first <- err
		if rlm.streaming {
			rlm.stream(ctx)
		} else {
			rlm.poll(ctx, err)
		}
		rlm.logger.InfoCtx(ctx).Msg("realm stopped")
	}()
	return <-first
}

// Stop stops realm, canceling any load in flight, and blocks until the
// background refresh goroutine has exited. It is safe to call more than once.
// The last snapshot stays available to flag reads.
func (rlm *Realm) Stop() {
	rlm.lifecycle.Lock()
	if rlm.cancel != nil {
		rlm.cancel()
	}
	rlm.lifecycle.Unlock()
	rlm.wg.Wait()
}

// snapshotVersion returns the version of the snapshot, for setChamberIf.
func (rlm *Realm) snapshotVersion() uint64 {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	return rlm.version
}

// setChamber replaces the snapshot with c, at the server's revision rev (0
// when unknown).
func (rlm *Realm) setChamber(c *Chamber, rev uint64) {
	rlm.replace(c, rev, false, 0)
}

// setChamberIf replaces the snapshot with c, at the server's revision rev,
// which was fetched while the snapshot was at version, unless the snapshot
// has changed since: a newer chamber (from the stream, say) arrived while c
// was in flight. It reports whether it replaced the snapshot.
func (rlm *Realm) setChamberIf(c *Chamber, rev uint64, version uint64) bool {
	return rlm.replace(c, rev, true, version)
}

func (rlm *Realm) replace(c *Chamber, rev uint64, conditional bool, version uint64) bool {
	entry := NewChamberEntry(c, rlm.attributes)
	entry.revision = rev
	unsupported := c.unsupportedFlags()
	rlm.mu.Lock()
	if conditional && rlm.version != version {
		rlm.mu.Unlock()
		return false
	}
	rlm.root = entry
	rlm.version++
	rlm.refreshed = time.Now()
	changed := !slices.Equal(unsupported, rlm.unsupported)
	rlm.unsupported = unsupported
	rlm.mu.Unlock()
	if changed && len(unsupported) > 0 {
		rlm.logger.Warn().Strs("flags", unsupported).Msg("these flags use flag types or targeting operators this version of the realm SDK does not support: a flag of an unknown type returns the caller's default, and a rule with an unknown operator never matches; upgrade the SDK")
	}
	return true
}

// refreshedAt returns when the snapshot was last replaced, or the zero time
// if it never was.
func (rlm *Realm) refreshedAt() time.Time {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	return rlm.refreshed
}

func (rlm *Realm) getChamber() *ChamberEntry {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	return rlm.root
}

func chamberFromContext(ctx context.Context) *ChamberEntry {
	c, ok := ctx.Value(RequestContextKey).(*ChamberEntry)
	if !ok {
		return nil
	}
	return c
}

func (rlm *Realm) chamberFromContext(ctx context.Context) *ChamberEntry {
	c := chamberFromContext(ctx)
	if c != nil {
		return c
	}
	return rlm.getChamber()
}

func (rlm *Realm) NewContext(ctx context.Context) context.Context {
	c := rlm.getChamber()
	ctx = context.WithValue(ctx, RequestContextKey, c)
	return ctx
}

// Revision returns the server's storage revision of the chamber snapshot
// pinned to ctx (see NewContext), or of the current snapshot when ctx has
// none, so a service can record which configuration a request ran under (on
// its spans or logs, say). It is the highest revision among the chamber and
// the parents its inherited view merges, so it changes whenever any of them
// does.
//
// It is 0 before a chamber has loaded, and when the server does not report
// revisions: only servers on transactional storage (such as postgres, etcd,
// or boltdb) do. Revisions identify a version of the chamber rather than
// order versions: deleting a parent chamber can lower it.
func (rlm *Realm) Revision(ctx context.Context) uint64 {
	c := rlm.chamberFromContext(ctx)
	if c == nil {
		return 0
	}
	return c.revision
}

// NewContextWithEvaluation pins the current chamber snapshot (like NewContext)
// and additionally associates the provided EvaluationContext with the context.
// Flag retrievals performed with the returned context apply targeting rules
// against the context's Key and Attributes (merged over WithAttributes).
func (rlm *Realm) NewContextWithEvaluation(ctx context.Context, ec EvaluationContext) context.Context {
	ctx = rlm.NewContext(ctx)
	return context.WithValue(ctx, EvaluationContextKey, ec)
}

// evaluationFromContext returns the EvaluationContext associated with ctx, or
// the zero EvaluationContext (no targeting) when none is present.
func evaluationFromContext(ctx context.Context) EvaluationContext {
	ec, ok := ctx.Value(EvaluationContextKey).(EvaluationContext)
	if !ok {
		return EvaluationContext{}
	}
	return ec
}

// Bool retrieves a bool by the key of the rule.
// Returns the default value if it does not exist and an error if the chamber is empty or could not be converted
func (rlm *Realm) Bool(ctx context.Context, ruleKey string, defaultValue bool) (bool, error) {
	c := rlm.chamberFromContext(ctx)
	if c == nil {
		return defaultValue, ErrChamberEmpty
	}
	return c.BoolValueFor(ruleKey, evaluationFromContext(ctx), defaultValue)
}

// String retrieves a string by the key of the rule.
// Returns the default value if it does not exist and an error if the chamber is empty or could not be converted
func (rlm *Realm) String(ctx context.Context, ruleKey string, defaultValue string) (string, error) {
	c := rlm.chamberFromContext(ctx)
	if c == nil {
		return defaultValue, ErrChamberEmpty
	}
	return c.StringValueFor(ruleKey, evaluationFromContext(ctx), defaultValue)
}

// Float64 retrieves a float64 by the key of the rule.
// Returns the default value if it does not exist and an error if the chamber is empty or could not be converted
func (rlm *Realm) Float64(ctx context.Context, ruleKey string, defaultValue float64) (float64, error) {
	c := rlm.chamberFromContext(ctx)
	if c == nil {
		return defaultValue, ErrChamberEmpty
	}
	return c.Float64ValueFor(ruleKey, evaluationFromContext(ctx), defaultValue)
}

// CustomValue retrieves an arbitrary value by the key of the rule
// and unmarshals the value into the custom value v
func (rlm *Realm) CustomValue(ctx context.Context, ruleKey string, v any) error {
	c := rlm.chamberFromContext(ctx)
	if c == nil {
		return ErrChamberEmpty
	}
	err := c.CustomValueFor(ruleKey, evaluationFromContext(ctx), v)
	if err != nil {
		return fmt.Errorf("could not convert custom rule %q: %w", ruleKey, err)
	}
	return nil
}
