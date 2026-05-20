package observability_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/nareg/goagent/observability"
)

func TestCostLedger_Record_sonnet(t *testing.T) {
	l := observability.NewCostLedger()
	// 1M input @ $3/M + 1M output @ $15/M = $18
	cost := l.Record("claude-sonnet-4-5", 1_000_000, 1_000_000)
	assert.InDelta(t, 18.00, cost, 0.001)
}

func TestCostLedger_Record_unknown_model(t *testing.T) {
	l := observability.NewCostLedger()
	// Unknown model has zero pricing; cost must be 0, not panic.
	cost := l.Record("unknown-model", 1_000_000, 1_000_000)
	assert.Equal(t, 0.0, cost)
}

func TestCostLedger_Summary(t *testing.T) {
	l := observability.NewCostLedger()
	l.Record("claude-sonnet-4-5", 1_000_000, 0)
	l.Record("claude-sonnet-4-5", 0, 1_000_000)

	summary := l.Summary()
	assert.InDelta(t, 18.00, summary.TotalCostUSD, 0.001)

	usage := summary.ByModel["claude-sonnet-4-5"]
	assert.Equal(t, int64(1_000_000), usage.InputTokens)
	assert.Equal(t, int64(1_000_000), usage.OutputTokens)
}

func TestCostLedger_TotalCost(t *testing.T) {
	l := observability.NewCostLedger()
	l.Record("claude-haiku-4-5", 1_000_000, 1_000_000)
	// $0.80 + $4.00 = $4.80
	assert.InDelta(t, 4.80, l.TotalCost(), 0.001)
}

func TestCostLedger_ContextRoundtrip(t *testing.T) {
	l := observability.NewCostLedger()
	ctx := observability.WithCostLedger(context.Background(), l)
	got := observability.CostLedgerFrom(ctx)
	assert.Equal(t, l, got)
}

func TestCostLedgerFrom_fallback(t *testing.T) {
	// Must return a non-nil ledger even when ctx carries none.
	got := observability.CostLedgerFrom(context.Background())
	assert.NotNil(t, got)
}

func TestCostLedger_Concurrent(t *testing.T) {
	l := observability.NewCostLedger()
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			l.Record("claude-sonnet-4-5", 100, 50)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	assert.InDelta(t, 20*l.Record("claude-sonnet-4-5", 0, 0), 0, 0.001)
	assert.Equal(t, int64(20*100), l.Summary().ByModel["claude-sonnet-4-5"].InputTokens)
}
