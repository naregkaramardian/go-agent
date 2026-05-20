package llm

import (
	"context"
	"encoding/json"
)

// Role identifies whose turn a message belongs to.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a single turn in the conversation.
type Message struct {
	Role      Role
	Content   string
	ToolCalls []ToolCall  // set when Role=assistant and model invoked tools
	ToolResult *ToolResult // set when delivering a tool result back to the model
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult delivers a tool's output back to the model.
type ToolResult struct {
	ToolCallID string
	Content    string
	IsError    bool
}

// ToolDefinition describes a callable tool to the model.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage // JSON Schema object
}

// CompletionRequest is a request for a model completion.
type CompletionRequest struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolDefinition
	MaxTokens   int
	Temperature *float64 // nil = model default
}

// Usage records token consumption for one completion.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// StopReason indicates why the model stopped generating.
type StopReason string

const (
	StopReasonEndTurn   StopReason = "end_turn"
	StopReasonToolUse   StopReason = "tool_use"
	StopReasonMaxTokens StopReason = "max_tokens"
)

// CompletionResponse is the model's response to a CompletionRequest.
type CompletionResponse struct {
	Content    string
	ToolCalls  []ToolCall
	StopReason StopReason
	Usage      Usage
	Model      string
}

// StreamChunk is one piece of a streaming completion.
type StreamChunk struct {
	TextDelta     string
	ToolCallDelta *ToolCallDelta
	Usage         *Usage // non-nil on the final chunk only
	Err           error
}

// ToolCallDelta carries an incremental portion of a tool call during streaming.
type ToolCallDelta struct {
	Index      int
	ID         string // set on the first delta for this index
	Name       string // set on the first delta for this index
	InputDelta string // partial JSON
}

// Handler is the base function type for LLM completions.
type Handler func(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)

// Middleware wraps a Handler to add cross-cutting behaviour.
type Middleware func(ctx context.Context, req *CompletionRequest, next Handler) (*CompletionResponse, error)

// LLMClient is the interface every provider must implement.
type LLMClient interface {
	Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)
	Stream(ctx context.Context, req *CompletionRequest) (<-chan StreamChunk, error)
}

// Chain applies middleware in order around base, outermost first.
func Chain(base Handler, mw ...Middleware) Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		m := mw[i]
		inner := base
		base = func(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
			return m(ctx, req, inner)
		}
	}
	return base
}
