package realm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steviebps/realm/api"
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

const (
	// maxRetryBackoff caps the backoff after transient failures.
	maxRetryBackoff = 30 * time.Second

	// slowRetryBackoff is the first wait, doubling up to maxSlowRetryBackoff,
	// after the server refuses realm's key (401, 403) or has no chamber at
	// its path (404), which retrying soon will not fix.
	slowRetryBackoff    = time.Minute
	maxSlowRetryBackoff = 5 * time.Minute

	// maxRetryAfter caps how long a server's Retry-After holds realm off.
	maxRetryAfter = 10 * time.Minute

	// maxChamberBytes bounds a chamber read from the server, polled or
	// streamed.
	maxChamberBytes = 16 << 20

	// maxErrorBytes bounds the error response realm reads.
	maxErrorBytes = 64 << 10

	// streamFailuresBeforePolling is how many stream attempts in a row may
	// deliver nothing before realm also polls (see streamDownAfter).
	streamFailuresBeforePolling = 3

	// repeatLogInterval is how often a failure that persists is logged again.
	repeatLogInterval = 5 * time.Minute
)

var (
	// retryBackoff is the first wait before retrying after a transient
	// failure; it doubles up to maxRetryBackoff.
	retryBackoff = time.Second

	// streamIdleTimeout is how long a stream may go without receiving
	// anything (the server sends a heartbeat every 30 seconds) before it is
	// considered dead and reopened. It catches connections silently dropped
	// by a proxy or NAT, which would otherwise leave the snapshot stale
	// indefinitely.
	streamIdleTimeout = 75 * time.Second

	// streamDownAfter is how long the stream may deliver nothing (no stream
	// open with the chamber) before realm also polls.
	streamDownAfter = time.Minute

	// streamRetryAfterFallback is how often realm tries the stream again
	// after the server answered a watch with a chamber (it does not stream).
	streamRetryAfterFallback = 5 * time.Minute
)

var (
	// errStreamClosed reports a stream the server ended, as it does
	// routinely (streams have a maximum lifetime) and on shutdown.
	errStreamClosed = errors.New("stream closed by the server")

	// errStreamIdle reports a stream closed for receiving nothing for
	// streamIdleTimeout.
	errStreamIdle = errors.New("no data or heartbeat received; the connection is presumed dead")

	// errNotStreaming reports a server that answered a watch with the
	// chamber itself: it predates streaming.
	errNotStreaming = errors.New("the server does not stream")

	// errNotChamber reports a payload that is not a chamber.
	errNotChamber = errors.New(`not a chamber: expected a JSON object with "rules"`)
)

// backoff produces capped exponential delays with jitter, so many clients
// reconnecting after the same outage spread out instead of arriving at once.
type backoff struct {
	initial, next, max time.Duration
}

func newBackoff(initial, max time.Duration) *backoff {
	initial = min(initial, max)
	return &backoff{initial: initial, next: initial, max: max}
}

// wait returns the next delay: a random duration in [d/2, d) for the current
// step d, which then doubles up to the cap.
func (b *backoff) wait() time.Duration {
	d := b.next
	b.next = min(b.next*2, b.max)
	return d/2 + rand.N(d/2+1)
}

func (b *backoff) reset() { b.next = b.initial }

// retryPolicy chooses how long to wait before retrying after a failure.
type retryPolicy struct {
	// fast backs off from transient failures: a second, doubling to its cap.
	fast *backoff
	// slow backs off from a refused key or a missing chamber: minutes.
	slow *backoff
}

func newRetryPolicy(max time.Duration) *retryPolicy {
	return &retryPolicy{
		fast: newBackoff(retryBackoff, max),
		slow: newBackoff(slowRetryBackoff, maxSlowRetryBackoff),
	}
}

// delay returns how long to wait before retrying after err: minutes after a
// 401, 403, or 404, at least what a 429's or 503's Retry-After asks for, and
// otherwise the fast backoff.
func (p *retryPolicy) delay(err error) time.Duration {
	var se *statusError
	if errors.As(err, &se) {
		if se.permanent() {
			return p.slow.wait()
		}
		return max(p.fast.wait(), se.retryAfter)
	}
	return p.fast.wait()
}

func (p *retryPolicy) reset() {
	p.fast.reset()
	p.slow.reset()
}

// holdOff is the least a poll that failed with err waits before polling
// again: minutes after a 401, 403, or 404, or what a 429's or 503's
// Retry-After asks for.
func holdOff(err error) time.Duration {
	var se *statusError
	if !errors.As(err, &se) {
		return 0
	}
	if se.permanent() {
		return slowRetryBackoff
	}
	return se.retryAfter
}

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

