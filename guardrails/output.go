package guardrails

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nareg/goagent/observability"
)

// RequireStructuredOutput blocks when the LLM response content is not valid JSON.
// The schema argument is reserved for future schema-level validation; pass nil
// to enforce JSON validity only.
// The check is skipped when the model responded with tool calls instead of text,
// since the tool-call payload is already structured by the provider.
func RequireStructuredOutput(_ json.RawMessage) Middleware {
	return func(ctx context.Context, step AgentStep, next StepHandler) (AgentStep, error) {
		s, err := next(ctx, step)
		if err != nil {
			return s, err
		}
		if s.Response == nil {
			return s, nil
		}
		// Structured-output check only applies to text responses.
		if len(s.Response.ToolCalls) > 0 {
			return s, nil
		}
		if !json.Valid([]byte(s.Response.Content)) {
			log := observability.LoggerFrom(ctx)
			observability.GuardrailTriggersTotal.WithLabelValues("structured_output", "blocked").Inc()
			log.WarnContext(ctx, "guardrail.triggered",
				slog.String("guardrail", "structured_output"),
				slog.String("action", "blocked"),
				slog.String("content_preview", observability.Truncate(s.Response.Content, 100)),
				slog.String("status", "error"),
			)
			return s, fmt.Errorf("guardrails.RequireStructuredOutput: response is not valid JSON")
		}
		return s, nil
	}
}
