package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nareg/goagent/guardrails"
	"github.com/nareg/goagent/llm"
	"github.com/nareg/goagent/memory"
	"github.com/nareg/goagent/observability"
	"github.com/nareg/goagent/tools"
)

// AgentConfig holds per-agent settings.
type AgentConfig struct {
	AgentID   string // auto-generated if empty
	Model     string
	System    string
	MaxSteps  int           // 0 = unlimited (not recommended)
	MaxTokens int           // max tokens per LLM call; 0 = provider default
	Memory    memory.Memory // optional long-term store; nil = disabled
}

// RunResult summarises a completed agent run.
type RunResult struct {
	Output   string
	Steps    int
	Cost     float64
	Duration time.Duration
}

// Agent is the main runtime: it drives the plan → act → observe loop.
type Agent struct {
	cfg        AgentConfig
	llm        llm.LLMClient
	registry   *tools.Registry
	buffer     *ConversationBuffer
	guardrails []guardrails.Middleware
}

// Use appends guardrail middleware to the agent. Call before Run.
// Guards run in registration order: mw[0] is outermost (first to execute).
func (a *Agent) Use(mw ...guardrails.Middleware) {
	a.guardrails = append(a.guardrails, mw...)
}

// NewAgent constructs an Agent. The ConversationBuffer must be pre-configured
// with a token budget (and optional summarizer) before passing it here.
func NewAgent(cfg AgentConfig, client llm.LLMClient, reg *tools.Registry, buf *ConversationBuffer) *Agent {
	if cfg.AgentID == "" {
		cfg.AgentID = uuid.New().String()
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 50
	}
	return &Agent{cfg: cfg, llm: client, registry: reg, buffer: buf}
}

// ID returns the agent's unique identifier.
func (a *Agent) ID() string { return a.cfg.AgentID }

// Run executes the agent loop for a single user message and returns when the
// model emits a terminal response or a budget/error condition fires.
func (a *Agent) Run(ctx context.Context, userMsg string) (*RunResult, error) {
	ctx, span := otel.Tracer("go-agent").Start(ctx, "agent.run",
		trace.WithAttributes(
			attribute.String("agent.id", a.cfg.AgentID),
			attribute.String("agent.model", a.cfg.Model),
			attribute.Int("agent.max_steps", a.cfg.MaxSteps),
		),
	)
	defer span.End()

	log := observability.LoggerFrom(ctx)
	runStart := time.Now()

	log.InfoContext(ctx, "agent.run.start",
		slog.String("agent_id", a.cfg.AgentID),
		slog.String("model", a.cfg.Model),
		slog.Int("max_steps", a.cfg.MaxSteps),
		slog.String("status", "ok"),
	)
	observability.AgentRunsTotal.WithLabelValues("started").Inc()

	a.buffer.Add(ctx, llm.Message{Role: llm.RoleUser, Content: recallEnrich(ctx, a.cfg.Memory, userMsg)})

	var (
		steps  int
		output string
		runErr error
	)

	for steps < a.cfg.MaxSteps {
		stepCtx, stepSpan := otel.Tracer("go-agent").Start(ctx, "agent.step",
			trace.WithAttributes(
				attribute.String("agent.id", a.cfg.AgentID),
				attribute.Int("step", steps),
			),
		)

		terminal, err := a.runStep(stepCtx, steps, &output)
		stepSpan.End()
		steps++

		if err != nil {
			runErr = err
			break
		}
		if terminal {
			break
		}
	}

	if runErr == nil && steps >= a.cfg.MaxSteps {
		runErr = fmt.Errorf("agent.Run: exceeded max steps (%d)", a.cfg.MaxSteps)
	}

	dur := time.Since(runStart)
	status := "ok"
	if runErr != nil {
		status = "error"
		span.RecordError(runErr)
		span.SetStatus(codes.Error, runErr.Error())
	}

	totalCost := observability.CostLedgerFrom(ctx).TotalCost()

	observability.AgentRunsTotal.WithLabelValues(status).Inc()
	observability.AgentRunDuration.WithLabelValues(status).Observe(dur.Seconds())
	span.SetAttributes(
		attribute.Int("steps", steps),
		attribute.Float64("cost_usd", totalCost),
		attribute.String("status", status),
	)

	log.InfoContext(ctx, "agent.run.complete",
		slog.String("agent_id", a.cfg.AgentID),
		slog.String("status", status),
		slog.Int("steps", steps),
		slog.Float64("total_cost_usd", totalCost),
		slog.Int64("latency_ms", dur.Milliseconds()),
	)

	if runErr != nil {
		return nil, runErr
	}
	storeMemory(ctx, a.cfg.Memory, userMsg, output)
	return &RunResult{Output: output, Steps: steps, Cost: totalCost, Duration: dur}, nil
}

