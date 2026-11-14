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
	// semver caches the canonical form of the "version" attribute for the
	// views of a subtree snapshot.
	semver semverCache
	// path is the chamber realm reads, or empty with a template.
	path string
	// template is the path template (see WithPathTemplate), or nil when
	// realm reads the single chamber at path.
	template *pathTemplate
	// lifecycle guards started, ctx, and cancel.
	lifecycle sync.Mutex
	started   bool
	// ctx is the context of the background refresh; Stop cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// mu guards the snapshot (root, or views with a template) and the fields
	// that describe it.
	mu   sync.RWMutex
	root *ChamberEntry
	// views is the subtree snapshot with a template: the view of each path
	// at or below the template's root, nil until the first load. The views
	// are immutable; the map changes in place under mu, so a change costs the
	// same however large the subtree.
	views map[string]*ChamberEntry
	// unsupportedSeen holds the flags of a subtree snapshot that this SDK
	// cannot fully evaluate, as logged.
	unsupportedSeen map[string]struct{}
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

	// loadFailures, streamFailures, streamDown, badEvents, and badViews keep
	// a failure that repeats from flooding the log.
	loadFailures, streamFailures, streamDown, badEvents, badViews failureLog
}

type RealmConfig struct {
	client             *client.HttpClient
	path               string
	pathTemplate       *string
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

// WithPathTemplate makes realm serve many chambers at once, picking one for
// each request from its evaluation context, instead of the single chamber of
// WithPath (which it cannot be combined with). A template is a path whose
// segments may be placeholders named after attributes, such as
// "/prod/tenants/{tenantId}/{locationId}/".
//
// Realm reads (or, with WithStreaming, watches) every chamber below the
// template's root: its literal prefix up to the first placeholder
// ("/prod/tenants/"). A request gets the view of the deepest path, filling
// placeholders in order from its attributes (as targeting clauses look them
// up, "key" being EvaluationContext.Key), that has a chamber: a location
// without a chamber gets its tenant's, and a tenant without one the root's.
// Filling stops at an attribute that is missing or empty, or holds "/" or
// "..", or is ".". ChamberPath reports the path a context resolved to.
//
// NewContextWithEvaluation resolves the template once and pins the view it
// picked to the context; NewContext pins the root's view. A flag getter with
// a context that pins nothing resolves it on each call.
//
// NewRealm refuses a template that does not start with "/", or has an empty
// segment, a placeholder that is not a whole segment, or a placeholder name
// with characters other than A-Z a-z 0-9 _ - and ".". See
// docs/design/subtree.md.
func WithPathTemplate(template string) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		rc.pathTemplate = &template
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

	var template *pathTemplate
	switch {
	case cfg.pathTemplate != nil && cfg.path != "":
		return nil, errors.New("WithPath and WithPathTemplate cannot be combined")
	case cfg.pathTemplate != nil:
		t, err := parsePathTemplate(*cfg.pathTemplate)
		if err != nil {
			return nil, err
		}
		template = t
	case cfg.path == "":
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

	v := attributes[VersionAttribute]
	return &Realm{
		tracer:          otel.Tracer("github.com/steviebps/realm"),
		logger:          logger,
		client:          cfg.client,
		path:            cfg.path,
		template:        template,
		attributes:      attributes,
		semver:          semverCache{raw: v, canonical: canonicalSemver(v)},
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

// Start loads the chamber (or, with WithPathTemplate, the subtree), then
// keeps it current in the background (by polling, or streaming with
// WithStreaming) until Stop.
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
		if rlm.hasSnapshot() {
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

// hasSnapshot reports whether a load (or a stream) has delivered the
// chamber, or the subtree.
func (rlm *Realm) hasSnapshot() bool {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	if rlm.template != nil {
		return rlm.views != nil
	}
	return rlm.root != nil
}

// resolve returns the subtree view ec resolves to (see WithPathTemplate), or
// nil when the snapshot holds none.
func (rlm *Realm) resolve(ec EvaluationContext) *ChamberEntry {
	ec = ec.withDefaults(rlm.attributes)
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	return rlm.template.resolve(rlm.views, ec)
}

// rootView returns the view of the template's root, or nil when the
// snapshot holds none.
func (rlm *Realm) rootView() *ChamberEntry {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()
	return rlm.views[rlm.template.root]
}

func chamberFromContext(ctx context.Context) *ChamberEntry {
	c, ok := ctx.Value(RequestContextKey).(*ChamberEntry)
	if !ok {
		return nil
	}
	return c
}

// chamberFromContext returns the view pinned to ctx or, when it pins none,
// the current chamber, or with a template the view the context's
// EvaluationContext resolves to.
func (rlm *Realm) chamberFromContext(ctx context.Context) *ChamberEntry {
	c := chamberFromContext(ctx)
	if c != nil {
		return c
	}
	if rlm.template != nil {
		return rlm.resolve(evaluationFromContext(ctx))
	}
	return rlm.getChamber()
}

// NewContext pins the current chamber snapshot to ctx, so every flag read
// with the returned context sees the same chamber. With WithPathTemplate, it
// pins the view of the template's root.
func (rlm *Realm) NewContext(ctx context.Context) context.Context {
	var c *ChamberEntry
	if rlm.template != nil {
		c = rlm.rootView()
	} else {
		c = rlm.getChamber()
	}
	ctx = context.WithValue(ctx, RequestContextKey, c)
	return ctx
}

// ChamberPath returns the path of the chamber flag reads with ctx use: with
// WithPathTemplate, the path of the view pinned to ctx (see
// NewContextWithEvaluation) or that its evaluation context resolves to, and
// otherwise the path given to WithPath. It is "" when there is no chamber
// (before the first load, say), when flag reads return ErrChamberEmpty.
//
// A placeholder filled from a mistyped or missing attribute falls back to a
// shallower chamber silently; ChamberPath shows where a request landed.
func (rlm *Realm) ChamberPath(ctx context.Context) string {
	c := rlm.chamberFromContext(ctx)
	switch {
	case c == nil:
		return ""
	case rlm.template == nil:
		return rlm.path
	}
	return c.path
}

// Revision returns the server's storage revision of the chamber snapshot
// pinned to ctx (see NewContext), or of the current snapshot when ctx has
// none, so a service can record which configuration a request ran under (on
// its spans or logs, say). It is the highest revision among the chamber and
// the parents its inherited view merges, so it changes whenever any of them
// does. With WithPathTemplate, it is the revision of the view pinned to ctx,
// or that its evaluation context resolves to.
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
//
// With WithPathTemplate, it pins the view ec resolves to instead, so flag
// reads with the returned context do not resolve the template again. Before
// the first load there is none to pin, and each read resolves it.
func (rlm *Realm) NewContextWithEvaluation(ctx context.Context, ec EvaluationContext) context.Context {
	if rlm.template != nil {
		ctx = context.WithValue(ctx, RequestContextKey, rlm.resolve(ec))
	} else {
		ctx = rlm.NewContext(ctx)
	}
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
