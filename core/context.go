package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/nareg/goagent/llm"
	"github.com/nareg/goagent/observability"
)

// ConversationBuffer holds the message history bounded by a token budget.
// When the buffer exceeds the budget, TrimToFit evicts oldest messages (FIFO),
// optionally summarising them via a cheap LLM call first.
type ConversationBuffer struct {
	mu          sync.Mutex
	messages    []llm.Message
	tokenBudget int
	tokenCount  int
	summarizer  llm.LLMClient // nil = bare eviction
	sumModel    string
}

// NewConversationBuffer returns an empty buffer bounded by tokenBudget.
func NewConversationBuffer(tokenBudget int) *ConversationBuffer {
	return &ConversationBuffer{tokenBudget: tokenBudget}
}

// WithSummarizer attaches an LLM client used to summarise evicted messages
// instead of dropping them silently.
func (b *ConversationBuffer) WithSummarizer(client llm.LLMClient, model string) *ConversationBuffer {
	b.summarizer = client
	b.sumModel = model
	return b
}

// Add appends a message and updates the token gauge.
func (b *ConversationBuffer) Add(_ context.Context, msg llm.Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = append(b.messages, msg)
	b.tokenCount += estimateTokens(msg)
	observability.ContextCurrentTokens.Set(float64(b.tokenCount))
}

// Messages returns a snapshot of the current message slice.
func (b *ConversationBuffer) Messages() []llm.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]llm.Message, len(b.messages))
	copy(out, b.messages)
	return out
}

// TokenCount returns the current estimated token count.
func (b *ConversationBuffer) TokenCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokenCount
}

// TrimToFit evicts the oldest messages until the buffer fits within its budget.
// If a summarizer is configured it first compresses the evicted segment into a
// synthetic summary message. Safe to call from the agent loop; the mutex ensures
// concurrent Add/Messages calls remain consistent.
func (b *ConversationBuffer) TrimToFit(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.tokenCount <= b.tokenBudget {
		return nil
	}

	ctx, span := otel.Tracer("go-agent").Start(ctx, "context.evict",
		trace.WithAttributes(
			attribute.Int("tokens.before", b.tokenCount),
			attribute.Int("budget", b.tokenBudget),
		),
	)
	defer span.End()

	log := observability.LoggerFrom(ctx)
	tokensBefore := b.tokenCount

	// Always preserve the last 2 messages (most recent user + assistant turn).
	keepFrom := len(b.messages) - 2
	if keepFrom < 0 {
		keepFrom = 0
	}

	// Walk forward until the budget fits or we've reached the keep boundary.
	evictUntil := 0
	running := b.tokenCount
	for evictUntil < keepFrom && running > b.tokenBudget {
		running -= estimateTokens(b.messages[evictUntil])
		evictUntil++
	}
	if evictUntil == 0 {
		return nil
	}

	if b.summarizer != nil {
		summary, err := b.summarise(ctx, b.messages[:evictUntil])
		if err != nil {
			log.InfoContext(ctx, "context.compaction.error",
				slog.String("error", err.Error()),
				slog.String("status", "error"),
			)
			// Fall through to bare eviction.
		} else {
			summaryMsg := llm.Message{
				Role:    llm.RoleUser,
				Content: "[Summary of earlier conversation]: " + summary,
			}
			b.messages = append([]llm.Message{summaryMsg}, b.messages[evictUntil:]...)
			b.tokenCount = running + estimateTokens(summaryMsg)
			observability.ContextCompactionsTotal.Inc()
			observability.ContextCurrentTokens.Set(float64(b.tokenCount))
			span.SetAttributes(attribute.Int("tokens.after", b.tokenCount))
			log.InfoContext(ctx, "context.compacted",
				slog.String("status", "ok"),
				slog.Int("tokens_before", tokensBefore),
				slog.Int("tokens_after", b.tokenCount),
				slog.Int("messages_evicted", evictUntil),
			)
			return nil
		}
	}

	// Bare eviction.
	b.messages = b.messages[evictUntil:]
	b.tokenCount = running
	observability.ContextCurrentTokens.Set(float64(b.tokenCount))
	span.SetAttributes(attribute.Int("tokens.after", b.tokenCount))
	log.InfoContext(ctx, "context.evicted",
		slog.String("status", "ok"),
		slog.Int("tokens_before", tokensBefore),
		slog.Int("tokens_after", b.tokenCount),
		slog.Int("messages_evicted", evictUntil),
	)
	return nil
}

// summarise calls the summarizer LLM to compress a slice of messages.
func (b *ConversationBuffer) summarise(ctx context.Context, msgs []llm.Message) (string, error) {
	var content string
	for _, m := range msgs {
		content += fmt.Sprintf("[%s]: %s\n", m.Role, m.Content)
	}
	resp, err := b.summarizer.Complete(ctx, &llm.CompletionRequest{
		Model:  b.sumModel,
		System: "Summarise the following conversation excerpt concisely, preserving key facts and decisions.",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: observability.Truncate(content, 8000)},
		},
		MaxTokens: 512,
	})
	if err != nil {
		return "", fmt.Errorf("core.summarise: %w", err)
	}
	return resp.Content, nil
}

// estimateTokens approximates the token count for one message (4 chars ≈ 1 token).
func estimateTokens(msg llm.Message) int {
	n := len(msg.Content)/4 + 4
	for _, tc := range msg.ToolCalls {
		n += len(tc.Input)/4 + 10
	}
	if msg.ToolResult != nil {
		n += len(msg.ToolResult.Content)/4 + 5
	}
	return n
}
