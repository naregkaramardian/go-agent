package orchestration_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/nareg/goagent/agents"
	"github.com/nareg/goagent/core"
	"github.com/nareg/goagent/llm"
	"github.com/nareg/goagent/orchestration"
	"github.com/nareg/goagent/tools"
)

// stubLLM returns a canned response so tests don't hit the network.
type stubLLM struct{ response string }

func (s *stubLLM) Complete(_ context.Context, _ *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{
		Content:    s.response,
		StopReason: "end_turn",
		Usage:      llm.Usage{InputTokens: 10, OutputTokens: 20},
	}, nil
}

func (s *stubLLM) Stream(_ context.Context, _ *llm.CompletionRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 2)
	ch <- llm.StreamChunk{TextDelta: s.response}
	ch <- llm.StreamChunk{Usage: &llm.Usage{InputTokens: 10, OutputTokens: 20}}
	close(ch)
	return ch, nil
}

// newBuilder returns an orchestration.Builder backed by a stub LLM.
func newBuilder(response string) orchestration.Builder {
	return func(preset agents.Preset) *core.Agent {
		buf := core.NewConversationBuffer(10_000)
		reg := tools.NewRegistry()
		return core.NewAgent(core.AgentConfig{
			Model:     "stub",
			System:    preset.System,
			MaxSteps:  preset.MaxSteps,
			MaxTokens: preset.MaxTokens,
		}, &stubLLM{response: response}, reg, buf)
	}
}

func TestPipeline_RunsStepsInOrder(t *testing.T) {
	ctx := context.Background()
	p := &orchestration.Pipeline{
		Steps: []orchestration.AgentStep{
			{Name: "StepA", Preset: agents.SeniorEngineer()},
			{Name: "StepB", Preset: agents.CodeReviewer()},
		},
		Builder: newBuilder("step output"),
	}

	var buf bytes.Buffer
	result, err := p.Run(ctx, "do something", &buf)
	if err != nil {
		t.Fatalf("Pipeline.Run: unexpected error: %v", err)
	}
	if len(result.StepResults) != 2 {
		t.Errorf("want 2 step results, got %d", len(result.StepResults))
	}
	if result.StepResults[0].Name != "StepA" {
		t.Errorf("want first step StepA, got %q", result.StepResults[0].Name)
	}
	if result.StepResults[1].Name != "StepB" {
		t.Errorf("want second step StepB, got %q", result.StepResults[1].Name)
	}
}

func TestPipeline_OutputIsLastStep(t *testing.T) {
	ctx := context.Background()
	p := &orchestration.Pipeline{
		Steps: []orchestration.AgentStep{
			{Name: "First", Preset: agents.SeniorEngineer()},
			{Name: "Second", Preset: agents.CodeReviewer()},
		},
		Builder: newBuilder("final answer"),
	}

	var buf bytes.Buffer
	result, err := p.Run(ctx, "task", &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "final answer" {
		t.Errorf("want Output %q, got %q", "final answer", result.Output)
	}
}

func TestParallel_RunsAllSteps(t *testing.T) {
	ctx := context.Background()
	p := &orchestration.Parallel{
		Steps: []orchestration.AgentStep{
			{Name: "A", Preset: agents.SecurityReviewer()},
			{Name: "B", Preset: agents.CodeReviewer()},
			{Name: "C", Preset: agents.TestEngineer()},
		},
		Builder: newBuilder("parallel output"),
	}

	var buf bytes.Buffer
	result, err := p.Run(ctx, "review this", &buf)
	if err != nil {
		t.Fatalf("Parallel.Run: unexpected error: %v", err)
	}
	if len(result.StepResults) != 3 {
		t.Errorf("want 3 step results, got %d", len(result.StepResults))
	}
}

func TestReviewLoop_ApprovedOnFirstRound(t *testing.T) {
	ctx := context.Background()
	// Reviewer always returns APPROVED.
	roundCount := 0
	builder := func(preset agents.Preset) *core.Agent {
		var resp string
		if preset.ID == agents.CodeReviewer().ID {
			resp = "APPROVED — looks great"
		} else {
			resp = "here is my work"
		}
		roundCount++
		buf := core.NewConversationBuffer(10_000)
		reg := tools.NewRegistry()
		return core.NewAgent(core.AgentConfig{
			Model: "stub", System: preset.System,
			MaxSteps: preset.MaxSteps, MaxTokens: preset.MaxTokens,
		}, &stubLLM{response: resp}, reg, buf)
	}

	rl := &orchestration.ReviewLoop{
		Worker:    orchestration.AgentStep{Name: "Engineer", Preset: agents.SeniorEngineer()},
		Reviewer:  orchestration.AgentStep{Name: "Reviewer", Preset: agents.CodeReviewer()},
		MaxRounds: 3,
		Builder:   builder,
	}

	var buf bytes.Buffer
	result, err := rl.Run(ctx, "build X", &buf)
	if err != nil {
		t.Fatalf("ReviewLoop.Run: unexpected error: %v", err)
	}
	// 2 step results: worker + reviewer (both in round 1).
	if len(result.StepResults) != 2 {
		t.Errorf("want 2 step results, got %d", len(result.StepResults))
	}
}

func TestReviewLoop_ExhaustsMaxRounds(t *testing.T) {
	ctx := context.Background()
	rl := &orchestration.ReviewLoop{
		Worker:    orchestration.AgentStep{Name: "Engineer", Preset: agents.SeniorEngineer()},
		Reviewer:  orchestration.AgentStep{Name: "Reviewer", Preset: agents.CodeReviewer()},
		MaxRounds: 2,
		Builder:   newBuilder("needs more work"),
	}

	var buf bytes.Buffer
	result, err := rl.Run(ctx, "task", &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 2 rounds × 2 agents = 4 step results.
	if len(result.StepResults) != 4 {
		t.Errorf("want 4 step results, got %d", len(result.StepResults))
	}
}

func TestAllWorkflows_Exist(t *testing.T) {
	ids := []string{"code-review", "feature-build", "security-audit", "full-pipeline"}
	for _, id := range ids {
		wf, ok := orchestration.WorkflowByID(id)
		if !ok {
			t.Errorf("workflow %q not found", id)
			continue
		}
		if wf.ID != id {
			t.Errorf("workflow ID mismatch: want %q, got %q", id, wf.ID)
		}
		if wf.Name == "" {
			t.Errorf("workflow %q has empty name", id)
		}
		if wf.Build == nil {
			t.Errorf("workflow %q has nil Build function", id)
		}
	}
}

func TestAllWorkflows_BuildReturnsRunner(t *testing.T) {
	b := newBuilder("test output")
	for _, wf := range orchestration.AllWorkflows() {
		runner := wf.Build(b)
		if runner == nil {
			t.Errorf("workflow %q Build returned nil", wf.ID)
		}
	}
}

func TestResult_TotalCostAccumulates(t *testing.T) {
	ctx := context.Background()
	p := &orchestration.Pipeline{
		Steps: []orchestration.AgentStep{
			{Name: "A", Preset: agents.SeniorEngineer()},
			{Name: "B", Preset: agents.SeniorEngineer()},
		},
		Builder: newBuilder("output"),
	}

	var buf bytes.Buffer
	result, err := p.Run(ctx, "task", &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalCost < 0 {
		t.Errorf("TotalCost should not be negative")
	}
	if result.Duration < 0 {
		t.Errorf("Duration should not be negative")
	}
	_ = result.Duration.Round(time.Millisecond) // smoke-test Duration field
}
