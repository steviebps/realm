package realm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/helper/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var refreshes, _ = otel.Meter("github.com/steviebps/realm").Int64Counter(
	"realm.sdk.refreshes",
	metric.WithDescription("Chamber loads by the SDK (Start and polling), by result."),
	metric.WithUnit("{refresh}"))

type Realm struct {
	attributes map[string]string
	path       string
	// lifecycle guards started and cancel.
	lifecycle       sync.Mutex
	started         bool
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	mu              sync.RWMutex
	root            *ChamberEntry
	client          *client.HttpClient
	pollingInterval time.Duration
	streaming       bool
	logger          *logging.TracedLogger
	tracer          trace.Tracer
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

// WithStreaming enables real-time updates over a server-sent-events stream. When
// enabled, realm applies chamber changes as the server pushes them, reconnecting
// with backoff and falling back to polling if the server does not support
// streaming. Defaults to false (polling only).
func WithStreaming(streaming bool) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		rc.streaming = streaming
		return rc
	})
}

// WithLogger sets the logger realm reports refresh failures and reconnects
// to. By default realm writes JSON lines to standard error; pass
// zerolog.Nop() to silence it.
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

	if cfg.pollingInterval <= 0 {
		cfg.pollingInterval = DefaultPollingInterval
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
		logger = logging.NewTracedLogger()
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

// Start loads the chamber, then keeps it current in the background (by
// polling, or streaming with WithStreaming) until Stop.
//
// If the first load fails, Start returns its error but keeps retrying in the
// background, with backoff, so a service can start while realm is
// unreachable: until a load succeeds, flag reads return their defaults along
// with ErrChamberEmpty. Calling Start again only retries the load
// synchronously. A stopped Realm cannot be restarted.
func (rlm *Realm) Start() error {
	rlm.lifecycle.Lock()
	defer rlm.lifecycle.Unlock()

	if rlm.started {
		if rlm.getChamber() != nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(rlm.logger.WithContext(context.Background()), DefaultStartTimeout)
		defer cancel()
		chamber, err := rlm.retrieveChamber(ctx, rlm.path)
		if err == nil {
			rlm.setChamber(chamber)
		}
		return err
	}
	rlm.started = true

	ctx, cancel := context.WithCancel(rlm.logger.WithContext(context.Background()))
	rlm.cancel = cancel

	chamber, err := rlm.retrieveChamber(ctx, rlm.path)
	if err == nil {
		rlm.setChamber(chamber)
	}

	loaded := err == nil
	rlm.wg.Add(1)
	go func() {
		defer rlm.wg.Done()
		if rlm.streaming {
			rlm.stream(ctx)
		} else {
			rlm.poll(ctx, loaded)
		}
	}()

	return err
}

const (
	// DefaultStartTimeout bounds a synchronous chamber load by a repeated
	// Start.
	DefaultStartTimeout = 15 * time.Second

	// retryBackoff is the first wait before retrying a failed refresh; it
	// doubles up to maxRetryBackoff.
	retryBackoff    = time.Second
	maxRetryBackoff = 30 * time.Second
)

// streamIdleTimeout is how long a stream may go without receiving anything
// (the server sends a heartbeat every 30 seconds) before it is considered
// dead and reopened. It catches connections silently dropped by a proxy or
// NAT, which would otherwise leave the snapshot stale indefinitely.
var streamIdleTimeout = 75 * time.Second

// backoff produces capped exponential delays with jitter, so many clients
// reconnecting after the same outage spread out instead of arriving at once.
type backoff struct {
	next, max time.Duration
}

func newBackoff(max time.Duration) *backoff {
	return &backoff{next: min(retryBackoff, max), max: max}
}

// wait returns the next delay: a random duration in [d/2, d) for the current
// step d, which then doubles up to the cap.
func (b *backoff) wait() time.Duration {
	d := b.next
	b.next = min(b.next*2, b.max)
	return d/2 + rand.N(d/2+1)
}

func (b *backoff) reset() { b.next = min(retryBackoff, b.max) }

// jitter spreads a fixed interval by up to ±10%.
func jitter(d time.Duration) time.Duration {
	spread := d / 5
	if spread <= 0 {
		return d
	}
	return d - spread/2 + rand.N(spread)
}

// sleep waits for d or until ctx is done, reporting whether d elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// poll refreshes the chamber snapshot on the polling interval until realm is
// stopped. Failed refreshes are retried sooner, with backoff, so an outage
// does not leave the snapshot stale (or never loaded) for a whole interval.
func (rlm *Realm) poll(ctx context.Context, loaded bool) {
	retry := newBackoff(min(maxRetryBackoff, rlm.pollingInterval))
	wait := jitter(rlm.pollingInterval)
	if !loaded {
		wait = retry.wait()
	}
	for sleep(ctx, wait) {
		if chamber, err := rlm.retrieveChamber(ctx, rlm.path); err == nil {
			rlm.setChamber(chamber)
			retry.reset()
			wait = jitter(rlm.pollingInterval)
		} else {
			wait = retry.wait()
		}
	}
	rlm.logger.InfoCtx(ctx).Msg("shutting down realm")
}

// stream keeps the chamber snapshot current from a server-sent-events stream,
// reconnecting with capped, jittered exponential backoff. If the server does
// not support streaming, it falls back to polling permanently.
func (rlm *Realm) stream(ctx context.Context) {
	retry := newBackoff(maxRetryBackoff)
	for ctx.Err() == nil {
		supported, received, err := rlm.consumeStream(ctx)
		if !supported {
			rlm.logger.InfoCtx(ctx).Msg("realm server does not support streaming; falling back to polling")
			rlm.poll(ctx, rlm.getChamber() != nil)
			return
		}
		switch {
		case err == nil || ctx.Err() != nil:
		case errors.Is(err, errStreamClosed) && received:
			rlm.logger.DebugCtx(ctx).Msg("chamber stream ended; reconnecting")
		default:
			rlm.logger.WarnCtx(ctx).Str("error", err.Error()).Msg("chamber stream disconnected; reconnecting")
		}
		if received {
			retry.reset()
		}
		if !sleep(ctx, retry.wait()) {
			break
		}
	}
	rlm.logger.InfoCtx(ctx).Msg("shutting down realm")
}

// errStreamClosed reports a stream the server ended, as it does routinely
// (streams have a maximum lifetime) and on shutdown.
var errStreamClosed = errors.New("stream closed by the server")

// errStreamIdle reports a stream closed for receiving nothing for
// streamIdleTimeout.
var errStreamIdle = errors.New("no data or heartbeat received; the connection is presumed dead")

// consumeStream opens one streaming connection and applies chamber updates until
// it drops. It reports whether the server supports streaming (false means the
// caller should fall back to polling) and whether at least one event was
// applied (used to reset reconnect backoff).
func (rlm *Realm) consumeStream(ctx context.Context) (supported bool, received bool, err error) {
	// The connection is torn down when nothing arrives for streamIdleTimeout,
	// including while waiting for the response headers.
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(streamIdleTimeout, cancel)
	defer idle.Stop()
	idleErr := func(err error) error {
		if parent.Err() == nil && ctx.Err() != nil {
			return errStreamIdle
		}
		return err
	}

	resp, err := rlm.client.Watch(ctx, rlm.path)
	if err != nil {
		// A transient connection error: keep retrying the stream.
		return true, false, idleErr(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// An error from the server or a proxy in front of it (during a
		// deploy, say) is transient: retry the stream rather than giving up
		// on it.
		return true, false, fmt.Errorf("chamber stream: unexpected status %s", resp.Status)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// The server answered with the chamber itself instead of a stream (it
		// predates streaming and ignored ?watch=true): fall back to polling.
		return false, false, nil
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var data strings.Builder
	for scanner.Scan() {
		idle.Reset(streamIdleTimeout)
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			// blank line terminates an event
			if data.Len() == 0 {
				continue
			}
			var c Chamber
			if uerr := json.Unmarshal([]byte(data.String()), &c); uerr != nil {
				rlm.logger.ErrorCtx(ctx).Str("error", uerr.Error()).Msg("could not unmarshal streamed chamber; keeping the last one")
			} else {
				rlm.setChamber(&c)
				received = true
			}
			data.Reset()
		default:
			// comment/heartbeat (": ping") or other SSE fields (event:, id:) — ignore
		}
	}

	err = scanner.Err()
	if err == nil {
		err = errStreamClosed
	}
	return true, received, idleErr(err)
}

// Stop stops realm and blocks until the background refresh goroutine has exited.
// It is safe to call more than once.
func (rlm *Realm) Stop() {
	rlm.lifecycle.Lock()
	if rlm.cancel != nil {
		rlm.cancel()
	}
	rlm.lifecycle.Unlock()
	rlm.wg.Wait()
}

func (rlm *Realm) retrieveChamber(ctx context.Context, path string) (_ *Chamber, err error) {
	ctx, span := rlm.tracer.Start(ctx, "retrieveChamber", trace.WithAttributes(attribute.String("realm.path", path)))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			refreshes.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
		} else {
			refreshes.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "ok")))
		}
		span.End()
	}()

	logger := rlm.logger
	client := rlm.client

	res, err := client.PerformRequest(ctx, "GET", strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		logger.ErrorCtx(ctx).Msg(fmt.Sprintf("could not perform request for getting: %q, %s", path, err.Error()))
		return nil, err
	}
	defer res.Body.Close()

	// Decoded leniently: fields added to the envelope by newer servers must
	// not break older SDKs.
	var httpRes api.HTTPErrorAndDataResponse
	if err := json.NewDecoder(res.Body).Decode(&httpRes); err != nil {
		err = fmt.Errorf("could not read response for %q (status %s): %w", path, res.Status, err)
		logger.ErrorCtx(ctx).Msg(err.Error())
		return nil, err
	}

	if len(httpRes.Errors) > 0 || res.StatusCode != http.StatusOK {
		err := fmt.Errorf("could not get %q: %s %s", path, res.Status, strings.Join(httpRes.Errors, "; "))
		logger.ErrorCtx(ctx).Msg(err.Error())
		return nil, err
	}

	var c Chamber
	err = json.Unmarshal(httpRes.Data, &c)
	if err != nil {
		logger.ErrorCtx(ctx).Str("error", err.Error()).Msg(fmt.Sprintf("could not unmarshal chamber from response for getting: %q", path))
		return nil, err
	}

	return &c, nil
}

func (rlm *Realm) setChamber(c *Chamber) {
	entry := NewChamberEntry(c, rlm.attributes)
	rlm.mu.Lock()
	defer rlm.mu.Unlock()
	rlm.root = entry
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
