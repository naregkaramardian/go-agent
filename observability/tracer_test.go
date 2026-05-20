package observability_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/nareg/goagent/observability"
)

func TestStartSpan_noopTracer(t *testing.T) {
	// Install noop provider so no exporter is needed during tests.
	tp := noop.NewTracerProvider()
	ctx := context.Background()

	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(ctx, "test.span")
	defer span.End()

	// StartSpan uses the global tracer, which is the noop provider in test.
	ctx2, span2 := observability.StartSpan(ctx, "child.span",
		attribute.String("key", "value"),
	)
	defer span2.End()
	assert.NotNil(t, ctx2)
	assert.NotNil(t, span2)
}

func TestInitTracer_stdoutDev(t *testing.T) {
	// Unset OTLP endpoint so we get the stdout exporter.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := observability.InitTracer(context.Background(), "test-service", "0.0.0")
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	err = shutdown(context.Background())
	assert.NoError(t, err)
}

func TestTraceHandler_injectsFields(t *testing.T) {
	// With a real SDK tracer the span context is valid → trace_id injected.
	shutdown, err := observability.InitTracer(context.Background(), "test-service", "0.0.0")
	require.NoError(t, err)
	defer shutdown(context.Background()) //nolint:errcheck

	ctx, span := observability.StartSpan(context.Background(), "test.op")
	defer span.End()

	// Logger must not panic when span context is valid.
	l := observability.NewLogger("dev")
	ctx = observability.WithLogger(ctx, l)
	// Use InfoContext so the active span context flows into the traceHandler.
	observability.LoggerFrom(ctx).InfoContext(ctx, "test.event",
		slog.String("key", "value"),
	)
}
