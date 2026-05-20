package llm

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

// ObserveLLM returns a Middleware that wraps every Complete call with a span,
// structured log lines, Prometheus metrics, and cost ledger recording.
func ObserveLLM() Middleware {
	return func(ctx context.Context, req *CompletionRequest, next Handler) (*CompletionResponse, error) {
		ctx, span := otel.Tracer("go-agent").Start(ctx, "llm.complete",
			trace.WithAttributes(
				attribute.String("llm.model", req.Model),
				attribute.String("llm.provider", "anthropic"),
				attribute.Int("llm.max_tokens", req.MaxTokens),
			),
		)
		defer span.End()

		log := observability.LoggerFrom(ctx)
		log.DebugContext(ctx, "llm.request.start",
			slog.String("model", req.Model),
			slog.Int("messages", len(req.Messages)),
		)

		start := time.Now()
		resp, err := next(ctx, req)
		dur := time.Since(start)

		status := "ok"
		if err != nil {
			status = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			observability.LLMRequestsTotal.WithLabelValues(req.Model, status).Inc()
			observability.LLMLatency.WithLabelValues(req.Model).Observe(dur.Seconds())
			log.InfoContext(ctx, "llm.request.error",
				slog.String("model", req.Model),
				slog.String("status", status),
				slog.Int64("latency_ms", dur.Milliseconds()),
				slog.String("error", err.Error()),
			)
			return nil, err
		}

		span.SetAttributes(
			attribute.Int64("latency_ms", dur.Milliseconds()),
			attribute.String("status", status),
			attribute.Int("llm.input_tokens", resp.Usage.InputTokens),
			attribute.Int("llm.output_tokens", resp.Usage.OutputTokens),
			attribute.String("llm.stop_reason", string(resp.StopReason)),
		)

		cost := observability.CostLedgerFrom(ctx).Record(req.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)

		observability.LLMRequestsTotal.WithLabelValues(req.Model, status).Inc()
		observability.LLMLatency.WithLabelValues(req.Model).Observe(dur.Seconds())

		log.InfoContext(ctx, "llm.request.complete",
			slog.String("model", req.Model),
			slog.String("status", status),
			slog.Int64("latency_ms", dur.Milliseconds()),
			slog.Int("input_tokens", resp.Usage.InputTokens),
			slog.Int("output_tokens", resp.Usage.OutputTokens),
			slog.String("stop_reason", string(resp.StopReason)),
			slog.Float64("cost_usd", cost),
		)
		log.InfoContext(ctx, "cost.updated",
			slog.String("model", req.Model),
			slog.Float64("cost_usd", cost),
			slog.Float64("total_cost_usd", observability.CostLedgerFrom(ctx).TotalCost()),
		)

		return resp, nil
	}
}
