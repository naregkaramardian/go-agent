package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/llm"
)

// mockClient is a test double for LLMClient.
type mockClient struct {
	completeFunc func(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error)
}

func (m *mockClient) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return m.completeFunc(ctx, req)
}

func (m *mockClient) Stream(_ context.Context, _ *llm.CompletionRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func TestChain_singleMiddleware(t *testing.T) {
	called := false
	mw := func(ctx context.Context, req *llm.CompletionRequest, next llm.Handler) (*llm.CompletionResponse, error) {
		called = true
		return next(ctx, req)
	}
	base := func(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
		return &llm.CompletionResponse{Content: "ok"}, nil
	}
	handler := llm.Chain(base, mw)
	resp, err := handler(context.Background(), &llm.CompletionRequest{})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.True(t, called)
}

func TestChain_orderIsOuterFirst(t *testing.T) {
	var order []int
	makeMW := func(n int) llm.Middleware {
		return func(ctx context.Context, req *llm.CompletionRequest, next llm.Handler) (*llm.CompletionResponse, error) {
			order = append(order, n)
			return next(ctx, req)
		}
	}
	base := func(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
		order = append(order, 0)
		return &llm.CompletionResponse{}, nil
	}
	llm.Chain(base, makeMW(1), makeMW(2), makeMW(3))(context.Background(), &llm.CompletionRequest{}) //nolint:errcheck
	assert.Equal(t, []int{1, 2, 3, 0}, order)
}

func TestChain_middlewareCanShortCircuit(t *testing.T) {
	errSentinel := errors.New("blocked")
	mw := func(ctx context.Context, req *llm.CompletionRequest, next llm.Handler) (*llm.CompletionResponse, error) {
		return nil, errSentinel
	}
	base := func(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
		t.Fatal("base should not be called")
		return nil, nil
	}
	_, err := llm.Chain(base, mw)(context.Background(), &llm.CompletionRequest{})
	assert.ErrorIs(t, err, errSentinel)
}

func TestMockClient_Complete(t *testing.T) {
	want := &llm.CompletionResponse{
		Content:    "hello",
		StopReason: llm.StopReasonEndTurn,
		Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5},
	}
	mc := &mockClient{
		completeFunc: func(_ context.Context, _ *llm.CompletionRequest) (*llm.CompletionResponse, error) {
			return want, nil
		},
	}
	got, err := mc.Complete(context.Background(), &llm.CompletionRequest{
		Model:    "claude-sonnet-4-5",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestMockClient_Stream_closesChannel(t *testing.T) {
	mc := &mockClient{}
	ch, err := mc.Stream(context.Background(), &llm.CompletionRequest{})
	require.NoError(t, err)
	var chunks []llm.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}
	assert.Empty(t, chunks)
}

func TestConvertTools_roundtrip(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"q":{"type":"string"}},"required":["q"]}`)
	tools := []llm.ToolDefinition{
		{Name: "search", Description: "Search the web", InputSchema: schema},
	}
	// Verify ToolDefinition fields are accessible
	assert.Equal(t, "search", tools[0].Name)
	assert.Equal(t, "Search the web", tools[0].Description)
}

func TestCompletionRequest_defaults(t *testing.T) {
	req := &llm.CompletionRequest{
		Model:    "claude-sonnet-4-5",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "ping"}},
	}
	assert.Equal(t, 0, req.MaxTokens)  // caller or client should default this
	assert.Nil(t, req.Temperature)
}
