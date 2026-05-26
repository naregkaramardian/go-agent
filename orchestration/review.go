package orchestration

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/nareg/goagent/observability"
)

// ReviewLoop runs a worker/reviewer cycle until the reviewer approves the
// output or the maximum number of rounds is exhausted.
//
// Round N:
//  1. Worker receives the task (round 1) or task + reviewer feedback (round N>1).
//  2. Reviewer receives the worker's output and decides: APPROVED or gives feedback.
//
// The reviewer signals approval by including the word "APPROVED" (case-insensitive)
// anywhere in its response. Any other response is treated as feedback for the
// next worker round.
type ReviewLoop struct {
	Worker    AgentStep
	Reviewer  AgentStep
	MaxRounds int
	Builder   Builder
}

// Run executes the review loop and returns when approved or rounds exhausted.
func (r *ReviewLoop) Run(ctx context.Context, task string, w io.Writer) (*Result, error) {
	ctx, span := observability.StartSpan(ctx, "orchestration.reviewloop",
		attribute.String("worker", r.Worker.Name),
		attribute.String("reviewer", r.Reviewer.Name),
		attribute.Int("max_rounds", r.MaxRounds),
	)
	defer span.End()

	observability.OrchestrationRunsTotal.WithLabelValues("reviewloop", "started").Inc()
	start := time.Now()

	maxRounds := r.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 3
	}

	var stepResults []StepResult
	var totalCost float64
	var workerOutput string
	var feedback string

	for round := 1; round <= maxRounds; round++ {
		// ── Worker turn ──────────────────────────────────────────────────────
		workerLabel := fmt.Sprintf("%s (round %d/%d)", r.Worker.Name, round, maxRounds)
		fmt.Fprintf(w, "\n%s\n\n", headerLine(workerLabel))

		ctx, workerSpan := observability.StartSpan(ctx, "orchestration.reviewloop.worker",
			attribute.Int("round", round),
		)

		workerInput := buildWorkerInput(task, feedback, r.Worker.Name, round)
		workerAgent := buildAgent(r.Builder, r.Worker)

		workerStart := time.Now()
		workerResult, workerErr := workerAgent.RunStreaming(ctx, workerInput, w)
		workerDur := time.Since(workerStart)

		wsr := StepResult{Name: workerLabel, Duration: workerDur, Err: workerErr}
		if workerResult != nil {
			wsr.Output = workerResult.Output
			wsr.Steps = workerResult.Steps
			wsr.Cost = workerResult.Cost
			totalCost += workerResult.Cost
			workerOutput = workerResult.Output
		}
		stepResults = append(stepResults, wsr)
		observability.OrchestrationStepDuration.
			WithLabelValues("reviewloop", r.Worker.Name).
			Observe(workerDur.Seconds())
		workerSpan.End()

		if workerErr != nil {
			observability.OrchestrationRunsTotal.WithLabelValues("reviewloop", "error").Inc()
			return buildReviewResult(workerOutput, stepResults, totalCost, start), workerErr
		}
		fmt.Fprintln(w)

		// ── Reviewer turn ─────────────────────────────────────────────────────
		reviewerLabel := fmt.Sprintf("%s (round %d/%d)", r.Reviewer.Name, round, maxRounds)
		fmt.Fprintf(w, "\n%s\n\n", headerLine(reviewerLabel))

		ctx, reviewerSpan := observability.StartSpan(ctx, "orchestration.reviewloop.reviewer",
			attribute.Int("round", round),
		)

		reviewerInput := buildReviewerInput(task, workerOutput, r.Reviewer.Name, round)
		reviewerAgent := buildAgent(r.Builder, r.Reviewer)

		reviewerStart := time.Now()
		reviewerResult, reviewerErr := reviewerAgent.RunStreaming(ctx, reviewerInput, w)
		reviewerDur := time.Since(reviewerStart)

		rsr := StepResult{Name: reviewerLabel, Duration: reviewerDur, Err: reviewerErr}
		if reviewerResult != nil {
			rsr.Output = reviewerResult.Output
			rsr.Steps = reviewerResult.Steps
			rsr.Cost = reviewerResult.Cost
			totalCost += reviewerResult.Cost
		}
		stepResults = append(stepResults, rsr)
		observability.OrchestrationStepDuration.
			WithLabelValues("reviewloop", r.Reviewer.Name).
			Observe(reviewerDur.Seconds())
		reviewerSpan.End()

		if reviewerErr != nil {
			observability.OrchestrationRunsTotal.WithLabelValues("reviewloop", "error").Inc()
			return buildReviewResult(workerOutput, stepResults, totalCost, start), reviewerErr
		}
		fmt.Fprintln(w)

		// Check for approval.
		if strings.Contains(strings.ToUpper(reviewerResult.Output), "APPROVED") {
			fmt.Fprintf(w, "\n%s\n", headerLine("Review Complete — APPROVED"))
			observability.OrchestrationRunsTotal.WithLabelValues("reviewloop", "approved").Inc()
			observability.OrchestrationDuration.WithLabelValues("reviewloop").Observe(time.Since(start).Seconds())
			return buildReviewResult(workerOutput, stepResults, totalCost, start), nil
		}

		feedback = reviewerResult.Output
	}

	// Rounds exhausted without approval.
	fmt.Fprintf(w, "\n%s\n", headerLine("Review Complete — max rounds reached"))
	observability.OrchestrationRunsTotal.WithLabelValues("reviewloop", "max_rounds").Inc()
	observability.OrchestrationDuration.WithLabelValues("reviewloop").Observe(time.Since(start).Seconds())
	return buildReviewResult(workerOutput, stepResults, totalCost, start), nil
}

func buildWorkerInput(task, feedback, name string, round int) string {
	if round == 1 || feedback == "" {
		return fmt.Sprintf("## Task\n%s\n\nYou are the **%s**. Complete the task above.", task, name)
	}
	return fmt.Sprintf(
		"## Task\n%s\n\n## Reviewer feedback (round %d)\n%s\n\n"+
			"You are the **%s**. Revise your work based on the feedback above.",
		task, round-1, feedback, name,
	)
}

func buildReviewerInput(task, workerOutput, name string, round int) string {
	return fmt.Sprintf(
		"## Original task\n%s\n\n## Work submitted (round %d)\n%s\n\n"+
			"You are the **%s**. Review the submitted work.\n"+
			"If it meets quality standards, respond with APPROVED at the start.\n"+
			"Otherwise, provide specific, actionable feedback for improvement.",
		task, round, workerOutput, name,
	)
}

func buildReviewResult(output string, steps []StepResult, cost float64, start time.Time) *Result {
	return &Result{
		Output:      output,
		StepResults: steps,
		TotalCost:   cost,
		Duration:    time.Since(start),
	}
}
