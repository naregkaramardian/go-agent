package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nareg/goagent/guardrails"
	"github.com/nareg/goagent/llm"
	"github.com/nareg/goagent/observability"
	"github.com/nareg/goagent/tools"
)

// AgentConfig holds per-agent settings.
type AgentConfig struct {
	AgentID   string // auto-generated if empty
	Model     string
	System    string
	MaxSteps  int // 0 = unlimited (not recommended)
	MaxTokens int // max tokens per LLM call; 0 = provider default
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

	a.buffer.Add(ctx, llm.Message{Role: llm.RoleUser, Content: userMsg})

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

// errTerminal is used internally; kept unexported.
var errTerminal = errors.New("terminal")
