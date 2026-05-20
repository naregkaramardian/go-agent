package tools

import (
	"context"
	"fmt"
	"sync"
)

// Registry holds registered tools and dispatches calls through middleware.
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	handler Handler
}

// NewRegistry returns a Registry with ObserveTool middleware pre-installed.
// Additional middleware may be appended via mw.
func NewRegistry(mw ...Middleware) *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	r.handler = Chain(r.rawDispatch, append([]Middleware{ObserveTool()}, mw...)...)
	return r
}

// Register adds a Tool. Panics on duplicate name (programming error at startup).
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name()]; exists {
		panic(fmt.Sprintf("tools.Registry: duplicate tool name %q", t.Name()))
	}
	r.tools[t.Name()] = t
}

// Dispatch executes a tool call through the full middleware chain.
func (r *Registry) Dispatch(ctx context.Context, call ToolCall) ToolResult {
	return r.handler(ctx, call)
}

// Definitions returns every registered tool's contract for passing to an LLM.
func (r *Registry) Definitions() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, ToolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return defs
}

// rawDispatch is the un-middlewared inner dispatch.
func (r *Registry) rawDispatch(ctx context.Context, call ToolCall) ToolResult {
	r.mu.RLock()
	t, ok := r.tools[call.Name]
	r.mu.RUnlock()
	if !ok {
		return ToolResult{
			ToolCallID: call.ID,
			Error:      fmt.Errorf("tools.Registry: unknown tool %q", call.Name),
		}
	}
	out, err := t.Execute(ctx, call.Input)
	return ToolResult{ToolCallID: call.ID, Output: out, Error: err}
}
