package trace

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

func newHttpExporter(ctx context.Context) (trace.SpanExporter, error) {
	return otlptracehttp.New(ctx, otlptracehttp.WithInsecure())
}

func newStdOutExporter() (trace.SpanExporter, error) {
	return stdouttrace.New(
		stdouttrace.WithPrettyPrint())
}

func SetupOtelInstrumentation(ctx context.Context, withStdOut bool) (func(ctx context.Context) error, error) {
	var exp trace.SpanExporter
	var err error
	if withStdOut {
		exp, err = newStdOutExporter()
	} else {
		exp, err = newHttpExporter(ctx)
	}
	if err != nil {
		return nil, err
	}

	prop := newPropagator()
	otel.SetTextMapPropagator(prop)

	tp, err := newTraceProvider(exp)
	if err != nil {
		return nil, err
	}

	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newTraceProvider(traceExporter trace.SpanExporter) (*trace.TracerProvider, error) {
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

	traceProvider := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter,
			// Default is 5s. Set to 1s for demonstrative purposes.
			trace.WithBatchTimeout(5*time.Second)),
		trace.WithResource(r),
	)
	return traceProvider, nil
}
