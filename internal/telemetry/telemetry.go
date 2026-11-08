// Package telemetry wires OpenTelemetry tracing into benreadin. Export
// configuration (endpoint, headers, protocol) comes entirely from the
// standard OTEL_EXPORTER_OTLP_* environment variables, which
// otlptracehttp.New reads on its own — nothing to configure here.
package telemetry

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Setup configures a global TracerProvider that exports spans to Honeycomb
// via OTLP/HTTP. It returns a shutdown func that flushes and stops the
// exporter; callers must invoke it before process exit. If
// OTEL_EXPORTER_OTLP_HEADERS is unset (no Honeycomb key configured),
// Setup still installs a real SDK, so spans are created but simply not
// exported — no error, no data.
func Setup(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
		resource.WithFromEnv(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: build resource: %w", err)
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create OTLP exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if os.Getenv("OTEL_EXPORTER_OTLP_HEADERS") == "" {
		return tp.Shutdown, fmt.Errorf("telemetry: OTEL_EXPORTER_OTLP_HEADERS not set — spans will be created but never leave the process")
	}
	return tp.Shutdown, nil
}
