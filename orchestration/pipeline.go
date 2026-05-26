package orchestration

import (
	"context"
	"fmt"
	"io"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/nareg/goagent/observability"
)

// Pipeline runs a sequence of agent steps where each step receives the
// accumulated outputs of all prior steps alongside the original task.
// Steps execute serially; each step's streamed output is written to w.
type Pipeline struct {
	Steps   []AgentStep
	Builder Builder
}

// Run executes every step in order and returns the combined result.
// w receives the live output of each step as it streams.
func (p *Pipeline) Run(ctx context.Context, task string, w io.Writer) (*Result, error) {
	ctx, span := observability.StartSpan(ctx, "orchestration.pipeline",
		attribute.Int("steps", len(p.Steps)),
	)
	defer span.End()

	observability.OrchestrationRunsTotal.WithLabelValues("pipeline", "started").Inc()
	start := time.Now()

	var stepResults []StepResult
	var totalCost float64
	var lastOutput string

	for i, step := range p.Steps {
		fmt.Fprintf(w, "\n%s\n\n", headerLine(step.Name))

		ctx, stepSpan := observability.StartSpan(ctx, "orchestration.pipeline.step",
			attribute.String("step.name", step.Name),
			attribute.Int("step.index", i),
		)

		stepStart := time.Now()
		agent := buildAgent(p.Builder, step)
		input := composeInput(task, stepResults, step.Name)

		result, err := agent.RunStreaming(ctx, input, w)
		stepDur := time.Since(stepStart)

		sr := StepResult{
			Name:     step.Name,
			Duration: stepDur,
			Err:      err,
		}
		if result != nil {
			sr.Output = result.Output
			sr.Steps = result.Steps
			sr.Cost = result.Cost
			totalCost += result.Cost
			lastOutput = result.Output
		}

		stepResults = append(stepResults, sr)
		observability.OrchestrationStepDuration.
			WithLabelValues("pipeline", step.Name).
			Observe(stepDur.Seconds())

		stepSpan.End()

		if err != nil {
			observability.OrchestrationRunsTotal.WithLabelValues("pipeline", "error").Inc()
			return &Result{
				Output:      lastOutput,
				StepResults: stepResults,
				TotalCost:   totalCost,
				Duration:    time.Since(start),
			}, fmt.Errorf("orchestration.Pipeline: step %q: %w", step.Name, err)
		}

		fmt.Fprintln(w)
	}

	dur := time.Since(start)
	observability.OrchestrationRunsTotal.WithLabelValues("pipeline", "ok").Inc()
	observability.OrchestrationDuration.WithLabelValues("pipeline").Observe(dur.Seconds())

	return &Result{
		Output:      lastOutput,
		StepResults: stepResults,
		TotalCost:   totalCost,
		Duration:    dur,
	}, nil
}
