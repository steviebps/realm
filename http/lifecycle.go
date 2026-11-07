package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/steviebps/realm/helper/logging"
	"go.opentelemetry.io/otel/trace"
)

// RequestIDHeader carries the ID of a request. The server echoes the ID a
// proxy sent, or makes one, and logs it with the request, so a response can
// be matched to the server's log lines.
const RequestIDHeader = "X-Request-Id"

// responseWriteGrace is how much longer than the request timeout a client may
// take to receive a response. It is generous so that slow links can still
// load the UI or a large chamber.
const responseWriteGrace = 50 * time.Second

// readFailure returns the status and error to answer a request whose body
// could not be read with: 408 when it did not arrive in time, 413 when it was
// too large, else 400.
func readFailure(err error) (int, error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return http.StatusRequestTimeout, errors.New("the request body did not arrive in time")
	case errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge, fmt.Errorf("request body must be at most %d bytes", tooLarge.Limit)
	}
	return http.StatusBadRequest, err
}

// requestInfo is what the request lifecycle learns about a request from the
// handlers it wraps, for the access log.
type requestInfo struct {
	id string
	// actor names the caller once it is identified (see withPrincipal).
	actor string
	// err says why the request failed, when a handler said (see
	// noteFailure).
	err string
	// span is the request's span, when it is traced.
	span trace.SpanContext
}

type requestInfoKey struct{}

// requestInfoFrom returns the lifecycle record of the request ctx belongs to,
// or nil outside withLifecycle.
func requestInfoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(requestInfoKey{}).(*requestInfo)
	return info
}

// withLifecycle wraps the whole handler chain. It bounds how long a client
// may take to send its request and to read the response, gives every request
// an ID and a logger that carries it, recovers panics, and logs each request
// when it completes.
//
// net/http enforces no deadline on reading a request body or writing a
// response unless the server sets ReadTimeout or WriteTimeout, which would
// also cut off watch streams. Without one, a client that trickles its body or
// stops reading the response holds the connection and a goroutine forever,
// so the deadlines are set per request here instead: watch streams clear the
// read deadline once their request is read and bound each write (see
// streamEvents).
func withLifecycle(h http.Handler, logger *logging.TracedLogger, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(start.Add(timeout))
		if isWatchRequest(r) {
			// clear a deadline left by an earlier request on this connection
			_ = rc.SetWriteDeadline(time.Time{})
		} else {
			_ = rc.SetWriteDeadline(start.Add(timeout + responseWriteGrace))
		}

		info := &requestInfo{id: requestID(r)}
		w.Header().Set(RequestIDHeader, info.id)
		reqLogger := logging.FromZerolog(logger.With().Str("request_id", info.id).Logger())
		ctx := context.WithValue(reqLogger.WithContext(r.Context()), requestInfoKey{}, info)
		rec := &responseRecorder{ResponseWriter: w}

		defer func() {
			v := recover()
			if v == http.ErrAbortHandler {
				panic(v)
			}
			// whether the response was under way when the handler panicked
			underway := rec.status != 0
			if v != nil {
				reqLogger.ErrorCtx(ctx).Str("panic", fmt.Sprint(v)).Str("stack", string(debug.Stack())).Msg("panic serving request")
				if !underway {
					handleError(ctx, rec, http.StatusInternalServerError, createResponseWithErrors(nil, nil))
				}
			}
			logAccess(reqLogger, r, info, rec, time.Since(start))
			if v != nil && underway {
				// Abort the response, so the client sees it broken rather
				// than complete.
				panic(http.ErrAbortHandler)
			}
		}()
		h.ServeHTTP(rec, r.WithContext(ctx))
	})
}

// maxRequestIDLength bounds the request IDs accepted from clients.
const maxRequestIDLength = 128

// requestID returns the ID a proxy assigned to r, when it is safe to log and
// echo, or a new one.
func requestID(r *http.Request) string {
	if id := r.Header.Get(RequestIDHeader); id != "" && len(id) <= maxRequestIDLength && isTokenSafe(id) {
		return id
	}
	return uuid.NewString()
}

// isTokenSafe reports whether s holds only letters, digits, and - _ . : / + =.
func isTokenSafe(s string) bool {
	for _, c := range []byte(s) {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-', c == '_', c == '.', c == ':', c == '/', c == '+', c == '=':
		default:
			return false
		}
	}
	return true
}

// noteFailure records why a request failed, for its access log line, and
// logs server errors (5xx) in full. Client errors are not logged separately:
// the access log line, with this reason, covers them, so a client's mistakes
// (polling a chamber that does not exist yet, say) never read as errors.
func noteFailure(ctx context.Context, status int, err error) {
	if info := requestInfoFrom(ctx); info != nil {
		info.err = err.Error()
	}
	if status >= http.StatusInternalServerError {
		logging.Ctx(ctx).ErrorCtx(ctx).Int("status", status).Str("error", err.Error()).Msg("request failed")
	}
}

// isChange reports whether r asks to change something. Evaluation is a POST
// but reads.
func isChange(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return !strings.HasPrefix(r.URL.Path, evaluatePrefix)
}

// logAccess logs a completed request. Requests that matter to operators are
// logged at higher levels: server errors as errors, refused credentials and
// permissions as warnings, and changes as info. Reads, which SDKs and
// browsers make constantly (evaluations included), and probes are logged at
// debug.
func logAccess(logger *logging.TracedLogger, r *http.Request, info *requestInfo, rec *responseRecorder, elapsed time.Duration) {
	status := rec.status
	if status == 0 {
		// the handler wrote nothing, which net/http answers with 200
		status = http.StatusOK
	}
	var event *zerolog.Event
	switch {
	case status >= http.StatusInternalServerError:
		event = logger.Error()
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusTooManyRequests:
		event = logger.Warn()
	case isChange(r):
		event = logger.Info()
	default:
		event = logger.Debug()
	}
	if !event.Enabled() {
		return
	}
	if info.span.IsValid() {
		event = event.Str("trace_id", info.span.TraceID().String()).Str("span_id", info.span.SpanID().String())
	}
	if info.actor != "" {
		event = event.Str("actor", info.actor)
	}
	if info.err != "" {
		event = event.Str("error", info.err)
	}
	event.
		Str("method", r.Method).
		Str("route", routeOf(r.URL.Path)).
		Str("path", r.URL.Path).
		Int("status", status).
		Int64("bytes", rec.bytes).
		Float64("duration_ms", float64(elapsed.Microseconds())/1000).
		Str("remote_addr", r.RemoteAddr).
		Str("user_agent", r.UserAgent()).
		Msg("request")
}

// responseRecorder records the status and size of a response. It passes
// flushes through, with their errors, for watch streams, and unwraps for
// http.ResponseController.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rec *responseRecorder) WriteHeader(status int) {
	if rec.status == 0 && status >= http.StatusOK {
		rec.status = status
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *responseRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += int64(n)
	return n, err
}

func (rec *responseRecorder) Flush() { _ = rec.FlushError() }

// FlushError flushes the response, reporting a client that cannot take it
// (http.ResponseController.Flush calls it).
func (rec *responseRecorder) FlushError() error {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return http.NewResponseController(rec.ResponseWriter).Flush()
}

func (rec *responseRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }
