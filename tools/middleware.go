package tools

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nareg/goagent/observability"
)

// ObserveTool returns a Middleware that wraps every Dispatch call with an OTel
// span, structured log lines (tool.call.start / tool.call.complete), and
// Prometheus metrics.
func ObserveTool() Middleware {
	return func(ctx context.Context, call ToolCall, next Handler) ToolResult {
		ctx, span := otel.Tracer("go-agent").Start(ctx, "tool.call",
			trace.WithAttributes(
				attribute.String("tool.name", call.Name),
				attribute.String("tool.call_id", call.ID),
			),
		)
		defer span.End()

		log := observability.LoggerFrom(ctx)
		log.DebugContext(ctx, "tool.call.start",
			slog.String("tool", call.Name),
			slog.String("input", observability.Truncate(string(call.Input), 200)),
		)

		start := time.Now()
		result := next(ctx, call)
		dur := time.Since(start)

		status := "ok"
		if result.Error != nil {
			status = "error"
			span.RecordError(result.Error)
			span.SetStatus(codes.Error, result.Error.Error())
			observability.ToolErrorsTotal.WithLabelValues(call.Name).Inc()
		}

		span.SetAttributes(
			attribute.Int64("latency_ms", dur.Milliseconds()),
			attribute.String("status", status),
		)
		observability.ToolCallsTotal.WithLabelValues(call.Name, status).Inc()
		observability.ToolLatency.WithLabelValues(call.Name).Observe(dur.Seconds())

		log.InfoContext(ctx, "tool.call.complete",
			slog.String("tool", call.Name),
			slog.String("status", status),
			slog.Int64("latency_ms", dur.Milliseconds()),
			slog.String("output", observability.Truncate(string(result.Output), 200)),
		)
		return result
	}
}