// runStep executes one iteration of the plan → act cycle.
// Returns (terminal=true, nil) when the model signals it is done.
func (a *Agent) runStep(ctx context.Context, step int, output *string) (terminal bool, err error) {
	log := observability.LoggerFrom(ctx)

	// The base handler calls the LLM and populates AgentStep.Response.
	base := guardrails.StepHandler(func(ctx context.Context, s guardrails.AgentStep) (guardrails.AgentStep, error) {
		resp, err := a.llm.Complete(ctx, &llm.CompletionRequest{
			Model:     a.cfg.Model,
			System:    a.cfg.System,
			Messages:  a.buffer.Messages(),
			Tools:     a.toolDefs(),
			MaxTokens: a.cfg.MaxTokens,
		})
		if err != nil {
			return s, fmt.Errorf("agent.runStep[%d]: llm.Complete: %w", step, err)
		}
		s.Response = resp
		return s, nil
	})

	s, err := guardrails.Chain(base, a.guardrails...)(ctx, guardrails.AgentStep{
		Number:     step,
		TokenCount: a.buffer.TokenCount(),
	})
	if err != nil {
		observability.AgentStepsTotal.WithLabelValues(a.cfg.AgentID, "error").Inc()
		return false, err
	}
	if s.Response == nil {
		observability.AgentStepsTotal.WithLabelValues(a.cfg.AgentID, "error").Inc()
		return false, fmt.Errorf("agent.runStep[%d]: guardrail returned nil response", step)
	}
	observability.AgentStepsTotal.WithLabelValues(a.cfg.AgentID, "ok").Inc()
	resp := s.Response

	// Terminal: model finished and requested no tools.
	if (resp.StopReason == llm.StopReasonEndTurn || resp.StopReason == "") && len(resp.ToolCalls) == 0 {
		*output = resp.Content
		a.buffer.Add(ctx, llm.Message{Role: llm.RoleAssistant, Content: resp.Content})
		return true, nil
	}

	// Record the assistant turn (may include both text and tool calls).
	a.buffer.Add(ctx, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	})

	// No tool calls despite non-terminal stop reason — treat as terminal.
	if len(resp.ToolCalls) == 0 {
		*output = resp.Content
		return true, nil
	}

	log.DebugContext(ctx, "agent.step.tools",
		slog.Int("step", step),
		slog.Int("tool_calls", len(resp.ToolCalls)),
	)

	// Execute all tool calls in parallel; each goroutine inherits ctx (OTel propagates).
	toolResults := a.executeTools(ctx, resp.ToolCalls)

	for _, tr := range toolResults {
		content := string(tr.Output)
		isErr := tr.Error != nil
		if isErr {
			content = tr.Error.Error()
		}
		a.buffer.Add(ctx, llm.Message{
			Role: llm.RoleUser,
			ToolResult: &llm.ToolResult{
				ToolCallID: tr.ToolCallID,
				Content:    content,
				IsError:    isErr,
			},
		})
	}

	if trimErr := a.buffer.TrimToFit(ctx); trimErr != nil {
		log.InfoContext(ctx, "context.trim.error",
			slog.String("error", trimErr.Error()),
			slog.String("status", "error"),
		)
	}

	return false, nil
}

