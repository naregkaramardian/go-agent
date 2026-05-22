package guardrails

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nareg/goagent/observability"
)

// MaxSteps blocks before the LLM call when step.Number >= limit.
func MaxSteps(limit int) Middleware {
	return func(ctx context.Context, step AgentStep, next StepHandler) (AgentStep, error) {
		if step.Number >= limit {
			log := observability.LoggerFrom(ctx)
			observability.GuardrailTriggersTotal.WithLabelValues("max_steps", "blocked").Inc()
			log.WarnContext(ctx, "guardrail.triggered",
				slog.String("guardrail", "max_steps"),
				slog.String("action", "blocked"),
				slog.Int("step", step.Number),
				slog.Int("limit", limit),
				slog.String("status", "error"),
			)
			return step, fmt.Errorf("guardrails.MaxSteps: reached step limit %d", limit)
		}
		return next(ctx, step)
	}
}

// TokenBudget warns at 80% of limit and blocks at 100%.
// Reads step.TokenCount which the agent populates from the buffer before the chain runs.
func TokenBudget(limit int) Middleware {
	return func(ctx context.Context, step AgentStep, next StepHandler) (AgentStep, error) {
		if limit <= 0 {
			return next(ctx, step)
		}
		pct := float64(step.TokenCount) / float64(limit)
		log := observability.LoggerFrom(ctx)

		if pct >= 1.0 {
			observability.GuardrailTriggersTotal.WithLabelValues("token_budget", "blocked").Inc()
			log.WarnContext(ctx, "budget.exceeded",
				slog.String("guardrail", "token_budget"),
				slog.String("action", "blocked"),
				slog.Int("tokens", step.TokenCount),
				slog.Int("limit", limit),
				slog.String("status", "error"),
			)
			return step, fmt.Errorf("guardrails.TokenBudget: token count %d exceeds limit %d", step.TokenCount, limit)
		}

		if pct >= 0.8 {
			observability.GuardrailTriggersTotal.WithLabelValues("token_budget", "warned").Inc()
			log.WarnContext(ctx, "budget.warning",
				slog.String("guardrail", "token_budget"),
				slog.String("action", "warned"),
				slog.Int("tokens", step.TokenCount),
				slog.Int("limit", limit),
				slog.Float64("pct", pct*100),
				slog.String("status", "ok"),
			)
		}

		return next(ctx, step)
	}
}

// CostBudget reads the CostLedger from ctx; warns at 80% of limitUSD, blocks at 100%.
func CostBudget(limitUSD float64) Middleware {
	return func(ctx context.Context, step AgentStep, next StepHandler) (AgentStep, error) {
		if limitUSD <= 0 {
			return next(ctx, step)
		}
		current := observability.CostLedgerFrom(ctx).TotalCost()
		pct := current / limitUSD
		log := observability.LoggerFrom(ctx)

		if pct >= 1.0 {
			observability.GuardrailTriggersTotal.WithLabelValues("cost_budget", "blocked").Inc()
			log.WarnContext(ctx, "budget.exceeded",
				slog.String("guardrail", "cost_budget"),
				slog.String("action", "blocked"),
				slog.Float64("cost_usd", current),
				slog.Float64("limit_usd", limitUSD),
				slog.String("status", "error"),
			)
			return step, fmt.Errorf("guardrails.CostBudget: cost $%.4f exceeds limit $%.4f", current, limitUSD)
		}

		if pct >= 0.8 {
			observability.GuardrailTriggersTotal.WithLabelValues("cost_budget", "warned").Inc()
			log.WarnContext(ctx, "budget.warning",
				slog.String("guardrail", "cost_budget"),
				slog.String("action", "warned"),
				slog.Float64("cost_usd", current),
				slog.Float64("limit_usd", limitUSD),
				slog.Float64("pct", pct*100),
				slog.String("status", "ok"),
			)
		}

		return next(ctx, step)
	}
}
