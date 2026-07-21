package realm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/steviebps/realm/api"
	"github.com/steviebps/realm/client"
	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Realm struct {
	applicationVersion string
	path               string
	initSync           sync.Once
	stopCh             chan struct{}
	cancel             context.CancelFunc
	mu                 sync.RWMutex
	root               *ChamberEntry
	client             *client.HttpClient
	pollingInterval    time.Duration
	streaming          bool
	logger             *logging.TracedLogger
	tracer             trace.Tracer
}

type RealmConfig struct {
	client             *client.HttpClient
	path               string
	applicationVersion string
	// pollingInterval is how often realm will refetch the chamber from the realm server
	pollingInterval time.Duration
	// streaming makes realm keep its chamber current via a server-sent-events
	// stream (with polling as an automatic fallback) instead of only polling
	streaming bool
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

func WithVersion(version string) RealmOption {
	return realmOptionFunc(func(rc RealmConfig) RealmConfig {
		rc.applicationVersion = version
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

// NewRealm returns a new Realm struct that carries out all of the core features
func NewRealm(options ...RealmOption) (*Realm, error) {
	cfg := RealmConfig{}

	for _, opt := range options {
		cfg = opt.apply(cfg)
	}

	// TODO: setup sane default
	if cfg.client == nil {
		return nil, errors.New("client option must not be nil")
	}

	// TODO: setup sane default
	if cfg.path == "" {
		return nil, errors.New("path must not be empty")
	}

	if cfg.pollingInterval <= 0 {
		cfg.pollingInterval = DefaultPollingInterval
	}

	return &Realm{
		tracer:             otel.Tracer("github.com/steviebps/realm"),
		logger:             logging.NewTracedLogger(),
		client:             cfg.client,
		path:               cfg.path,
		applicationVersion: cfg.applicationVersion,
		stopCh:             make(chan struct{}),
		pollingInterval:    cfg.pollingInterval,
		streaming:          cfg.streaming,
	}, nil
}

// Start starts realm and initializes the underlying chamber
func (rlm *Realm) Start() error {
	var err error
	ctx, cancel := context.WithCancel(rlm.logger.WithContext(context.Background()))
	rlm.cancel = cancel
	rlm.initSync.Do(func() {
		var chamber *Chamber
		if chamber, err = rlm.retrieveChamber(ctx, rlm.path); err == nil {
			rlm.setChamber(chamber)
		}
	})

	if err != nil {
		cancel()
		return err
	}

	if rlm.streaming {
		go rlm.stream(ctx)
	} else {
		go rlm.poll(ctx)
	}

	return nil
}

// poll refreshes the chamber snapshot on a fixed interval until realm is stopped.
func (rlm *Realm) poll(ctx context.Context) {
	ticker := time.NewTicker(rlm.pollingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-rlm.stopCh:
			rlm.logger.InfoCtx(ctx).Msg("shutting down realm")
			return
		case <-ticker.C:
			if chamber, err := rlm.retrieveChamber(ctx, rlm.path); err == nil {
				rlm.setChamber(chamber)
			}
		}
	}
}

// stream keeps the chamber snapshot current from a server-sent-events stream,
// reconnecting with capped exponential backoff. If the server does not support
// streaming, it falls back to polling permanently.
func (rlm *Realm) stream(ctx context.Context) {
	const (
		initialBackoff = 1 * time.Second
		maxBackoff     = 30 * time.Second
	)
	backoff := initialBackoff

	for {
		select {
		case <-rlm.stopCh:
			rlm.logger.InfoCtx(ctx).Msg("shutting down realm")
			return
		default:
		}

		supported, received, err := rlm.consumeStream(ctx)
		if !supported {
			rlm.logger.InfoCtx(ctx).Msg("realm server does not support streaming; falling back to polling")
			rlm.poll(ctx)
			return
		}
		if err != nil {
			rlm.logger.ErrorCtx(ctx).Str("error", err.Error()).Msg("chamber stream disconnected; reconnecting")
		}
		if received {
			backoff = initialBackoff
		}

		select {
		case <-rlm.stopCh:
			return
		case <-time.After(backoff):
		}

		if !received {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// consumeStream opens one streaming connection and applies chamber updates until
// it drops. It reports whether the server supports streaming (false means the
// caller should fall back to polling) and whether at least one event was
// applied (used to reset reconnect backoff).
func (rlm *Realm) consumeStream(ctx context.Context) (supported bool, received bool, err error) {
	ctx, span := rlm.tracer.Start(ctx, "consumeStream", trace.WithAttributes(attribute.String("realm.path", rlm.path)))
	defer span.End()

	resp, err := rlm.client.Watch(ctx, rlm.path)
	if err != nil {
		// A transient connection error: keep retrying the stream.
		return true, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// The server did not give us an event stream (older server, or the watch
		// parameter is unsupported): fall back to polling.
		return false, false, nil
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var data strings.Builder
	for scanner.Scan() {
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
				rlm.logger.ErrorCtx(ctx).Str("error", uerr.Error()).Msg("could not unmarshal streamed chamber")
			} else {
				rlm.setChamber(&c)
				received = true
			}
			data.Reset()
		default:
			// comment/heartbeat (": ping") or other SSE fields (event:, id:) — ignore
		}
	}

	return true, received, scanner.Err()
}

// Stop stops realm and flushes any pending tasks
func (rlm *Realm) Stop() {
	close(rlm.stopCh)
	if rlm.cancel != nil {
		rlm.cancel()
	}
}

func (rlm *Realm) retrieveChamber(ctx context.Context, path string) (*Chamber, error) {
	ctx, span := rlm.tracer.Start(ctx, "retrieveChamber", trace.WithAttributes(attribute.String("realm.path", path)))
	defer span.End()

	logger := rlm.logger
	client := rlm.client

	res, err := client.PerformRequest(ctx, "GET", strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		logger.ErrorCtx(ctx).Msg(fmt.Sprintf("could not perform request for getting: %q, %s", path, err.Error()))
		return nil, err
	}
	defer res.Body.Close()

	var httpRes api.HTTPErrorAndDataResponse
	if err := utils.ReadInterfaceWith(res.Body, &httpRes); err != nil {
		logger.ErrorCtx(ctx).Str("error", err.Error()).Msg(fmt.Sprintf("could not read response for getting: %q", path))
		return nil, err
	}

	if len(httpRes.Errors) > 0 {
		logger.ErrorCtx(ctx).Msg(fmt.Sprintf("could not get %q: %s", path, httpRes.Errors))
		return nil, fmt.Errorf("%s", httpRes.Errors)
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
	entry := NewChamberEntry(c, rlm.applicationVersion)
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
// Rule retrievals performed with the returned context apply per-subject
// targeting, such as percentage rollouts bucketed by EvaluationContext.Key.
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