// failureLog keeps a failure that repeats, or comes and goes, from flooding
// the log: at most one failure is logged every repeatLogInterval, and the
// recovery from a run of failures only if one of them was logged.
type failureLog struct {
	mu sync.Mutex
	// failures counts the current run of failures.
	failures int
	// logged is when a failure was last logged, and reported whether one of
	// the current run was.
	logged   time.Time
	reported bool
}

// fail counts a failure at now, reporting whether to log it and how many
// failures in a row there have been.
func (l *failureLog) fail(now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures++
	if !l.logged.IsZero() && now.Sub(l.logged) < repeatLogInterval {
		return false, l.failures
	}
	l.logged, l.reported = now, true
	return true, l.failures
}

// ok ends a run of failures, returning how many there were and whether to
// log the recovery (one of them was logged).
func (l *failureLog) ok() (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, reported := l.failures, l.reported
	l.failures, l.reported = 0, false
	return n, reported
}

// load fetches the chamber and applies it, unless a newer one arrived while
// it was being fetched. Failures are logged, rate-limited.
func (rlm *Realm) load(ctx context.Context) error {
	version := rlm.snapshotVersion()
	c, rev, err := rlm.retrieveChamber(ctx, rlm.path)
	if err != nil {
		if !errors.Is(ctx.Err(), context.Canceled) {
			if log, n := rlm.loadFailures.fail(time.Now()); log {
				ev := rlm.logger.WarnCtx(ctx).Err(err).Str("path", rlm.path).Int("failures", n)
				if rlm.getChamber() == nil {
					ev.Msg("could not load flags; serving defaults until a load succeeds")
				} else {
					ev.Msg("could not refresh flags; serving the last ones loaded")
				}
			}
		}
		return err
	}
	rlm.setChamberIf(c, rev, version)
	if n, reported := rlm.loadFailures.ok(); reported {
		rlm.logger.InfoCtx(ctx).Str("path", rlm.path).Int("failures", n).Msg("flags loaded after earlier failures")
	}
	return nil
}

// poll refreshes the chamber on the polling interval until realm stops. err
// is the first load's. After a failure it retries sooner, with backoff, so an
// outage does not leave the snapshot stale (or never loaded) for a whole
// interval; a refused key or a missing chamber is retried after minutes.
func (rlm *Realm) poll(ctx context.Context, err error) {
	retry := newRetryPolicy(min(maxRetryBackoff, rlm.pollingInterval))
	for {
		wait := jitter(rlm.pollingInterval)
		if err != nil {
			wait = retry.delay(err)
		} else {
			retry.reset()
		}
		if !sleep(ctx, wait) {
			return
		}
		err = rlm.load(ctx)
	}
}

// stream keeps the snapshot current from a server-sent-events stream until
// realm stops, reconnecting with capped, jittered backoff (after minutes for a
// refused key or a missing chamber, and no sooner than a Retry-After asks).
//
// While the stream is down (it has delivered nothing for
// streamFailuresBeforePolling attempts in a row or for streamDownAfter, or
// the server does not stream at all), it also polls at the polling interval,
// until the stream delivers again. A server that does not stream is asked
// again every streamRetryAfterFallback.
func (rlm *Realm) stream(ctx context.Context) {
	retry := newRetryPolicy(maxRetryBackoff)
	s := &streamState{up: time.Now()}
	for {
		received, err := rlm.consumeStream(ctx, func() { rlm.streamDelivered(ctx, s) })
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		var wait time.Duration
		switch {
		case errors.Is(err, errNotStreaming):
			s.unsupported = true
			wait = jitter(streamRetryAfterFallback)
		case received:
			s.up = now
			retry.reset()
			wait = retry.delay(err)
		default:
			s.failures++
			wait = retry.delay(err)
		}
		rlm.logStreamEnd(ctx, received, err)
		if !rlm.pause(ctx, s, now.Add(wait)) {
			return
		}
	}
}

// streamState is what stream knows about the stream between attempts.
type streamState struct {
	// failures counts the attempts in a row that delivered nothing.
	failures int
	// up is when a stream last delivered, or streaming began.
	up time.Time
	// unsupported is set when the server answered a watch with a chamber.
	unsupported bool
	// polling is set while the stream is down, so the chamber is polled, and
	// nextPoll is when to poll next.
	polling  bool
	nextPoll time.Time
}

// down reports whether the stream is down at now (see stream).
func (s *streamState) down(now time.Time) bool {
	return s.unsupported || s.failures >= streamFailuresBeforePolling || !now.Before(s.up.Add(streamDownAfter))
}