// executeTools dispatches every tool call concurrently and collects results.
func (a *Agent) executeTools(ctx context.Context, calls []llm.ToolCall) []tools.ToolResult {
	results := make([]tools.ToolResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call llm.ToolCall) {
			defer wg.Done()
			results[i] = a.registry.Dispatch(ctx, tools.ToolCall{
				ID:    call.ID,
				Name:  call.Name,
				Input: call.Input,
			})
		}(i, call)
	}
	wg.Wait()
	return results
}

// toolDefs converts registry definitions to the llm package's ToolDefinition type.
func (a *Agent) toolDefs() []llm.ToolDefinition {
	defs := a.registry.Definitions()
	out := make([]llm.ToolDefinition, len(defs))
	for i, d := range defs {
		out[i] = llm.ToolDefinition{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: d.Schema,
		}
	}
	return out
}

// RunStreaming is like Run but writes text deltas to out as the model generates
// them. Tool execution still happens synchronously between steps.
func (a *Agent) RunStreaming(ctx context.Context, userMsg string, out io.Writer) (*RunResult, error) {
	ctx, span := otel.Tracer("go-agent").Start(ctx, "agent.run",
		trace.WithAttributes(
			attribute.String("agent.id", a.cfg.AgentID),
			attribute.String("agent.model", a.cfg.Model),
			attribute.Int("agent.max_steps", a.cfg.MaxSteps),
		),
	)
	defer span.End()

	log := observability.LoggerFrom(ctx)
	runStart := time.Now()

	log.InfoContext(ctx, "agent.run.start",
		slog.String("agent_id", a.cfg.AgentID),
		slog.String("model", a.cfg.Model),
		slog.Int("max_steps", a.cfg.MaxSteps),
		slog.String("status", "ok"),
	)
	observability.AgentRunsTotal.WithLabelValues("started").Inc()

	a.buffer.Add(ctx, llm.Message{Role: llm.RoleUser, Content: recallEnrich(ctx, a.cfg.Memory, userMsg)})

	var (
		steps  int
		output string
		runErr error
	)

	for steps < a.cfg.MaxSteps {
		stepCtx, stepSpan := otel.Tracer("go-agent").Start(ctx, "agent.step",
			trace.WithAttributes(
				attribute.String("agent.id", a.cfg.AgentID),
				attribute.Int("step", steps),
			),
		)

		terminal, err := a.runStepStreaming(stepCtx, steps, &output, out)
		stepSpan.End()
		steps++

		if err != nil {
			runErr = err
			break
		}
		if terminal {
			break
		}
	}

	if runErr == nil && steps >= a.cfg.MaxSteps {
		runErr = fmt.Errorf("agent.RunStreaming: exceeded max steps (%d)", a.cfg.MaxSteps)
	}

	dur := time.Since(runStart)
	status := "ok"
	if runErr != nil {
		status = "error"
		span.RecordError(runErr)
		span.SetStatus(codes.Error, runErr.Error())
	}

	totalCost := observability.CostLedgerFrom(ctx).TotalCost()

	observability.AgentRunsTotal.WithLabelValues(status).Inc()
	observability.AgentRunDuration.WithLabelValues(status).Observe(dur.Seconds())
	span.SetAttributes(
		attribute.Int("steps", steps),
		attribute.Float64("cost_usd", totalCost),
		attribute.String("status", status),
	)

	log.InfoContext(ctx, "agent.run.complete",
		slog.String("agent_id", a.cfg.AgentID),
		slog.String("status", status),
		slog.Int("steps", steps),
		slog.Float64("total_cost_usd", totalCost),
		slog.Int64("latency_ms", dur.Milliseconds()),
	)

	if runErr != nil {
		return nil, runErr
	}
	storeMemory(ctx, a.cfg.Memory, userMsg, output)
	return &RunResult{Output: output, Steps: steps, Cost: totalCost, Duration: dur}, nil
}

