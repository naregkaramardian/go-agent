package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/nareg/goagent/observability"
)

const (
	defaultMaxTokens = 4096
	maxRetries       = 4
	baseRetryDelay   = time.Second
)

// AnthropicConfig holds configuration for the Anthropic client.
type AnthropicConfig struct {
	APIKey    string
	Model     string
	MaxTokens int
}

// AnthropicClient wraps the Anthropic SDK and implements LLMClient.
type AnthropicClient struct {
	sdk   *anthropic.Client
	model string
	chain Handler
}

// NewAnthropicClient constructs an AnthropicClient. ObserveLLM middleware is
// prepended automatically; additional middleware may be appended via mw.
func NewAnthropicClient(cfg AnthropicConfig, mw ...Middleware) *AnthropicClient {
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	sdk := anthropic.NewClient(option.WithAPIKey(cfg.APIKey))
	c := &AnthropicClient{
		sdk:   &sdk,
		model: cfg.Model,
	}
	c.chain = Chain(c.complete, append([]Middleware{ObserveLLM()}, mw...)...)
	return c
}

// Complete sends a blocking request and returns the full response.
func (c *AnthropicClient) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	return c.chain(ctx, req)
}

// complete is the raw completion call, retrying on rate-limit errors.
func (c *AnthropicClient) complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	params, err := c.buildParams(req)
	if err != nil {
		return nil, fmt.Errorf("llm.AnthropicClient.complete: %w", err)
	}

	log := observability.LoggerFrom(ctx)
	delay := baseRetryDelay

	for attempt := 0; attempt <= maxRetries; attempt++ {
		msg, callErr := c.sdk.Messages.New(ctx, params)
		if callErr == nil {
			return c.convertResponse(msg), nil
		}
		if !isRateLimit(callErr) || attempt == maxRetries {
			return nil, fmt.Errorf("llm.AnthropicClient.complete: %w", callErr)
		}
		log.InfoContext(ctx, "llm.request.retry",
			slog.Int("attempt", attempt+1),
			slog.String("reason", "rate_limit"),
			slog.Duration("backoff", delay),
		)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("llm.AnthropicClient.complete: %w", ctx.Err())
		case <-time.After(delay):
		}
		delay *= 2
	}
	// unreachable
	return nil, fmt.Errorf("llm.AnthropicClient.complete: exceeded retries")
}

// Stream starts a streaming completion and returns a channel of chunks.
// The channel is closed after the final chunk (Usage populated) or on error.
func (c *AnthropicClient) Stream(ctx context.Context, req *CompletionRequest) (<-chan StreamChunk, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	params, err := c.buildParams(req)
	if err != nil {
		return nil, fmt.Errorf("llm.AnthropicClient.Stream: %w", err)
	}
	ch := make(chan StreamChunk, 32)
	go func() {
		defer close(ch)
		c.runStream(ctx, params, ch)
	}()
	return ch, nil
}

func (c *AnthropicClient) runStream(ctx context.Context, params anthropic.MessageNewParams, ch chan<- StreamChunk) {
	stream := c.sdk.Messages.NewStreaming(ctx, params)
	acc := anthropic.Message{}

	for stream.Next() {
		event := stream.Current()
		if err := acc.Accumulate(event); err != nil {
			ch <- StreamChunk{Err: fmt.Errorf("llm.stream.accumulate: %w", err)}
			return
		}
		if chunk := chunkFromEvent(event); chunk != nil {
			ch <- *chunk
		}
	}
	if err := stream.Err(); err != nil {
		ch <- StreamChunk{Err: fmt.Errorf("llm.stream: %w", err)}
		return
	}
	// Final chunk carries usage from the accumulated message.
	ch <- StreamChunk{
		Usage: &Usage{
			InputTokens:  int(acc.Usage.InputTokens),
			OutputTokens: int(acc.Usage.OutputTokens),
		},
	}
}

func (c *AnthropicClient) buildParams(req *CompletionRequest) (anthropic.MessageNewParams, error) {
	if req.MaxTokens == 0 {
		req.MaxTokens = defaultMaxTokens
	}
	msgs, err := convertMessages(req.Messages)
	if err != nil {
		return anthropic.MessageNewParams{}, err
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
		Messages:  msgs,
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if len(req.Tools) > 0 {
		params.Tools = convertTools(req.Tools)
	}
	return params, nil
}

func (c *AnthropicClient) convertResponse(msg *anthropic.Message) *CompletionResponse {
	resp := &CompletionResponse{
		StopReason: StopReason(msg.StopReason),
		Model:      string(msg.Model),
		Usage: Usage{
			InputTokens:  int(msg.Usage.InputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
		},
	}
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			resp.Content += block.Text
		case "tool_use":
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: block.Input,
			})
		}
	}
	return resp
}

func convertMessages(msgs []Message) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			if m.ToolResult != nil {
				out = append(out, anthropic.NewUserMessage(
					anthropic.NewToolResultBlock(m.ToolResult.ToolCallID, m.ToolResult.Content, m.ToolResult.IsError),
				))
			} else {
				out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
			}
		case RoleAssistant:
			if len(m.ToolCalls) > 0 {
				blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolCalls)+1)
				if m.Content != "" {
					blocks = append(blocks, anthropic.NewTextBlock(m.Content))
				}
				for _, tc := range m.ToolCalls {
					var input any
					_ = json.Unmarshal(tc.Input, &input)
					blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Name))
				}
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			} else {
				out = append(out, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
			}
		default:
			return nil, fmt.Errorf("llm.convertMessages: unsupported role %q", m.Role)
		}
	}
	return out, nil
}

func convertTools(tools []ToolDefinition) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, len(tools))
	for i, t := range tools {
		var schema anthropic.ToolInputSchemaParam
		// Unmarshal properties and required from the raw JSON schema.
		var raw struct {
			Properties any      `json:"properties"`
			Required   []string `json:"required"`
		}
		_ = json.Unmarshal(t.InputSchema, &raw)
		schema.Properties = raw.Properties
		schema.Required = raw.Required
		out[i] = anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: schema,
			},
		}
	}
	return out
}

// chunkFromEvent converts a stream event into a StreamChunk. Returns nil for
// events that carry no user-visible content (e.g. message_stop).
func chunkFromEvent(event anthropic.MessageStreamEventUnion) *StreamChunk {
	switch event.Type {
	case "content_block_delta":
		delta := event.Delta
		switch delta.Type {
		case "text_delta":
			return &StreamChunk{TextDelta: delta.Text}
		case "input_json_delta":
			return &StreamChunk{
				ToolCallDelta: &ToolCallDelta{
					Index:      int(event.Index),
					InputDelta: delta.PartialJSON,
				},
			}
		}
	case "content_block_start":
		cb := event.ContentBlock
		if cb.Type == "tool_use" {
			return &StreamChunk{
				ToolCallDelta: &ToolCallDelta{
					Index: int(event.Index),
					ID:    cb.ID,
					Name:  cb.Name,
				},
			}
		}
	}
	return nil
}

func isRateLimit(err error) bool {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}
