package observability

import (
	"context"
	"fmt"
	"io"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// InitTracer configures the global OTel TracerProvider.
//
// Exporter selection (first match wins):
//   OTEL_EXPORTER_OTLP_ENDPOINT set → OTLP HTTP exporter (production)
//   spanWriter != nil              → stdout exporter writing to spanWriter (dev/verbose)
//   neither                        → no-op exporter (default; keeps the CLI clean)
//
// The caller must invoke the returned shutdown function before process exit.
func InitTracer(ctx context.Context, serviceName, version string, spanWriter ...io.Writer) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("observability.InitTracer: build resource: %w", err)
	}

	var exporter sdktrace.SpanExporter
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		exporter, err = otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(endpoint),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("observability.InitTracer: OTLP exporter: %w", err)
		}
	} else if len(spanWriter) > 0 && spanWriter[0] != nil {
		exporter, err = stdouttrace.New(
			stdouttrace.WithWriter(spanWriter[0]),
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			return nil, fmt.Errorf("observability.InitTracer: stdout exporter: %w", err)
		}
	} else {
		// No destination configured — discard all spans silently.
		exporter = noopExporter{}
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// StartSpan is a convenience wrapper around the global "go-agent" tracer.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer("go-agent").Start(ctx, name, trace.WithAttributes(attrs...))
}

// noopExporter silently discards all spans.
type noopExporter struct{}

func (noopExporter) ExportSpans(_ context.Context, _ []sdktrace.ReadOnlySpan) error { return nil }
func (noopExporter) Shutdown(_ context.Context) error                                { return nil }
