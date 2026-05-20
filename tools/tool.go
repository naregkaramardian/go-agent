package tools

import (
	"context"
	"encoding/json"
)

// Tool is the interface every tool must implement.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

// ToolCall is a tool invocation from the agent loop.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult is the outcome of executing a tool.
type ToolResult struct {
	ToolCallID string
	Output     json.RawMessage
	Error      error
}

// ToolDefinition exposes a tool's contract to an LLM.
type ToolDefinition struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Handler is the core dispatch function type.
type Handler func(ctx context.Context, call ToolCall) ToolResult

// Middleware wraps a Handler to add cross-cutting behaviour.
type Middleware func(ctx context.Context, call ToolCall, next Handler) ToolResult

// Chain applies middleware in order around base, outermost first.
func Chain(base Handler, mw ...Middleware) Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		m := mw[i]
		inner := base
		base = func(ctx context.Context, call ToolCall) ToolResult {
			return m(ctx, call, inner)
		}
	}
	return base
}
