package guardrails_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/guardrails"
	"github.com/nareg/goagent/llm"
)

func TestRequireStructuredOutput_passes_validJSON(t *testing.T) {
	base := responseHandler(&llm.CompletionResponse{
		Content:    `{"answer": 42}`,
		StopReason: llm.StopReasonEndTurn,
	})
	h := guardrails.Chain(base, guardrails.RequireStructuredOutput(nil))
	_, err := h(testCtx(), guardrails.AgentStep{})
	require.NoError(t, err)
}

func TestRequireStructuredOutput_blocks_invalidJSON(t *testing.T) {
	base := responseHandler(&llm.CompletionResponse{
		Content:    "Here is a plain text response without JSON.",
		StopReason: llm.StopReasonEndTurn,
	})
	h := guardrails.Chain(base, guardrails.RequireStructuredOutput(nil))
	_, err := h(testCtx(), guardrails.AgentStep{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RequireStructuredOutput")
}

func TestRequireStructuredOutput_skips_toolCallResponse(t *testing.T) {
	// When the model responds with tool calls (not text), the check is skipped.
	base := responseHandler(&llm.CompletionResponse{
		Content: "not json",
		ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "search", Input: json.RawMessage(`{}`)},
		},
		StopReason: llm.StopReasonToolUse,
	})
	h := guardrails.Chain(base, guardrails.RequireStructuredOutput(nil))
	_, err := h(testCtx(), guardrails.AgentStep{})
	require.NoError(t, err)
}

func TestRequireStructuredOutput_passes_nilResponse(t *testing.T) {
	base := guardrails.StepHandler(func(_ context.Context, s guardrails.AgentStep) (guardrails.AgentStep, error) {
		// Response intentionally left nil.
		return s, nil
	})
	h := guardrails.Chain(base, guardrails.RequireStructuredOutput(nil))
	_, err := h(testCtx(), guardrails.AgentStep{})
	require.NoError(t, err)
}

func TestRequireStructuredOutput_passes_emptyJSON(t *testing.T) {
	base := responseHandler(&llm.CompletionResponse{
		Content:    `{}`,
		StopReason: llm.StopReasonEndTurn,
	})
	h := guardrails.Chain(base, guardrails.RequireStructuredOutput(nil))
	_, err := h(testCtx(), guardrails.AgentStep{})
	require.NoError(t, err)
}

func TestRequireStructuredOutput_withSchemaArg_stillValidatesJSON(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"result":{"type":"number"}}}`)
	base := responseHandler(&llm.CompletionResponse{
		Content:    "not valid json at all",
		StopReason: llm.StopReasonEndTurn,
	})
	h := guardrails.Chain(base, guardrails.RequireStructuredOutput(schema))
	_, err := h(testCtx(), guardrails.AgentStep{})
	require.Error(t, err)
}
