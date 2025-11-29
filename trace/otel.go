package trace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func newHttpExporter(ctx context.Context) (trace.SpanExporter, error) {
	return otlptracehttp.New(ctx, otlptracehttp.WithInsecure())
}

func newStdOutExporter() (trace.SpanExporter, error) {
	return stdouttrace.New(
		stdouttrace.WithPrettyPrint())
}

func SetupOtelInstrumentation(ctx context.Context, withStdOut bool) (func(ctx context.Context) error, error) {
	var shutdownFuncs []func(context.Context) error
	var err error

	shutdown := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	var exp trace.SpanExporter

	if withStdOut {
		exp, err = newStdOutExporter()
	} else {
		exp, err = newHttpExporter(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("creating exporter: %w", err)
	}

	r, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("realm"),
		),
	)
	if err != nil {
		return nil, err
	}

	prop := newPropagator()
	otel.SetTextMapPropagator(prop)

	tp, err := newTraceProvider(exp, r)
	if err != nil {
		return shutdown, fmt.Errorf("creating trace provider: %w", err)
	}

	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)
	otel.SetTracerProvider(tp)

	// Set up logger provider.
	loggerProvider, err := newLoggerProvider(r)
	if err != nil {
		return shutdown, fmt.Errorf("creating logger provider: %w", err)
	}
	shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
	global.SetLoggerProvider(loggerProvider)

	return shutdown, nil
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newTraceProvider(traceExporter trace.SpanExporter, r *resource.Resource) (*trace.TracerProvider, error) {
	traceProvider := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter,
			// Default is 5s
			trace.WithBatchTimeout(5*time.Second)),
		trace.WithResource(r),
	)
	return traceProvider, nil
}

func newLoggerProvider(r *resource.Resource) (*log.LoggerProvider, error) {
	logExporter, err := stdoutlog.New(stdoutlog.WithPrettyPrint())
	if err != nil {
		return nil, err
	}

	loggerProvider := log.NewLoggerProvider(
		log.WithProcessor(log.NewBatchProcessor(logExporter)),
		log.WithResource(r),
	)
	return loggerProvider, nil
}
