package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/nareg/goagent/agents"
)

// Runner is the common interface satisfied by Pipeline, Parallel, and ReviewLoop.
type Runner interface {
	Run(ctx context.Context, task string, w io.Writer) (*Result, error)
}

// Workflow is a named, pre-wired orchestration configuration.
type Workflow struct {
	// ID is the value passed to --workflow on the CLI.
	ID string
	// Name is the human-readable display name.
	Name string
	// Description is shown in 'goagent orchestrate list'.
	Description string
	// Build returns a fully configured Runner ready to call Run(ctx, task, w).
	Build func(b Builder) Runner
}

// AllWorkflows returns every pre-wired workflow.
func AllWorkflows() []Workflow {
	return []Workflow{
		CodeReviewWorkflow(),
		FeatureBuildWorkflow(),
		SecurityAuditWorkflow(),
		FullPipelineWorkflow(),
	}
}

// WorkflowByID looks up a workflow by ID.
func WorkflowByID(id string) (Workflow, bool) {
	for _, w := range AllWorkflows() {
		if w.ID == id {
			return w, true
		}
	}
	return Workflow{}, false
}

// ─────────────────────────────────────────────────────────────────────────────
// Pre-wired workflows
// ─────────────────────────────────────────────────────────────────────────────

// CodeReviewWorkflow pairs a senior engineer (author) with a code reviewer
// in a ReviewLoop: the reviewer approves or requests changes up to 3 rounds.
func CodeReviewWorkflow() Workflow {
	return Workflow{
		ID:          "code-review",
		Name:        "Code Review",
		Description: "Senior engineer writes; code reviewer approves or requests changes (up to 3 rounds).",
		Build: func(b Builder) Runner {
			return &ReviewLoop{
				Worker:    AgentStep{Name: "Senior Engineer", Preset: agents.SeniorEngineer()},
				Reviewer:  AgentStep{Name: "Code Reviewer", Preset: agents.CodeReviewer()},
				MaxRounds: 3,
				Builder:   b,
			}
		},
	}
}

// FeatureBuildWorkflow runs architect → engineer → test engineer sequentially.
func FeatureBuildWorkflow() Workflow {
	return Workflow{
		ID:          "feature-build",
		Name:        "Feature Build",
		Description: "Architect designs, senior engineer implements, test engineer writes tests.",
		Build: func(b Builder) Runner {
			return &Pipeline{
				Steps: []AgentStep{
					{Name: "Software Architect", Preset: agents.SoftwareArchitect()},
					{Name: "Senior Engineer", Preset: agents.SeniorEngineer()},
					{Name: "Test Engineer", Preset: agents.TestEngineer()},
				},
				Builder: b,
			}
		},
	}
}

// SecurityAuditWorkflow fans out a security reviewer and code reviewer in
// parallel, then a senior engineer synthesises the combined findings.
func SecurityAuditWorkflow() Workflow {
	return Workflow{
		ID:          "security-audit",
		Name:        "Security Audit",
		Description: "Security reviewer + code reviewer run in parallel; senior engineer synthesises findings.",
		Build: func(b Builder) Runner {
			return &securityAuditRunner{builder: b}
		},
	}
}

