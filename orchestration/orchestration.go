// Package orchestration wires multiple agents into higher-level workflows.
//
// Three primitives:
//   - Pipeline   — A → B → C sequential; each step sees all prior outputs
//   - Parallel   — concurrent fan-out on the same input, results collected
//   - ReviewLoop — worker ↔ reviewer until approved or max rounds reached
//
// Pre-wired workflows for the built-in agent presets live in presets.go.
package orchestration

import (
	"fmt"
	"strings"
	"time"

	"github.com/nareg/goagent/agents"
	"github.com/nareg/goagent/core"
)

// Builder creates a fresh *core.Agent for the given preset.
// Each call must return a new agent with an empty conversation buffer so
// steps do not bleed state into each other.
type Builder func(preset agents.Preset) *core.Agent

// AgentStep is one unit of work: a display name + the preset that drives it.
type AgentStep struct {
	Name   string
	Preset agents.Preset
}

// StepResult holds the outcome of a single agent step.
type StepResult struct {
	Name     string
	Output   string
	Steps    int
	Cost     float64
	Duration time.Duration
	Err      error
}

// Result is the final outcome of a full orchestration run.
type Result struct {
	// Output is the last step's text (Pipeline/ReviewLoop) or merged text (Parallel).
	Output      string
	StepResults []StepResult
	TotalCost   float64
	Duration    time.Duration
}

// composeInput builds the message delivered to a step given prior results.
// The first step receives the raw task; every subsequent step gets the task
// plus a structured summary of everything done before it.
func composeInput(task string, prior []StepResult, currentName string) string {
	if len(prior) == 0 {
		return task
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Task\n%s\n\n## Work completed so far\n", task)

	for _, r := range prior {
		if r.Err != nil {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n%s\n", r.Name, r.Output)
	}

	fmt.Fprintf(&b,
		"\n---\nYou are the **%s**. Build on the work above — apply your expertise without repeating what has already been done.",
		currentName,
	)
	return b.String()
}

// headerLine returns a fixed-width visual separator with a label.
func headerLine(label string) string {
	const width = 50
	if label == "" {
		return strings.Repeat("─", width)
	}
	padding := width - len(label) - 2
	if padding < 1 {
		padding = 1
	}
	return fmt.Sprintf("── %s %s", label, strings.Repeat("─", padding))
}

// stepRef is the shared build helper called by all three orchestration types.
func buildAgent(builder Builder, step AgentStep) *core.Agent {
	return builder(step.Preset)
}
