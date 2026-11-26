package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/pkg/storage"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// streamChamber holds an SSE connection open and pushes the chamber at path
// whenever it changes. Clients evaluate the chamber themselves, so its
// schedules need no pushes of their own.
func streamChamber(w http.ResponseWriter, r *http.Request, reader *chamberReader, brk *broker, path string) {
	streamEvents(w, r, brk, path, "chamber", false, func(ctx context.Context) (streamPayload, error) {
		return reader.Watched(ctx, path)
	})
}

// streamPayload is what a stream sends as one event.
type streamPayload struct {
	data []byte
	// revision is the storage revision data was produced from, sent as the
	// event's id; 0 when the storage does not track revisions.
	revision uint64
	// changesAt is when data changes by itself (a schedule boundary of an
	// evaluated flag); zero when it changes only with writes.
	changesAt time.Time
	// sent, when set, is called once the stream has sent data, and not for
	// data it leaves unsent because it is unchanged.
	sent func()
}

// streamEvents holds an SSE connection open and sends the payload produced by
// current as a single event: once at the start, then again whenever the broker
// signals a change at or above path and the payload differs from the last one
// sent. It emits periodic heartbeats so dead connections and idle
// intermediaries are handled. A payload spanning several lines is sent as
// several data fields (see eventText).
//
// Each event's id is the revision its payload was produced from, when the
// storage tracks revisions. A write that leaves the payload unchanged sends
// nothing, so the last id names the first revision that produced the payload
// the client holds, which may be older than storage's.
//
// current may also return a time at which the payload changes by itself (a
// schedule boundary of an evaluated flag); the payload is produced again then.
//
// A payload whose source does not exist yet is not fatal: the stream waits for
// it to be created. Neither is a failure to produce it (storage unreachable,
// say): the client keeps the payload it has, and it is produced again with
// backoff, so a storage outage does not make every client reconnect at once.
//
// The request must have been read in full: the stream lifts the request's
// read deadline, and bounds each write instead, so a client that stops
// reading loses its stream rather than holding it.
//
// public marks a stream anyone may open, which the broker limits to a share
// of its streams (see broker.publicMax).
func streamEvents(w http.ResponseWriter, r *http.Request, brk *broker, path, event string, public bool, current func(context.Context) (streamPayload, error)) {
	ctx := r.Context()
	logger := logging.Ctx(ctx)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Bool("realm.server.watch", true))

	// The headers are committed as it opens, so clients see the stream open
	// even while they wait for the payload's source to be created.
	st := openStream(ctx, w, func() (<-chan struct{}, func(), bool) { return brk.Subscribe(path, public) }, nil)
	if st == nil {
		return
	}
	defer st.close()
	retry := st.retry

	// recheck fires when the last payload produced changes by itself.
	recheck := time.NewTimer(0)
	recheck.Stop()
	defer recheck.Stop()

	var last []byte
	// send writes the current payload unless it is unchanged. It returns
	// false when the connection should be torn down.
	send := func() bool {
		payload, err := current(ctx)
		if ctx.Err() != nil {
			return false
		}
		if err != nil {
			var nfError *storage.NotFoundError
			if errors.As(err, &nfError) {
				// wait for it to be created
				retry.failures = 0
				recheck.Stop()
				return true
			}
			if errors.Is(err, storage.ErrInvalidPath) {
				// storage refuses the path: it will never exist
				return false
			}
			delay := retry.fail()
			// the shared read logs the failure itself, once for every stream
			logger.DebugCtx(ctx).Str("path", path).Str("error", err.Error()).Dur("retryIn", delay).Msg("could not produce a stream payload")
			return true
		}
		retry.succeed()
		if payload.changesAt.IsZero() {
			recheck.Stop()
		} else {
			recheck.Reset(time.Until(payload.changesAt))
		}
		if last != nil && bytes.Equal(payload.data, last) {
			return true
		}
		if !st.conn.write(eventText(event, payload.revision, payload.data)) {
			return false
		}
		if payload.sent != nil {
			payload.sent()
		}
		last = payload.data
		return true
	}

	if !send() {
		return
	}
	st.run(ctx, send, send, recheck.C)
}

