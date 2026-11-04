package logging

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

type contextKey struct {
	name string
}

var (
	// loggerContextKey is the context key used to store the logger in the context
	loggerContextKey = &contextKey{"realm-logger"}
	// nopLogger is a no-op logger used when no logger is found in the context
	nopLogger = &TracedLogger{Logger: zerolog.Nop()}
)

// TracedLogger wraps zerolog.Logger with context-aware methods
type TracedLogger struct {
	zerolog.Logger
}

// NewTracedLogger creates a logger that writes one JSON object per line to
// standard error, or human-readable lines when standard error is a terminal.
// Set REALM_LOG_FORMAT to "json" or "console" to choose explicitly.
func NewTracedLogger() *TracedLogger {
	return New(defaultWriter())
}

// New creates a logger that writes JSON lines to w, with RFC 3339 timestamps
// and the caller's location.
func New(w io.Writer) *TracedLogger {
	logger := zerolog.New(w).
		With().
		Timestamp().
		Caller().
		Logger()
	return &TracedLogger{Logger: logger}
}

// FromZerolog wraps an existing zerolog logger.
func FromZerolog(l zerolog.Logger) *TracedLogger {
	return &TracedLogger{Logger: l}
}

func defaultWriter() io.Writer {
	switch os.Getenv("REALM_LOG_FORMAT") {
	case "json":
		return os.Stderr
	case "console":
		return zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
	}
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
	}
	return os.Stderr
}

// InfoCtx logs an info message with trace context
func (l *TracedLogger) InfoCtx(ctx context.Context) *zerolog.Event {
	return l.addTraceContext(ctx, l.Info())
}

// WarnCtx logs a warning message with trace context
func (l *TracedLogger) WarnCtx(ctx context.Context) *zerolog.Event {
	return l.addTraceContext(ctx, l.Warn())
}

// ErrorCtx logs an error message with trace context
func (l *TracedLogger) ErrorCtx(ctx context.Context) *zerolog.Event {
	return l.addTraceContext(ctx, l.Error())
}

// DebugCtx logs a debug message with trace context
func (l *TracedLogger) DebugCtx(ctx context.Context) *zerolog.Event {
	return l.addTraceContext(ctx, l.Debug())
}

// addTraceContext adds trace information to a log event
func (l *TracedLogger) addTraceContext(ctx context.Context, event *zerolog.Event) *zerolog.Event {
	spanCtx := trace.SpanContextFromContext(ctx)

	if spanCtx.IsValid() {
		event = event.
			Str("trace_id", spanCtx.TraceID().String()).
			Str("span_id", spanCtx.SpanID().String()).
			Bool("trace_sampled", spanCtx.IsSampled())
	}

	return event
}

// With creates a child logger with additional fields
func (l *TracedLogger) With() zerolog.Context {
	return l.Logger.With()
}

func (l TracedLogger) WithContext(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, loggerContextKey, &l)
	return l.Logger.WithContext(ctx)
}

func Ctx(ctx context.Context) *TracedLogger {
	if l, ok := ctx.Value(loggerContextKey).(*TracedLogger); ok {
		return l
	}
	return nopLogger
}
