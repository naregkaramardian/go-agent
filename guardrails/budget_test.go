package guardrails_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/guardrails"
	"github.com/nareg/goagent/observability"
)

func testCtx() context.Context {
	ctx := context.Background()
	ctx = observability.WithLogger(ctx, observability.NewLogger("dev"))
	ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())
	return ctx
}

// --- MaxSteps ---

func TestMaxSteps_passes_belowLimit(t *testing.T) {
	h := guardrails.Chain(noopHandler, guardrails.MaxSteps(5))
	_, err := h(testCtx(), guardrails.AgentStep{Number: 3})
	require.NoError(t, err)
}

func TestMaxSteps_blocks_atLimit(t *testing.T) {
	h := guardrails.Chain(noopHandler, guardrails.MaxSteps(5))
	_, err := h(testCtx(), guardrails.AgentStep{Number: 5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxSteps")
}

func TestMaxSteps_blocks_aboveLimit(t *testing.T) {
	h := guardrails.Chain(noopHandler, guardrails.MaxSteps(5))
	_, err := h(testCtx(), guardrails.AgentStep{Number: 10})
	require.Error(t, err)
}

// --- TokenBudget ---

func TestTokenBudget_passes_wellUnder(t *testing.T) {
	h := guardrails.Chain(noopHandler, guardrails.TokenBudget(1000))
	_, err := h(testCtx(), guardrails.AgentStep{TokenCount: 100})
	require.NoError(t, err)
}

func TestTokenBudget_warns_at80pct(t *testing.T) {
	// 800/1000 = 80% — should warn but not block.
	h := guardrails.Chain(noopHandler, guardrails.TokenBudget(1000))
	_, err := h(testCtx(), guardrails.AgentStep{TokenCount: 800})
	require.NoError(t, err) // warn but continue
}

func TestTokenBudget_blocks_at100pct(t *testing.T) {
	h := guardrails.Chain(noopHandler, guardrails.TokenBudget(1000))
	_, err := h(testCtx(), guardrails.AgentStep{TokenCount: 1000})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TokenBudget")
}

func TestTokenBudget_blocks_over100pct(t *testing.T) {
	h := guardrails.Chain(noopHandler, guardrails.TokenBudget(1000))
	_, err := h(testCtx(), guardrails.AgentStep{TokenCount: 1500})
	require.Error(t, err)
}

func TestTokenBudget_zeroLimit_noOp(t *testing.T) {
	h := guardrails.Chain(noopHandler, guardrails.TokenBudget(0))
	_, err := h(testCtx(), guardrails.AgentStep{TokenCount: 999999})
	require.NoError(t, err)
}

// --- CostBudget ---

func TestCostBudget_passes_underLimit(t *testing.T) {
	ctx := testCtx()
	observability.CostLedgerFrom(ctx).Record("claude-haiku-4-5", 100, 100)

	h := guardrails.Chain(noopHandler, guardrails.CostBudget(10.0))
	_, err := h(ctx, guardrails.AgentStep{})
	require.NoError(t, err)
}

func TestCostBudget_warns_at80pct(t *testing.T) {
	ctx := testCtx()
	// claude-haiku-4-5: $0.80/1M input + $4.00/1M output
	// Record enough to hit ~80% of $0.001 limit: ~800K tokens at haiku input rate.
	// Easier: record a cost directly at 80% threshold.
	// $0.001 limit, 80% = $0.0008. Record 1M input tokens = $0.80 ≠ that.
	// Use a cost we can control: set limit high enough that recorded cost is ~80%.
	// Record 800K input tokens at haiku rate = $0.64. Set limit = $0.80.
	observability.CostLedgerFrom(ctx).Record("claude-haiku-4-5", 800_000, 0)

	h := guardrails.Chain(noopHandler, guardrails.CostBudget(0.80))
	_, err := h(ctx, guardrails.AgentStep{})
	require.NoError(t, err) // warn but continue
}

func TestCostBudget_blocks_atLimit(t *testing.T) {
	ctx := testCtx()
	// Record 1M input tokens at haiku rate = $0.80. Set limit = $0.80.
	observability.CostLedgerFrom(ctx).Record("claude-haiku-4-5", 1_000_000, 0)

	h := guardrails.Chain(noopHandler, guardrails.CostBudget(0.80))
	_, err := h(ctx, guardrails.AgentStep{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CostBudget")
}

func TestCostBudget_zeroLimit_noOp(t *testing.T) {
	ctx := testCtx()
	observability.CostLedgerFrom(ctx).Record("claude-haiku-4-5", 999_999_999, 0)

	h := guardrails.Chain(noopHandler, guardrails.CostBudget(0))
	_, err := h(ctx, guardrails.AgentStep{})
	require.NoError(t, err)
}
