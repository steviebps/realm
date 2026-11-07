package trace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steviebps/realm/helper/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

func newHttpTraceExporter(ctx context.Context) (trace.SpanExporter, error) {
	return otlptracehttp.New(ctx)
}

func newHttpMetricExporter(ctx context.Context) (metric.Exporter, error) {
	return otlpmetrichttp.New(ctx)
}

func newStdOutExporter() (trace.SpanExporter, error) {
	return stdouttrace.New(
		stdouttrace.WithPrettyPrint())
}

// SetupOtelInstrumentation installs global trace and metric providers that
// export over OTLP/HTTP, configured by the standard OTEL_* environment
// variables (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_SERVICE_NAME,
// OTEL_RESOURCE_ATTRIBUTES, OTEL_TRACES_SAMPLER, …). OTEL_SDK_DISABLED=true
// turns telemetry off. Export errors are logged with the logger in ctx.
func SetupOtelInstrumentation(ctx context.Context, withStdOut bool, version string) (func(ctx context.Context) error, error) {
	var shutdownFuncs []func(context.Context) error
	var err error

	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return func(context.Context) error { return nil }, nil
	}
	logger := logging.Ctx(ctx)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.WarnCtx(ctx).Str("error", err.Error()).Msg("telemetry export failed")
	}))

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
		exp, err = newHttpTraceExporter(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("creating exporter: %w", err)
	}
	// Later detectors win, so OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES
	// override the defaults. The attributes are schemaless so they merge under
	// the SDK's schema URL; pairing them with semconv.SchemaURL would fail at
	// startup whenever the SDK and this semconv import disagree on versions.
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("realm"), semconv.ServiceVersion(version)),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating telemetry resource: %w", err)
	}

	prop := newPropagator()
	otel.SetTextMapPropagator(prop)

	tp, err := newTraceProvider(exp, res)
	if err != nil {
		return shutdown, fmt.Errorf("creating trace provider: %w", err)
	}

	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)
	otel.SetTracerProvider(tp)

	if withStdOut {
		// Only traces are printed: metrics have no OTLP collector to go to,
		// and exporting them would fail (and warn) every minute.
		return shutdown, nil
	}
	meterProvider, err := newMeterProvider(ctx, res)
	if err != nil {
		return shutdown, fmt.Errorf("creating meter provider: %w", err)
	}

	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	return shutdown, nil
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newTraceProvider(traceExporter trace.SpanExporter, res *resource.Resource) (*trace.TracerProvider, error) {
	traceProvider := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter,
			trace.WithBatchTimeout(5*time.Second)),
		trace.WithResource(res),
	)
	return traceProvider, nil
}

func newMeterProvider(ctx context.Context, res *resource.Resource) (*metric.MeterProvider, error) {
	metricExporter, err := newHttpMetricExporter(ctx)
	if err != nil {
		return nil, err
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter,
			metric.WithInterval(1*time.Minute))),
	)
	return meterProvider, nil
}
