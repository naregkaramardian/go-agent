package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/nareg/goagent/observability"
)

// Parallel runs all steps concurrently on the same input task.
// Each step streams into its own buffer; results are printed in declaration
// order once all goroutines finish.
type Parallel struct {
	Steps   []AgentStep
	Builder Builder
}

// Run fans out the task to every step simultaneously and returns when all
// steps complete. The combined output is written to w in declaration order.
func (p *Parallel) Run(ctx context.Context, task string, w io.Writer) (*Result, error) {
	ctx, span := observability.StartSpan(ctx, "orchestration.parallel",
		attribute.Int("steps", len(p.Steps)),
	)
	defer span.End()

	observability.OrchestrationRunsTotal.WithLabelValues("parallel", "started").Inc()
	start := time.Now()

	type item struct {
		idx int
		buf bytes.Buffer
		sr  StepResult
	}

	items := make([]item, len(p.Steps))
	for i := range items {
		items[i].idx = i
	}

	var wg sync.WaitGroup
	for i, step := range p.Steps {
		wg.Add(1)
		go func(i int, step AgentStep) {
			defer wg.Done()

			ctx, stepSpan := observability.StartSpan(ctx, "orchestration.parallel.step",
				attribute.String("step.name", step.Name),
				attribute.Int("step.index", i),
			)
			defer stepSpan.End()

			stepStart := time.Now()
			agent := buildAgent(p.Builder, step)
			result, err := agent.RunStreaming(ctx, task, &items[i].buf)
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
			}
			items[i].sr = sr

			observability.OrchestrationStepDuration.
				WithLabelValues("parallel", step.Name).
				Observe(stepDur.Seconds())
		}(i, step)
	}
	wg.Wait()

	// Collect and print in order.
	stepResults := make([]StepResult, len(p.Steps))
	var totalCost float64
	var merged bytes.Buffer
	var firstErr error

	for i := range items {
		sr := items[i].sr
		stepResults[i] = sr
		totalCost += sr.Cost

		fmt.Fprintf(w, "\n%s\n\n", headerLine(sr.Name))
		w.Write(items[i].buf.Bytes()) //nolint:errcheck
		fmt.Fprintln(w)

		merged.WriteString(sr.Output)
		merged.WriteString("\n\n")

		if sr.Err != nil && firstErr == nil {
			firstErr = fmt.Errorf("orchestration.Parallel: step %q: %w", sr.Name, sr.Err)
		}
	}

	dur := time.Since(start)
	status := "ok"
	if firstErr != nil {
		status = "error"
	}
	observability.OrchestrationRunsTotal.WithLabelValues("parallel", status).Inc()
	observability.OrchestrationDuration.WithLabelValues("parallel").Observe(dur.Seconds())

	return &Result{
		Output:      merged.String(),
		StepResults: stepResults,
		TotalCost:   totalCost,
		Duration:    dur,
	}, firstErr
}