// eventStream is an open server-sent event stream, subscribed to the changes
// it streams.
type eventStream struct {
	conn *streamConn
	// signals receives the broker's signals of changes.
	signals <-chan struct{}
	// retry schedules a failed attempt to produce what the stream sends.
	retry *streamBackoff
	// release undoes what opening the stream did, last first.
	release []func()
}

// openStream opens a server-sent event stream on w, in the order every
// stream needs:
//
//  1. it responds 500 when w cannot stream;
//  2. it subscribes to the changes the stream sends with subscribe, which
//     returns the broker's signals, how to unsubscribe, and whether the
//     broker had room (otherwise it responds 503, and the client retries
//     later);
//  3. it runs prepare, when not nil, which may still respond with a status
//     of its own (returning ok false) or wrap w (to compress it), returning
//     the writer to stream on and what to run when the stream ends;
//  4. and only then starts the response (see startStream).
//
// Subscribing comes before anything is read, so that a change made while
// the stream reads what it first sends is signaled rather than lost; prepare
// comes before the response starts, so that it can still refuse with a
// status. openStream returns nil when the stream did not open, having
// responded; otherwise the caller closes the stream when it ends. A step
// that fails or panics has what came before it undone, so a panic the
// request lifecycle recovers from does not keep a broker subscription.
func openStream(ctx context.Context, w http.ResponseWriter, subscribe func() (<-chan struct{}, func(), bool), prepare func(http.ResponseWriter) (http.ResponseWriter, func(), bool)) *eventStream {
	if _, ok := w.(http.Flusher); !ok {
		handleError(ctx, w, http.StatusInternalServerError, createResponseWithErrors(nil, []string{"streaming is not supported"}))
		return nil
	}
	signals, unsubscribe, ok := subscribe()
	if !ok {
		w.Header().Set("Retry-After", "5")
		handleError(ctx, w, http.StatusServiceUnavailable, createResponseWithErrors(nil, []string{"too many open streams; try again later"}))
		return nil
	}
	st := &eventStream{signals: signals, release: []func(){unsubscribe}}
	opened := false
	defer func() {
		if !opened {
			st.close()
		}
	}()
	if prepare != nil {
		pw, end, ok := prepare(w)
		if !ok {
			return nil
		}
		w = pw
		st.release = append(st.release, end)
	}
	if st.conn = startStream(w); st.conn == nil {
		return nil
	}
	st.retry = newStreamBackoff()
	st.release = append(st.release, st.conn.close, st.retry.stop)
	opened = true
	return st
}

// close undoes what opening the stream did.
func (st *eventStream) close() {
	for i := len(st.release) - 1; i >= 0; i-- {
		st.release[i]()
	}
}

// run serves the stream until ctx is done, its lifetime ends, or a callback
// returns false: it calls onSignal for each change the broker signals, and
// onRefresh when a failed attempt is due again (see retry) or recheck fires
// (a nil recheck never does), and sends heartbeats in between so dead
// connections and idle intermediaries are noticed.
func (st *eventStream) run(ctx context.Context, onSignal, onRefresh func() bool, recheck <-chan time.Time) {
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		var ok bool
		select {
		case <-ctx.Done():
			return
		case <-st.conn.lifetime.C:
			return
		case <-st.signals:
			ok = onSignal()
		case <-recheck:
			ok = onRefresh()
		case <-st.retry.timer.C:
			ok = onRefresh()
		case <-heartbeat.C:
			ok = st.conn.write(": ping\n\n")
		}
		if !ok {
			return
		}
	}
}

// streamConn is an open server-sent event stream.
type streamConn struct {
	w  http.ResponseWriter
	rc *http.ResponseController
	// lifetime fires when the stream has been open long enough (see
	// maxStreamLifetime).
	lifetime *time.Timer
}

