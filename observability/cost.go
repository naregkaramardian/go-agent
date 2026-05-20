package observability

import (
	"context"
	"sync"
)

// TokenPrice is the USD cost per 1 million tokens.
type TokenPrice struct {
	Input  float64
	Output float64
}

// ModelPricing maps model IDs to their token prices (USD per 1M tokens).
// Update this table when Anthropic changes pricing.
var ModelPricing = map[string]TokenPrice{
	"claude-opus-4-5":           {Input: 15.00, Output: 75.00},
	"claude-sonnet-4-5":         {Input: 3.00, Output: 15.00},
	"claude-haiku-4-5":          {Input: 0.80, Output: 4.00},
	"claude-haiku-4-5-20251001": {Input: 0.80, Output: 4.00},
	"claude-opus-4-7":           {Input: 15.00, Output: 75.00},
	"claude-sonnet-4-6":         {Input: 3.00, Output: 15.00},
}

// CostSummary holds aggregate token and cost totals across all models.
type CostSummary struct {
	ByModel      map[string]ModelUsage
	TotalCostUSD float64
}

// ModelUsage holds per-model aggregate stats.
type ModelUsage struct {
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// CostLedger accumulates token usage and computes USD costs. Thread-safe.
type CostLedger struct {
	mu           sync.Mutex
	inputTokens  map[string]int64
	outputTokens map[string]int64
	costUSD      map[string]float64
}

// NewCostLedger returns an initialised CostLedger.
func NewCostLedger() *CostLedger {
	return &CostLedger{
		inputTokens:  make(map[string]int64),
		outputTokens: make(map[string]int64),
		costUSD:      make(map[string]float64),
	}
}

// Record adds a usage event and returns the USD cost for this call.
func (l *CostLedger) Record(model string, inputTok, outputTok int) float64 {
	p := ModelPricing[model]
	cost := float64(inputTok)/1e6*p.Input + float64(outputTok)/1e6*p.Output

	l.mu.Lock()
	l.inputTokens[model] += int64(inputTok)
	l.outputTokens[model] += int64(outputTok)
	l.costUSD[model] += cost
	l.mu.Unlock()

	LLMTokensTotal.WithLabelValues(model, "input").Add(float64(inputTok))
	LLMTokensTotal.WithLabelValues(model, "output").Add(float64(outputTok))
	LLMCostUSDTotal.WithLabelValues(model).Add(cost)

	return cost
}

// TotalCost returns the cumulative USD cost across all models.
func (l *CostLedger) TotalCost() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var total float64
	for _, c := range l.costUSD {
		total += c
	}
	return total
}

// Summary returns per-model and aggregate usage.
func (l *CostLedger) Summary() CostSummary {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := CostSummary{ByModel: make(map[string]ModelUsage)}
	for model, in := range l.inputTokens {
		s.ByModel[model] = ModelUsage{
			InputTokens:  in,
			OutputTokens: l.outputTokens[model],
			CostUSD:      l.costUSD[model],
		}
		s.TotalCostUSD += l.costUSD[model]
	}
	return s
}

// WithCostLedger stores l in ctx.
func WithCostLedger(ctx context.Context, l *CostLedger) context.Context {
	return context.WithValue(ctx, costLedgerKey, l)
}

// CostLedgerFrom retrieves the CostLedger from ctx.
// Returns a fresh no-op ledger if none is stored; never returns nil.
func CostLedgerFrom(ctx context.Context) *CostLedger {
	if l, ok := ctx.Value(costLedgerKey).(*CostLedger); ok {
		return l
	}
	return NewCostLedger()
}
