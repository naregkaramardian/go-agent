package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/nareg/goagent/observability"
)

const openaiBaseRetryDelay = time.Second

// OpenAIConfig holds configuration for the OpenAI client.
type OpenAIConfig struct {
	APIKey    string
	BaseURL   string // optional: override for Azure or compatible endpoints
	MaxTokens int
}

// OpenAIClient wraps the OpenAI SDK and implements LLMClient.
type OpenAIClient struct {
	sdk   *openai.Client
	chain Handler
}

// NewOpenAIClient constructs an OpenAIClient with ObserveLLM pre-wired.
func NewOpenAIClient(cfg OpenAIConfig, mw ...Middleware) *OpenAIClient {
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	sdk := openai.NewClient(opts...)
	c := &OpenAIClient{sdk: &sdk}
	c.chain = Chain(c.complete, append([]Middleware{ObserveLLM()}, mw...)...)
	return c
}

// Complete sends a blocking request and returns the full response.
func (c *OpenAIClient) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	return c.chain(ctx, req)
}

func (c *OpenAIClient) complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	params, err := c.buildParams(req)
	if err != nil {
		return nil, fmt.Errorf("llm.OpenAIClient.complete: %w", err)
	}

	log := observability.LoggerFrom(ctx)
	delay := openaiBaseRetryDelay

	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, callErr := c.sdk.Chat.Completions.New(ctx, params)
		if callErr == nil {
			return convertOpenAIResponse(resp), nil
		}
		if !isOpenAIRateLimit(callErr) || attempt == maxRetries {
			return nil, fmt.Errorf("llm.OpenAIClient.complete: %w", callErr)
		}
		log.InfoContext(ctx, "llm.request.retry",
			slog.Int("attempt", attempt+1),
			slog.String("reason", "rate_limit"),
			slog.Duration("backoff", delay),
		)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("llm.OpenAIClient.complete: %w", ctx.Err())
		case <-time.After(delay):
		}
		delay *= 2
	}
	return nil, fmt.Errorf("llm.OpenAIClient.complete: exceeded retries")
}

// Stream starts a streaming completion and returns a channel of chunks.
func (c *OpenAIClient) Stream(ctx context.Context, req *CompletionRequest) (<-chan StreamChunk, error) {
	params, err := c.buildParams(req)
	if err != nil {
		return nil, fmt.Errorf("llm.OpenAIClient.Stream: %w", err)
	}
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}
	ch := make(chan StreamChunk, 32)
	go func() {
		defer close(ch)
		c.runStream(ctx, params, ch)
	}()
	return ch, nil
}

func (c *OpenAIClient) runStream(ctx context.Context, params openai.ChatCompletionNewParams, ch chan<- StreamChunk) {
	stream := c.sdk.Chat.Completions.NewStreaming(ctx, params)

	var inputTokens, outputTokens int

	for stream.Next() {
		chunk := stream.Current()

		// Track usage from the final chunk (stream_options.include_usage=true).
		if chunk.Usage.TotalTokens > 0 {
			inputTokens = int(chunk.Usage.PromptTokens)
			outputTokens = int(chunk.Usage.CompletionTokens)
		}

		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			ch <- StreamChunk{TextDelta: delta.Content}
		}

		for i, tc := range delta.ToolCalls {
			tcd := &ToolCallDelta{Index: i}
			if tc.ID != "" {
				tcd.ID = tc.ID
			}
			if tc.Function.Name != "" {
				tcd.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				tcd.InputDelta = tc.Function.Arguments
			}
			if tcd.ID != "" || tcd.Name != "" || tcd.InputDelta != "" {
				ch <- StreamChunk{ToolCallDelta: tcd}
			}
		}
	}

	if err := stream.Err(); err != nil {
		ch <- StreamChunk{Err: fmt.Errorf("llm.openai.stream: %w", err)}
		return
	}

	ch <- StreamChunk{Usage: &Usage{InputTokens: inputTokens, OutputTokens: outputTokens}}
}

func (c *OpenAIClient) buildParams(req *CompletionRequest) (openai.ChatCompletionNewParams, error) {
	if req.MaxTokens == 0 {
		req.MaxTokens = defaultMaxTokens
	}

	msgs, err := convertMessagesToOpenAI(req.Messages, req.System)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}

	params := openai.ChatCompletionNewParams{
		Model:     openai.ChatModel(req.Model),
		MaxTokens: openai.Int(int64(req.MaxTokens)),
		Messages:  msgs,
	}

	if len(req.Tools) > 0 {
		params.Tools = convertToolsToOpenAI(req.Tools)
	}

	return params, nil
}

func convertMessagesToOpenAI(msgs []Message, system string) ([]openai.ChatCompletionMessageParamUnion, error) {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs)+1)

	if system != "" {
		out = append(out, openai.SystemMessage(system))
	}

	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			if m.ToolResult != nil {
				out = append(out, openai.ToolMessage(m.ToolResult.Content, m.ToolResult.ToolCallID))
			} else {
				out = append(out, openai.UserMessage(m.Content))
			}
		case RoleAssistant:
			if len(m.ToolCalls) > 0 {
				tcs := make([]openai.ChatCompletionMessageToolCallParam, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					tcs[i] = openai.ChatCompletionMessageToolCallParam{
						ID:   tc.ID,
						Type: "function",
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: string(tc.Input),
						},
					}
				}
				msg := openai.ChatCompletionAssistantMessageParam{
					Role:      "assistant",
					ToolCalls: tcs,
				}
				if m.Content != "" {
					msg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: openai.String(m.Content),
					}
				}
				out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &msg})
			} else {
				out = append(out, openai.AssistantMessage(m.Content))
			}
		default:
			return nil, fmt.Errorf("llm.convertMessagesToOpenAI: unsupported role %q", m.Role)
		}
	}
	return out, nil
}

func convertToolsToOpenAI(tools []ToolDefinition) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, len(tools))
	for i, t := range tools {
		var params openai.FunctionParameters
		_ = json.Unmarshal(t.InputSchema, &params)
		out[i] = openai.ChatCompletionToolParam{
			Type: "function",
			Function: openai.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  params,
			},
		}
	}
	return out
}

func convertOpenAIResponse(resp *openai.ChatCompletion) *CompletionResponse {
	if len(resp.Choices) == 0 {
		return &CompletionResponse{Model: string(resp.Model)}
	}
	choice := resp.Choices[0]
	r := &CompletionResponse{
		Content: choice.Message.Content,
		Model:   string(resp.Model),
		Usage: Usage{
			InputTokens:  int(resp.Usage.PromptTokens),
			OutputTokens: int(resp.Usage.CompletionTokens),
		},
	}
	switch choice.FinishReason {
	case "stop":
		r.StopReason = StopReasonEndTurn
	case "tool_calls":
		r.StopReason = StopReasonToolUse
	case "length":
		r.StopReason = StopReasonMaxTokens
	default:
		r.StopReason = StopReasonEndTurn
	}
	for _, tc := range choice.Message.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments),
		})
	}
	return r
}

func isOpenAIRateLimit(err error) bool {
	type statusErr interface{ StatusCode() int }
	if se, ok := err.(statusErr); ok {
		return se.StatusCode() == http.StatusTooManyRequests
	}
	return false
}