// startStream starts a server-sent event response on w (see openStream, which
// starts it once the stream has subscribed to the changes it sends). The request must have been read in
// full: the stream lifts the request's read deadline, and bounds each write
// instead, so a client that stops reading loses its stream rather than
// holding it. It returns nil when the client is gone; otherwise the caller
// closes the stream when it ends.
func startStream(w http.ResponseWriter) *streamConn {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	// Streams end after a while and clients reconnect, which re-checks their
	// credentials (a revoked key or expired session loses its stream) and
	// spreads streams over replicas added since.
	c := &streamConn{w: w, rc: rc, lifetime: time.NewTimer(maxStreamLifetime + rand.N(maxStreamLifetime/4))}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if !c.write("") {
		c.close()
		return nil
	}
	return c
}

// close releases the stream's timers.
func (c *streamConn) close() {
	c.lifetime.Stop()
}

// buffer writes s without flushing it, giving the client streamWriteTimeout
// to take what the write sends. It returns false when the stream should end.
func (c *streamConn) buffer(s string) bool {
	_ = c.rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	_, err := io.WriteString(c.w, s)
	return err == nil
}

// flush sends what was buffered. It returns false when the stream should end.
func (c *streamConn) flush() bool {
	_ = c.rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	return c.rc.Flush() == nil
}

// write writes and flushes s. It returns false when the stream should end.
func (c *streamConn) write(s string) bool {
	return c.buffer(s) && c.flush()
}

// streamBackoff schedules a stream's next attempt after failures to produce
// what it sends (see streamRetryDelay).
type streamBackoff struct {
	// timer fires when the next attempt is due.
	timer *time.Timer
	// failures counts the consecutive failures.
	failures int
}

// newStreamBackoff returns a backoff with nothing scheduled.
func newStreamBackoff() *streamBackoff {
	t := time.NewTimer(0)
	t.Stop()
	return &streamBackoff{timer: t}
}

// fail records a failure and schedules the next attempt, returning its delay.
func (b *streamBackoff) fail() time.Duration {
	b.failures++
	delay := streamRetryDelay(b.failures)
	b.timer.Reset(delay)
	return delay
}

// succeed records a success and cancels any scheduled attempt.
func (b *streamBackoff) succeed() {
	b.failures = 0
	b.timer.Stop()
}

// stop cancels any scheduled attempt.
func (b *streamBackoff) stop() {
	b.timer.Stop()
}

// eventText returns data as one server-sent event, with id rev unless rev is
// 0. Each line of data becomes a data field, which clients join back with
// newlines: a payload spanning lines (a chamber stored pretty-printed, say)
// would otherwise end the event's fields at its first line, and clients would
// drop the rest.
func eventText(event string, rev uint64, data []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "event: %s\n", event)
	if rev != 0 {
		var buf [20]byte
		b.WriteString("id: ")
		b.Write(strconv.AppendUint(buf[:0], rev, 10))
		b.WriteByte('\n')
	}
	text := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(data))
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteByte('\n')
	return b.String()
}

// maxStreamLifetime is how long a watch stream stays open before the server
// closes it (plus up to a quarter more, at random, so streams opened together
// do not all reconnect together).
var maxStreamLifetime = 20 * time.Minute

// streamWriteTimeout bounds each write to a watch stream.
var streamWriteTimeout = 30 * time.Second

// heartbeatInterval is how often a watch stream sends a heartbeat (": ping").
var heartbeatInterval = 30 * time.Second

// streamRetryDelay is how long a stream waits before producing its payload
// again after failures consecutive failures: doubling from one second to 30,
// with jitter so streams on many paths do not retry together.
func streamRetryDelay(failures int) time.Duration {
	d := 30 * time.Second
	if failures <= 5 {
		d = time.Second << (failures - 1)
	}
	return d/2 + rand.N(d/2+1)
}
