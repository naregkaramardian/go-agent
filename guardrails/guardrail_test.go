package guardrails_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/guardrails"
	"github.com/nareg/goagent/llm"
)

// noopHandler returns the step unchanged.
var noopHandler guardrails.StepHandler = func(_ context.Context, s guardrails.AgentStep) (guardrails.AgentStep, error) {
	return s, nil
}

// responseHandler returns a step with the given response set.
func responseHandler(resp *llm.CompletionResponse) guardrails.StepHandler {
	return func(_ context.Context, s guardrails.AgentStep) (guardrails.AgentStep, error) {
		s.Response = resp
		return s, nil
	}
}

func TestChain_emptyMiddleware(t *testing.T) {
	called := false
	base := guardrails.StepHandler(func(_ context.Context, s guardrails.AgentStep) (guardrails.AgentStep, error) {
		called = true
		return s, nil
	})
	h := guardrails.Chain(base)
	_, err := h(context.Background(), guardrails.AgentStep{})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestChain_order(t *testing.T) {
	var order []string

	base := guardrails.StepHandler(func(_ context.Context, s guardrails.AgentStep) (guardrails.AgentStep, error) {
		order = append(order, "base")
		return s, nil
	})
	mw0 := guardrails.Middleware(func(ctx context.Context, s guardrails.AgentStep, next guardrails.StepHandler) (guardrails.AgentStep, error) {
		order = append(order, "mw0-before")
		s, err := next(ctx, s)
		order = append(order, "mw0-after")
		return s, err
	})
	mw1 := guardrails.Middleware(func(ctx context.Context, s guardrails.AgentStep, next guardrails.StepHandler) (guardrails.AgentStep, error) {
		order = append(order, "mw1-before")
		s, err := next(ctx, s)
		order = append(order, "mw1-after")
		return s, err
	})

	h := guardrails.Chain(base, mw0, mw1)
	_, err := h(context.Background(), guardrails.AgentStep{})
	require.NoError(t, err)
	assert.Equal(t, []string{"mw0-before", "mw1-before", "base", "mw1-after", "mw0-after"}, order)
}

func TestChain_shortCircuit(t *testing.T) {
	baseCalled := false
	base := guardrails.StepHandler(func(_ context.Context, s guardrails.AgentStep) (guardrails.AgentStep, error) {
		baseCalled = true
		return s, nil
	})
	blocker := guardrails.Middleware(func(_ context.Context, s guardrails.AgentStep, _ guardrails.StepHandler) (guardrails.AgentStep, error) {
		return s, errors.New("blocked")
	})

	h := guardrails.Chain(base, blocker)
	_, err := h(context.Background(), guardrails.AgentStep{})
	require.Error(t, err)
	assert.False(t, baseCalled, "base should not be called when middleware short-circuits")
}

func TestChain_errorPropagatesUpward(t *testing.T) {
	want := errors.New("inner error")
	base := guardrails.StepHandler(func(_ context.Context, s guardrails.AgentStep) (guardrails.AgentStep, error) {
		return s, want
	})
	passThrough := guardrails.Middleware(func(ctx context.Context, s guardrails.AgentStep, next guardrails.StepHandler) (guardrails.AgentStep, error) {
		return next(ctx, s)
	})

	h := guardrails.Chain(base, passThrough)
	_, err := h(context.Background(), guardrails.AgentStep{})
	assert.ErrorIs(t, err, want)
}