// FullPipelineWorkflow is the full-team workflow:
//  1. Architect designs the solution.
//  2. Engineer + DB specialist work in parallel.
//  3. Security reviewer audits the combined output.
//  4. Test engineer writes the test plan.
func FullPipelineWorkflow() Workflow {
	return Workflow{
		ID:          "full-pipeline",
		Name:        "Full Pipeline",
		Description: "Architect → (engineer + DB specialist in parallel) → security audit → test plan.",
		Build: func(b Builder) Runner {
			return &fullPipelineRunner{builder: b}
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Composite runners (multi-stage workflows not expressible as a single primitive)
// ─────────────────────────────────────────────────────────────────────────────

// securityAuditRunner: parallel review then engineer synthesis.
type securityAuditRunner struct{ builder Builder }

func (r *securityAuditRunner) Run(ctx context.Context, task string, w io.Writer) (*Result, error) {
	// Stage 1: parallel security + code review.
	parallel := &Parallel{
		Steps: []AgentStep{
			{Name: "Security Reviewer", Preset: agents.SecurityReviewer()},
			{Name: "Code Reviewer", Preset: agents.CodeReviewer()},
		},
		Builder: r.builder,
	}
	stage1, err := parallel.Run(ctx, task, w)
	if err != nil {
		return stage1, err
	}

	// Stage 2: engineer synthesises both review reports.
	fmt.Fprintf(w, "\n%s\n\n", headerLine("Senior Engineer — Remediation"))
	var engineerBuf bytes.Buffer
	synthInput := fmt.Sprintf(
		"## Original task\n%s\n\n## Review findings\n%s\n\n"+
			"You are the **Senior Engineer**. Summarise the key findings and produce a prioritised remediation plan.",
		task, stage1.Output,
	)
	agent := buildAgent(r.builder, AgentStep{Name: "Senior Engineer", Preset: agents.SeniorEngineer()})
	synthResult, synthErr := agent.RunStreaming(ctx, synthInput, io.MultiWriter(w, &engineerBuf))
	fmt.Fprintln(w)

	combined := &Result{
		StepResults: stage1.StepResults,
		TotalCost:   stage1.TotalCost,
		Duration:    stage1.Duration,
	}
	if synthResult != nil {
		combined.Output = synthResult.Output
		combined.TotalCost += synthResult.Cost
		combined.Duration += synthResult.Duration
		combined.StepResults = append(combined.StepResults, StepResult{
			Name:     "Senior Engineer",
			Output:   synthResult.Output,
			Steps:    synthResult.Steps,
			Cost:     synthResult.Cost,
			Duration: synthResult.Duration,
		})
	}
	return combined, synthErr
}

// fullPipelineRunner: architect → parallel (engineer + db) → security → tests.
type fullPipelineRunner struct{ builder Builder }

func (r *fullPipelineRunner) Run(ctx context.Context, task string, w io.Writer) (*Result, error) {
	var allStepResults []StepResult
	var totalCost float64

	// Stage 1: architect.
	fmt.Fprintf(w, "\n%s\n\n", headerLine("Software Architect"))
	archAgent := buildAgent(r.builder, AgentStep{Name: "Software Architect", Preset: agents.SoftwareArchitect()})
	archResult, err := archAgent.RunStreaming(ctx, task, w)
	fmt.Fprintln(w)
	if err != nil {
		return &Result{TotalCost: totalCost}, err
	}
	allStepResults = append(allStepResults, StepResult{
		Name: "Software Architect", Output: archResult.Output,
		Steps: archResult.Steps, Cost: archResult.Cost, Duration: archResult.Duration,
	})
	totalCost += archResult.Cost
	archSummary := archResult.Output

	// Stage 2: engineer + DB specialist in parallel on the architecture output.
	parallelTask := fmt.Sprintf("## Architecture Design\n%s\n\n## Original task\n%s", archSummary, task)
	parallel := &Parallel{
		Steps: []AgentStep{
			{Name: "Senior Engineer", Preset: agents.SeniorEngineer()},
			{Name: "DB Specialist", Preset: agents.DBDesignSpecialist()},
		},
		Builder: r.builder,
	}
	stage2, err := parallel.Run(ctx, parallelTask, w)
	if stage2 != nil {
		allStepResults = append(allStepResults, stage2.StepResults...)
		totalCost += stage2.TotalCost
	}
	if err != nil {
		return &Result{Output: stage2.Output, StepResults: allStepResults, TotalCost: totalCost}, err
	}

	// Stage 3: security reviewer audits everything so far.
	fmt.Fprintf(w, "\n%s\n\n", headerLine("Security Reviewer"))
	secInput := composeInput(task, allStepResults, "Security Reviewer")
	secAgent := buildAgent(r.builder, AgentStep{Name: "Security Reviewer", Preset: agents.SecurityReviewer()})
	secResult, err := secAgent.RunStreaming(ctx, secInput, w)
	fmt.Fprintln(w)
	if err == nil && secResult != nil {
		allStepResults = append(allStepResults, StepResult{
			Name: "Security Reviewer", Output: secResult.Output,
			Steps: secResult.Steps, Cost: secResult.Cost, Duration: secResult.Duration,
		})
		totalCost += secResult.Cost
	}
	if err != nil {
		return &Result{StepResults: allStepResults, TotalCost: totalCost}, err
	}

	// Stage 4: test engineer writes the test plan.
	fmt.Fprintf(w, "\n%s\n\n", headerLine("Test Engineer"))
	testInput := composeInput(task, allStepResults, "Test Engineer")
	testAgent := buildAgent(r.builder, AgentStep{Name: "Test Engineer", Preset: agents.TestEngineer()})
	testResult, err := testAgent.RunStreaming(ctx, testInput, w)
	fmt.Fprintln(w)

	var finalOutput string
	if testResult != nil {
		allStepResults = append(allStepResults, StepResult{
			Name: "Test Engineer", Output: testResult.Output,
			Steps: testResult.Steps, Cost: testResult.Cost, Duration: testResult.Duration,
		})
		totalCost += testResult.Cost
		finalOutput = testResult.Output
	}

	return &Result{
		Output:      finalOutput,
		StepResults: allStepResults,
		TotalCost:   totalCost,
	}, err
}
