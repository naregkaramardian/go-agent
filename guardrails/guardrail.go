package guardrails

import (
	"context"

	"github.com/nareg/goagent/llm"
)

// AgentStep is the unit that flows through the guardrail chain each iteration.
// The base handler (LLM call) populates Response; guards that run before it
// see a nil Response and short-circuit based on step metadata alone.
type AgentStep struct {
	Number     int                      // step index (0-based)
	TokenCount int                      // conversation buffer token count at step start
	Response   *llm.CompletionResponse  // nil until the base handler populates it
}

// StepHandler executes an agent step and returns the (possibly mutated) step.
type StepHandler func(ctx context.Context, step AgentStep) (AgentStep, error)

// Middleware wraps a StepHandler to inspect, mutate, or short-circuit the step.
// The outermost middleware in a Chain runs first and returns last.
type Middleware func(ctx context.Context, step AgentStep, next StepHandler) (AgentStep, error)

// Chain builds a StepHandler where mw[0] wraps mw[1] wraps … wraps base.
// mw[0] is the outermost layer: first to run, last to return.
func Chain(base StepHandler, mw ...Middleware) StepHandler {
	h := base
	for i := len(mw) - 1; i >= 0; i-- {
		m, inner := mw[i], h
		h = func(ctx context.Context, step AgentStep) (AgentStep, error) {
			return m(ctx, step, inner)
		}
	}
	return h
}
