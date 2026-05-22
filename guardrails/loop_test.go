package guardrails_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/guardrails"
	"github.com/nareg/goagent/llm"
)

func toolCallStep(name string, input string) guardrails.StepHandler {
	return responseHandler(&llm.CompletionResponse{
		ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: name, Input: json.RawMessage(input)},
		},
		StopReason: llm.StopReasonToolUse,
	})
}

func terminalStep() guardrails.StepHandler {
	return responseHandler(&llm.CompletionResponse{
		Content:    "done",
		StopReason: llm.StopReasonEndTurn,
	})
}

func TestLoopDetection_passes_varied(t *testing.T) {
	guard := guardrails.LoopDetection(3)

	for i, name := range []string{"tool_a", "tool_b", "tool_a"} {
		h := guardrails.Chain(toolCallStep(name, `{}`), guard)
		_, err := h(testCtx(), guardrails.AgentStep{Number: i})
		require.NoError(t, err, "step %d should pass", i)
	}
}

func TestLoopDetection_fires_after_repeated(t *testing.T) {
	guard := guardrails.LoopDetection(3)
	ctx := testCtx()

	// First two identical calls should pass.
	for i := 0; i < 2; i++ {
		h := guardrails.Chain(toolCallStep("search", `{"q":"foo"}`), guard)
		_, err := h(ctx, guardrails.AgentStep{Number: i})
		require.NoError(t, err)
	}

	// Third identical call triggers the guard.
	h := guardrails.Chain(toolCallStep("search", `{"q":"foo"}`), guard)
	_, err := h(ctx, guardrails.AgentStep{Number: 2})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LoopDetection")
}

func TestLoopDetection_resets_on_terminal_response(t *testing.T) {
	// windowSize=3: two identical tool calls should pass, the third would fire.
	guard := guardrails.LoopDetection(3)
	ctx := testCtx()

	// Two identical tool calls — passes because window isn't full yet.
	for i := 0; i < 2; i++ {
		h := guardrails.Chain(toolCallStep("op", `{}`), guard)
		_, err := h(ctx, guardrails.AgentStep{Number: i})
		require.NoError(t, err)
	}

	// A terminal response resets state.
	h := guardrails.Chain(terminalStep(), guard)
	_, err := h(ctx, guardrails.AgentStep{Number: 2})
	require.NoError(t, err)

	// After reset, repeating the tool call is fine — window is empty again.
	h = guardrails.Chain(toolCallStep("op", `{}`), guard)
	_, err = h(ctx, guardrails.AgentStep{Number: 3})
	require.NoError(t, err)
}

func TestLoopDetection_windowSize_one_clampedToTwo(t *testing.T) {
	// windowSize < 2 is clamped to 2, so a single call should never fire.
	guard := guardrails.LoopDetection(1)
	ctx := testCtx()

	h := guardrails.Chain(toolCallStep("x", `{}`), guard)
	_, err := h(ctx, guardrails.AgentStep{Number: 0})
	require.NoError(t, err)
}

func TestLoopDetection_differentInputs_notALoop(t *testing.T) {
	guard := guardrails.LoopDetection(2)
	ctx := testCtx()

	for i, input := range []string{`{"page":1}`, `{"page":2}`, `{"page":1}`} {
		h := guardrails.Chain(toolCallStep("paginate", input), guard)
		_, err := h(ctx, guardrails.AgentStep{Number: i})
		require.NoError(t, err)
	}
}
