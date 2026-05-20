package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/core"
	"github.com/nareg/goagent/llm"
	"github.com/nareg/goagent/observability"
	"github.com/nareg/goagent/tools"
)

// stubLLMClient satisfies llm.LLMClient for testing.
type stubLLMClient struct {
	responses []*llm.CompletionResponse
	idx       int
	err       error
}

func (s *stubLLMClient) Complete(_ context.Context, _ *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.idx >= len(s.responses) {
		return &llm.CompletionResponse{Content: "done", StopReason: llm.StopReasonEndTurn}, nil
	}
	r := s.responses[s.idx]
	s.idx++
	return r, nil
}

func (s *stubLLMClient) Stream(_ context.Context, _ *llm.CompletionRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

// stubTool satisfies tools.Tool for testing.
type stubTool struct {
	name   string
	output json.RawMessage
	err    error
}

func (t *stubTool) Name() string             { return t.name }
func (t *stubTool) Description() string      { return "stub tool" }
func (t *stubTool) Schema() json.RawMessage  { return json.RawMessage(`{}`) }
func (t *stubTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return t.output, t.err
}

func newTestAgent(client llm.LLMClient, reg *tools.Registry) *core.Agent {
	buf := core.NewConversationBuffer(100_000)
	ctx := observability.WithLogger(context.Background(), observability.NewLogger("dev"))
	ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())
	_ = ctx
	return core.NewAgent(
		core.AgentConfig{Model: "claude-haiku-4-5", MaxSteps: 10},
		client,
		reg,
		buf,
	)
}

func TestAgent_run_terminalOnFirstResponse(t *testing.T) {
	client := &stubLLMClient{
		responses: []*llm.CompletionResponse{
			{Content: "hello world", StopReason: llm.StopReasonEndTurn},
		},
	}
	reg := tools.NewRegistry()
	agent := newTestAgent(client, reg)

	ctx := observability.WithLogger(context.Background(), observability.NewLogger("dev"))
	ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())

	result, err := agent.Run(ctx, "hi")
	require.NoError(t, err)
	assert.Equal(t, "hello world", result.Output)
	assert.Equal(t, 1, result.Steps)
}

func TestAgent_run_toolCallThenTerminal(t *testing.T) {
	toolOutput := json.RawMessage(`{"result":"42"}`)
	client := &stubLLMClient{
		responses: []*llm.CompletionResponse{
			{
				Content: "",
				ToolCalls: []llm.ToolCall{
					{ID: "call-1", Name: "calc", Input: json.RawMessage(`{}`)},
				},
				StopReason: llm.StopReasonToolUse,
			},
			{Content: "the answer is 42", StopReason: llm.StopReasonEndTurn},
		},
	}

	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "calc", output: toolOutput})

	agent := newTestAgent(client, reg)
	ctx := observability.WithLogger(context.Background(), observability.NewLogger("dev"))
	ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())

	result, err := agent.Run(ctx, "what is 6*7?")
	require.NoError(t, err)
	assert.Equal(t, "the answer is 42", result.Output)
	assert.Equal(t, 2, result.Steps)
}

func TestAgent_run_llmError(t *testing.T) {
	client := &stubLLMClient{err: errors.New("network failure")}
	reg := tools.NewRegistry()
	agent := newTestAgent(client, reg)

	ctx := observability.WithLogger(context.Background(), observability.NewLogger("dev"))
	ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())

	_, err := agent.Run(ctx, "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network failure")
}

func TestAgent_run_maxStepsExceeded(t *testing.T) {
	// Always returns a tool call, so the loop never terminates naturally.
	client := &stubLLMClient{}
	client.responses = make([]*llm.CompletionResponse, 20)
	for i := range client.responses {
		client.responses[i] = &llm.CompletionResponse{
			ToolCalls:  []llm.ToolCall{{ID: "c", Name: "noop", Input: json.RawMessage(`{}`)}},
			StopReason: llm.StopReasonToolUse,
		}
	}

	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "noop", output: json.RawMessage(`{}`)})

	buf := core.NewConversationBuffer(100_000)
	agent := core.NewAgent(
		core.AgentConfig{Model: "claude-haiku-4-5", MaxSteps: 3},
		client, reg, buf,
	)

	ctx := observability.WithLogger(context.Background(), observability.NewLogger("dev"))
	ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())

	_, err := agent.Run(ctx, "spin forever")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded max steps")
}

func TestAgent_run_toolError_doesNotHaltLoop(t *testing.T) {
	// Tool returns an error; agent should forward it as a tool result and continue.
	client := &stubLLMClient{
		responses: []*llm.CompletionResponse{
			{
				ToolCalls:  []llm.ToolCall{{ID: "c1", Name: "broken", Input: json.RawMessage(`{}`)}},
				StopReason: llm.StopReasonToolUse,
			},
			{Content: "handled the error", StopReason: llm.StopReasonEndTurn},
		},
	}

	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "broken", err: errors.New("tool broke")})

	agent := newTestAgent(client, reg)
	ctx := observability.WithLogger(context.Background(), observability.NewLogger("dev"))
	ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())

	result, err := agent.Run(ctx, "use broken tool")
	require.NoError(t, err)
	assert.Equal(t, "handled the error", result.Output)
}

func TestNewAgent_defaultsAgentIDAndMaxSteps(t *testing.T) {
	client := &stubLLMClient{
		responses: []*llm.CompletionResponse{
			{Content: "hi", StopReason: llm.StopReasonEndTurn},
		},
	}
	reg := tools.NewRegistry()
	buf := core.NewConversationBuffer(100_000)

	// Zero values — constructor should fill defaults.
	agent := core.NewAgent(core.AgentConfig{Model: "claude-haiku-4-5"}, client, reg, buf)

	ctx := observability.WithLogger(context.Background(), observability.NewLogger("dev"))
	ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())

	result, err := agent.Run(ctx, "hello")
	require.NoError(t, err)
	assert.NotEmpty(t, result.Output)
}
