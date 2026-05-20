package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/tools"
)

// echoTool returns its input as output — useful for registry tests.
type echoTool struct{ name string }

func (e *echoTool) Name() string             { return e.name }
func (e *echoTool) Description() string      { return "echoes input" }
func (e *echoTool) Schema() json.RawMessage  { return json.RawMessage(`{"type":"object"}`) }
func (e *echoTool) Execute(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	return input, nil
}

// errTool always returns an error.
type errTool struct{}

func (e *errTool) Name() string             { return "error_tool" }
func (e *errTool) Description() string      { return "always errors" }
func (e *errTool) Schema() json.RawMessage  { return json.RawMessage(`{"type":"object"}`) }
func (e *errTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("intentional error")
}

func TestRegistry_registerAndDispatch(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "echo"})

	call := tools.ToolCall{ID: "1", Name: "echo", Input: json.RawMessage(`{"msg":"hi"}`)}
	result := reg.Dispatch(context.Background(), call)
	require.NoError(t, result.Error)
	assert.Equal(t, json.RawMessage(`{"msg":"hi"}`), result.Output)
	assert.Equal(t, "1", result.ToolCallID)
}

func TestRegistry_unknownTool(t *testing.T) {
	reg := tools.NewRegistry()
	result := reg.Dispatch(context.Background(), tools.ToolCall{Name: "ghost"})
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "unknown tool")
}

func TestRegistry_duplicatePanics(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "dup"})
	assert.Panics(t, func() {
		reg.Register(&echoTool{name: "dup"})
	})
}

func TestRegistry_errToolPropagates(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&errTool{})
	result := reg.Dispatch(context.Background(), tools.ToolCall{Name: "error_tool", Input: json.RawMessage(`{}`)})
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "intentional error")
}

func TestRegistry_definitions(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "a"})
	reg.Register(&echoTool{name: "b"})
	defs := reg.Definitions()
	assert.Len(t, defs, 2)
	names := []string{defs[0].Name, defs[1].Name}
	assert.ElementsMatch(t, []string{"a", "b"}, names)
}

func TestRegistry_concurrentDispatch(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "echo"})

	const n = 50
	var wg sync.WaitGroup
	results := make([]tools.ToolResult, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = reg.Dispatch(context.Background(), tools.ToolCall{
				ID:    "id",
				Name:  "echo",
				Input: json.RawMessage(`{}`),
			})
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		assert.NoError(t, r.Error)
	}
}

func TestChain_orderIsOuterFirst(t *testing.T) {
	var order []int
	makeMW := func(n int) tools.Middleware {
		return func(ctx context.Context, call tools.ToolCall, next tools.Handler) tools.ToolResult {
			order = append(order, n)
			return next(ctx, call)
		}
	}
	base := func(_ context.Context, _ tools.ToolCall) tools.ToolResult {
		order = append(order, 0)
		return tools.ToolResult{}
	}
	tools.Chain(base, makeMW(1), makeMW(2), makeMW(3))(context.Background(), tools.ToolCall{})
	assert.Equal(t, []int{1, 2, 3, 0}, order)
}