// runStepStreaming executes one iteration using Stream() so text deltas are
// written to out immediately. Tool calls are executed synchronously as usual.
func (a *Agent) runStepStreaming(ctx context.Context, step int, output *string, out io.Writer) (bool, error) {
	log := observability.LoggerFrom(ctx)

	base := guardrails.StepHandler(func(ctx context.Context, s guardrails.AgentStep) (guardrails.AgentStep, error) {
		req := &llm.CompletionRequest{
			Model:     a.cfg.Model,
			System:    a.cfg.System,
			Messages:  a.buffer.Messages(),
			Tools:     a.toolDefs(),
			MaxTokens: a.cfg.MaxTokens,
		}

		ctx, span := otel.Tracer("go-agent").Start(ctx, "llm.stream",
			trace.WithAttributes(
				attribute.String("llm.model", req.Model),
				attribute.String("llm.provider", "anthropic"),
			),
		)
		defer span.End()

		log.DebugContext(ctx, "llm.request.start",
			slog.String("model", req.Model),
			slog.Int("messages", len(req.Messages)),
		)

		start := time.Now()
		ch, err := a.llm.Stream(ctx, req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return s, fmt.Errorf("agent.runStepStreaming[%d]: stream: %w", step, err)
		}

		resp, err := drainStream(ch, out)
		dur := time.Since(start)

		status := "ok"
		if err != nil {
			status = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			observability.LLMRequestsTotal.WithLabelValues(req.Model, status).Inc()
			observability.LLMLatency.WithLabelValues(req.Model).Observe(dur.Seconds())
			return s, fmt.Errorf("agent.runStepStreaming[%d]: drain: %w", step, err)
		}

		resp.Model = req.Model
		cost := observability.CostLedgerFrom(ctx).Record(req.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)

		span.SetAttributes(
			attribute.Int64("latency_ms", dur.Milliseconds()),
			attribute.String("status", status),
			attribute.Int("llm.input_tokens", resp.Usage.InputTokens),
			attribute.Int("llm.output_tokens", resp.Usage.OutputTokens),
		)
		observability.LLMRequestsTotal.WithLabelValues(req.Model, status).Inc()
		observability.LLMLatency.WithLabelValues(req.Model).Observe(dur.Seconds())

		log.InfoContext(ctx, "llm.stream.complete",
			slog.String("model", req.Model),
			slog.String("status", status),
			slog.Int64("latency_ms", dur.Milliseconds()),
			slog.Int("input_tokens", resp.Usage.InputTokens),
			slog.Int("output_tokens", resp.Usage.OutputTokens),
			slog.Float64("cost_usd", cost),
		)
		log.InfoContext(ctx, "cost.updated",
			slog.String("model", req.Model),
			slog.Float64("cost_usd", cost),
			slog.Float64("total_cost_usd", observability.CostLedgerFrom(ctx).TotalCost()),
		)

		s.Response = resp
		return s, nil
	})

	s, err := guardrails.Chain(base, a.guardrails...)(ctx, guardrails.AgentStep{
		Number:     step,
		TokenCount: a.buffer.TokenCount(),
	})
	if err != nil {
		observability.AgentStepsTotal.WithLabelValues(a.cfg.AgentID, "error").Inc()
		return false, err
	}
	if s.Response == nil {
		observability.AgentStepsTotal.WithLabelValues(a.cfg.AgentID, "error").Inc()
		return false, fmt.Errorf("agent.runStepStreaming[%d]: nil response after guardrails", step)
	}
	observability.AgentStepsTotal.WithLabelValues(a.cfg.AgentID, "ok").Inc()
	resp := s.Response

	if (resp.StopReason == llm.StopReasonEndTurn || resp.StopReason == "") && len(resp.ToolCalls) == 0 {
		*output = resp.Content
		a.buffer.Add(ctx, llm.Message{Role: llm.RoleAssistant, Content: resp.Content})
		return true, nil
	}

	a.buffer.Add(ctx, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	})

	if len(resp.ToolCalls) == 0 {
		*output = resp.Content
		return true, nil
	}

	log.DebugContext(ctx, "agent.step.tools",
		slog.Int("step", step),
		slog.Int("tool_calls", len(resp.ToolCalls)),
	)

	toolResults := a.executeTools(ctx, resp.ToolCalls)

	for _, tr := range toolResults {
		content := string(tr.Output)
		isErr := tr.Error != nil
		if isErr {
			content = tr.Error.Error()
		}
		a.buffer.Add(ctx, llm.Message{
			Role: llm.RoleUser,
			ToolResult: &llm.ToolResult{
				ToolCallID: tr.ToolCallID,
				Content:    content,
				IsError:    isErr,
			},
		})
	}

	if trimErr := a.buffer.TrimToFit(ctx); trimErr != nil {
		log.InfoContext(ctx, "context.trim.error",
			slog.String("error", trimErr.Error()),
			slog.String("status", "error"),
		)
	}

	return false, nil
}

