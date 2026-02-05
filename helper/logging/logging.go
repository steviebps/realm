package logging

import (
	"context"
	"os"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

type contextKey struct {
	name string
}

var (
	loggerContextKey = &contextKey{"realm-logger"}
)

// TracedLogger wraps zerolog.Logger with context-aware methods
type TracedLogger struct {
	zerolog.Logger
}

// NewTracedLogger creates a new traced logger
func NewTracedLogger() *TracedLogger {
	zerolog.TimeFieldFormat = time.RFC3339Nano

	consoleWriter := zerolog.NewConsoleWriter()
	multi := zerolog.MultiLevelWriter(consoleWriter, os.Stderr)

	logger := zerolog.New(multi).
		With().
		Timestamp().
		Caller().
		Logger()

	return &TracedLogger{Logger: logger}
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
		spanCtx.TraceFlags()
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
	return nil
}
