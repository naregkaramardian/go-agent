package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nareg/goagent/orchestration"
)

var orchestrateWorkflow string

var orchestrateCmd = &cobra.Command{
	Use:   "orchestrate",
	Short: "Run a multi-agent workflow",
	Long: `Run a named multi-agent workflow against a task description.

Available sub-commands:
  goagent orchestrate list              — list all pre-wired workflows
  goagent orchestrate run --workflow <id> "<task>"

Workflow IDs:
  code-review      senior engineer writes; code reviewer approves or requests changes
  feature-build    architect → engineer → test engineer (sequential pipeline)
  security-audit   security + code review in parallel; engineer synthesises findings
  full-pipeline    architect → (engineer + DB specialist) → security audit → test plan`,
	SilenceUsage: true,
}

var orchestrateListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List available multi-agent workflows",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tDESCRIPTION")
		fmt.Fprintln(tw, "──\t────\t───────────")
		for _, wf := range orchestration.AllWorkflows() {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", wf.ID, wf.Name, wf.Description)
		}
		return tw.Flush()
	},
}

var orchestrateRunCmd = &cobra.Command{
	Use:          "run [task...]",
	Short:        "Run a workflow against a task",
	SilenceUsage: true,
	RunE:         runOrchestrate,
}

func init() {
	orchestrateRunCmd.Flags().StringVar(&orchestrateWorkflow, "workflow", "", "Workflow ID to run (required)")

	orchestrateCmd.AddCommand(orchestrateListCmd)
	orchestrateCmd.AddCommand(orchestrateRunCmd)
}

func runOrchestrate(cmd *cobra.Command, args []string) error {
	if orchestrateWorkflow == "" {
		return fmt.Errorf("--workflow is required (run 'goagent orchestrate list' to see options)")
	}

	ctx := cmd.Context()
	ctx, deps, err := bootstrap(ctx)
	if err != nil {
		return err
	}
	defer deps.cleanup()

	if deps.builder == nil {
		fmt.Fprintln(os.Stderr, "Error: no API key found. Set ANTHROPIC_API_KEY or OPENAI_API_KEY.")
		return fmt.Errorf("no API key found")
	}

	wf, ok := orchestration.WorkflowByID(orchestrateWorkflow)
	if !ok {
		return fmt.Errorf("unknown workflow %q — run 'goagent orchestrate list' to see options", orchestrateWorkflow)
	}

	// Collect task from args or stdin.
	var task string
	if len(args) > 0 {
		task = strings.Join(args, " ")
	} else {
		var sb strings.Builder
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			sb.WriteString(scanner.Text())
			sb.WriteString("\n")
		}
		if scanErr := scanner.Err(); scanErr != nil {
			return fmt.Errorf("cli.orchestrate: reading stdin: %w", scanErr)
		}
		task = strings.TrimSpace(sb.String())
	}

	if task == "" {
		return fmt.Errorf("cli.orchestrate: no task provided")
	}

	fmt.Fprintf(os.Stdout, "workflow: %s  ·  %s\n", wf.Name, deps.provider)
	fmt.Fprintln(os.Stdout, strings.Repeat("─", 50))

	runner := wf.Build(deps.builder)
	result, runErr := runner.Run(ctx, task, os.Stdout)

	// Always print cost summary.
	if result != nil && result.TotalCost > 0 {
		fmt.Fprintf(os.Stdout, "\n%s\n", strings.Repeat("─", 50))
		fmt.Fprintf(os.Stdout, "Total cost: $%.6f  ·  %s\n",
			result.TotalCost, result.Duration.Round(1_000_000))
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", runErr)
		return runErr
	}
	return nil
}