// drainStream reads all chunks from ch, writes text deltas to out, and returns
// a fully assembled CompletionResponse. The Model field is left empty for the
// caller to populate.
func drainStream(ch <-chan llm.StreamChunk, out io.Writer) (*llm.CompletionResponse, error) {
	var (
		content       strings.Builder
		usage         llm.Usage
		toolCallMap   = make(map[int]*llm.ToolCall)
		inputBuilders = make(map[int]*strings.Builder)
	)

	for chunk := range ch {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		if chunk.TextDelta != "" {
			content.WriteString(chunk.TextDelta)
			if out != nil {
				_, _ = fmt.Fprint(out, chunk.TextDelta)
			}
		}
		if d := chunk.ToolCallDelta; d != nil {
			if _, ok := toolCallMap[d.Index]; !ok {
				toolCallMap[d.Index] = &llm.ToolCall{ID: d.ID, Name: d.Name}
				inputBuilders[d.Index] = &strings.Builder{}
			} else {
				tc := toolCallMap[d.Index]
				if d.ID != "" {
					tc.ID = d.ID
				}
				if d.Name != "" {
					tc.Name = d.Name
				}
			}
			if d.InputDelta != "" {
				inputBuilders[d.Index].WriteString(d.InputDelta)
			}
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
	}

	toolCalls := make([]llm.ToolCall, 0, len(toolCallMap))
	for i := 0; i < len(toolCallMap); i++ {
		tc := toolCallMap[i]
		if b, ok := inputBuilders[i]; ok {
			tc.Input = json.RawMessage(b.String())
		}
		toolCalls = append(toolCalls, *tc)
	}

	stopReason := llm.StopReasonEndTurn
	if len(toolCalls) > 0 {
		stopReason = llm.StopReasonToolUse
	}

	return &llm.CompletionResponse{
		Content:    content.String(),
		ToolCalls:  toolCalls,
		StopReason: stopReason,
		Usage:      usage,
	}, nil
}

// errTerminal is used internally; kept unexported.
var errTerminal = errors.New("terminal")

// recallEnrich prepends relevant long-term memories to msg so the model has
// context from past sessions. Returns msg unchanged when mem is nil or empty.
func recallEnrich(ctx context.Context, mem memory.Memory, msg string) string {
	if mem == nil {
		return msg
	}
	entries, err := mem.Recall(ctx, msg, 5)
	if err != nil || len(entries) == 0 {
		return msg
	}
	var b strings.Builder
	b.WriteString("## Relevant memories from previous sessions\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- %s\n", e.Content)
	}
	b.WriteString("\n## Current request\n")
	b.WriteString(msg)
	return b.String()
}

// storeMemory saves the user/assistant exchange as a memory entry.
// Best-effort: errors are silently dropped to avoid breaking the agent run.
func storeMemory(ctx context.Context, mem memory.Memory, userMsg, output string) {
	if mem == nil || output == "" {
		return
	}
	_ = mem.Store(ctx, memory.MemoryEntry{
		Content: fmt.Sprintf("User: %s\nAssistant: %s",
			observability.Truncate(userMsg, 300),
			observability.Truncate(output, 500)),
		Tags: []string{"session"},
	})
}