// streamDelivered records that a stream delivered the chamber, the first
// time it does, ending any polling.
func (rlm *Realm) streamDelivered(ctx context.Context, s *streamState) {
	n, failed := rlm.streamFailures.ok()
	_, wasDown := rlm.streamDown.ok()
	if failed || wasDown {
		rlm.logger.InfoCtx(ctx).Int("failures", n).Msg("the chamber stream is connected again")
	}
	s.failures, s.unsupported, s.polling = 0, false, false
}

// pause waits until at, the next stream attempt, meanwhile polling the
// chamber while the stream is down. It reports false when realm stops.
func (rlm *Realm) pause(ctx context.Context, s *streamState, at time.Time) bool {
	for {
		now := time.Now()
		if !s.polling && s.down(now) {
			s.polling = true
			// The snapshot is current until it is streamDownAfter (or the
			// polling interval, if shorter) old.
			s.nextPoll = rlm.refreshedAt().Add(min(streamDownAfter, rlm.pollingInterval))
			if log, _ := rlm.streamDown.fail(now); log && s.unsupported {
				rlm.logger.InfoCtx(ctx).Str("interval", rlm.pollingInterval.String()).Msg("the realm server does not stream; polling instead, and trying the stream again every few minutes")
			} else if log {
				rlm.logger.WarnCtx(ctx).Str("interval", rlm.pollingInterval.String()).Msg("the chamber stream is down; polling until it recovers")
			}
		}
		if s.polling && !now.Before(s.nextPoll) {
			err := rlm.load(ctx)
			if ctx.Err() != nil {
				return false
			}
			now = time.Now()
			s.nextPoll = now.Add(max(jitter(rlm.pollingInterval), holdOff(err)))
		}
		if !now.Before(at) {
			return true
		}
		until := at
		if s.polling {
			until = minTime(until, s.nextPoll)
		} else {
			until = minTime(until, s.up.Add(streamDownAfter))
		}
		if !sleep(ctx, until.Sub(now)) {
			return false
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// logStreamEnd logs why a stream ended, unless that is routine: the server
// closed a stream that had delivered (streams have a maximum lifetime), or
// it does not stream (logged when polling starts). Failures are rate-limited.
func (rlm *Realm) logStreamEnd(ctx context.Context, received bool, err error) {
	switch {
	case errors.Is(err, errNotStreaming):
	case received && errors.Is(err, errStreamClosed):
		rlm.logger.DebugCtx(ctx).Msg("chamber stream ended; reconnecting")
	default:
		if log, n := rlm.streamFailures.fail(time.Now()); log {
			rlm.logger.WarnCtx(ctx).Err(err).Int("failures", n).Msg("chamber stream unavailable; reconnecting")
		}
	}
}

// consumeStream opens one stream and applies the chambers it delivers until
// it ends, reporting whether it applied any; it calls delivered when it
// applies the first. errNotStreaming means the server answered with the
// chamber itself (it predates streaming), which has been applied.
func (rlm *Realm) consumeStream(ctx context.Context, delivered func()) (received bool, err error) {
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

	version := rlm.snapshotVersion()
	resp, err := rlm.client.Watch(ctx, rlm.path)
	if err != nil {
		return false, idleErr(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// An error from the server or a proxy in front of it (during a
		// deploy, say) is transient: retry the stream rather than giving up
		// on it.
		return false, fmt.Errorf("chamber stream: %w", newStatusError(resp))
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// A server that predates streaming ignores ?watch=true and answers
		// with the chamber. Anything else (a proxy's sign-in page, say) is a
		// failed attempt, retried as a stream.
		c, err := readChamberResponse(resp.Body)
		if err != nil {
			return false, idleErr(fmt.Errorf("chamber stream: the server answered with neither an event stream nor a chamber: %w", err))
		}
		rlm.setChamberIf(c, parseRevision(resp.Header.Get(api.RevisionHeader)), version)
		return false, errNotStreaming
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxChamberBytes)
	var (
		event   string
		id      string
		data    []byte
		hasData bool
	)
	for scanner.Scan() {
		idle.Reset(streamIdleTimeout)
		line := scanner.Bytes()
		if len(line) == 0 {
			// A blank line dispatches the event. Only chamber events (or
			// unnamed ones) carry a chamber; others, which a newer server may
			// send, are skipped.
			if hasData && (event == "" || event == "chamber") && rlm.applyEvent(ctx, data, parseRevision(id)) && !received {
				received = true
				delivered()
			}
			event, id, data, hasData = "", "", data[:0], false
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			event = string(value)
		case "id":
			// the revision the event's chamber was read at
			id = string(value)
		case "data":
			if hasData {
				data = append(data, '\n')
			}
			data = append(data, value...)
			hasData = true
		}
		// Comments (": ping" heartbeats) and the retry field need nothing.
	}

	err = scanner.Err()
	if err == nil {
		err = errStreamClosed
	}
	return received, idleErr(err)
}

// applyEvent applies the chamber an event carries, at the server's revision
// rev (0 when unknown), reporting whether it did. One that does not hold a
// chamber is skipped (the last one stays) and logged, rate-limited.
func (rlm *Realm) applyEvent(ctx context.Context, data []byte, rev uint64) bool {
	c, err := decodeChamber(data)
	if err != nil {
		if log, n := rlm.badEvents.fail(time.Now()); log {
			rlm.logger.WarnCtx(ctx).Err(err).Int("failures", n).Msg("skipped a streamed chamber that could not be read; keeping the last one")
		}
		return false
	}
	_, _ = rlm.badEvents.ok()
	rlm.setChamber(c, rev)
	return true
}

// parseRevision parses a revision the server sent (see api.RevisionHeader),
// returning 0 when there is none or it is not a revision.
func parseRevision(s string) uint64 {
	rev, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return rev
}

// retrieveChamber fetches the chamber at path, with the server's revision of
// it (0 when the server reports none).
func (rlm *Realm) retrieveChamber(ctx context.Context, path string) (_ *Chamber, rev uint64, err error) {
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

	res, err := rlm.client.PerformRequest(ctx, http.MethodGet, strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("could not get %q: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("could not get %q: %w", path, newStatusError(res))
	}
	c, err := readChamberResponse(res.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("could not read %q: %w", path, err)
	}
	rev = parseRevision(res.Header.Get(api.RevisionHeader))
	if rev != 0 {
		// as decimal text, like the header: an int64 cannot hold every revision
		span.SetAttributes(attribute.String("realm.revision", strconv.FormatUint(rev, 10)))
	}
	return c, rev, nil
}

// readChamberResponse reads a response envelope holding a chamber, of at
// most maxChamberBytes.
func readChamberResponse(body io.Reader) (*Chamber, error) {
	b, err := io.ReadAll(io.LimitReader(body, maxChamberBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxChamberBytes {
		return nil, fmt.Errorf("the response is larger than %d bytes", maxChamberBytes)
	}
	// Decoded leniently: fields added to the envelope by newer servers must
	// not break older SDKs.
	var envelope api.HTTPErrorAndDataResponse
	if err := json.Unmarshal(b, &envelope); err != nil {
		return nil, fmt.Errorf("the response is not a realm response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return nil, errors.New(strings.Join(envelope.Errors, "; "))
	}
	return decodeChamber(envelope.Data)
}

// decodeChamber decodes a chamber the server sent. Anything else (null, a
// value that is not an object, or an object without "rules") is an error
// rather than an empty chamber, so a stray payload never wipes out every
// flag.
func decodeChamber(data []byte) (*Chamber, error) {
	var probe struct {
		Rules json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || probe.Rules == nil {
		return nil, errNotChamber
	}
	var c Chamber
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// statusError is a response, from the realm server or a proxy in front of
// it, with a status other than 200.
type statusError struct {
	code int
	// status is the status line, such as "503 Service Unavailable".
	status string
	// messages are the errors the server reported, if any.
	messages []string
	// retryAfter is how long a 429 or 503 asked clients to wait.
	retryAfter time.Duration
}

// newStatusError describes res, reading what the server said from the start
// of its body.
func newStatusError(res *http.Response) *statusError {
	e := &statusError{code: res.StatusCode, status: res.Status, retryAfter: retryAfter(res)}
	var body api.HTTPErrorResponse
	if json.NewDecoder(io.LimitReader(res.Body, maxErrorBytes)).Decode(&body) == nil {
		e.messages = body.Errors
	}
	return e
}

func (e *statusError) Error() string {
	if len(e.messages) == 0 {
		return e.status
	}
	return e.status + ": " + strings.Join(e.messages, "; ")
}

// permanent reports whether retrying soon is pointless: the server refused
// realm's key, or has no chamber at its path.
func (e *statusError) permanent() bool {
	switch e.code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

// retryAfter returns how long a 429 or 503 response asks clients to wait
// (its Retry-After, in seconds or as a date), up to maxRetryAfter.
func retryAfter(res *http.Response) time.Duration {
	if res.StatusCode != http.StatusTooManyRequests && res.StatusCode != http.StatusServiceUnavailable {
		return 0
	}
	v := strings.TrimSpace(res.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(min(secs, int(maxRetryAfter/time.Second)), 0)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(min(time.Until(t), maxRetryAfter), 0)
	}
	return 0
}
