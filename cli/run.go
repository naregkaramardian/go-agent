package cli

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start an interactive multi-turn REPL session with the agent",
	Long: `Opens an interactive prompt where each input is sent to the agent.
The conversation history is preserved across turns. Press Ctrl+D (EOF) to exit.`,
	SilenceUsage: true,
	RunE:         runREPL,
}

func runREPL(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	ctx, deps, err := bootstrap(ctx)
	if err != nil {
		return err
	}
	defer deps.cleanup()

	if deps.agent == nil {
		deps.logger.WarnContext(ctx, "no API key found — exiting")
		fmt.Fprintln(os.Stderr, "Error: no API key found. Set ANTHROPIC_API_KEY or OPENAI_API_KEY.")
		fmt.Fprintln(os.Stderr, "Use --provider to specify which one to use.")
		return fmt.Errorf("no API key found")
	}

	// Start a trace for the entire REPL session.
	ctx, span := otel.Tracer("go-agent").Start(ctx, "cli.run.session",
		trace.WithAttributes(),
	)
	defer span.End()

	sc := span.SpanContext()
	model, _, _, _ := resolveAgentConfig(deps.provider)
	agentTypeLabel := flags.agentType
	if agentTypeLabel == "" {
		agentTypeLabel = "custom"
	}
	fmt.Printf("goagent interactive session\n")
	fmt.Printf("  Agent ID   : %s\n", deps.agentID)
	fmt.Printf("  Trace ID   : %s\n", sc.TraceID().String())
	fmt.Printf("  Provider   : %s\n", deps.provider)
	fmt.Printf("  Model      : %s\n", model)
	fmt.Printf("  Agent type : %s\n", agentTypeLabel)
	fmt.Printf("  Metrics  : http://localhost%s/metrics\n", flags.metricsAddr)
	fmt.Println()
	fmt.Println("Type your message and press Enter. Ctrl+D to exit.")
	fmt.Println(strings.Repeat("─", 60))

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\nYou: ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fmt.Print("\nAgent: ")
		result, runErr := deps.agent.RunStreaming(ctx, line, os.Stdout)
		if runErr != nil {
			deps.logger.WarnContext(ctx, "agent.run.error", slog.String("error", runErr.Error()))
			fmt.Fprintf(os.Stderr, "\n[error] %v\n", runErr)
			continue
		}
		fmt.Printf("\n\n[steps: %d | cost: $%.6f | %s]\n",
			result.Steps, result.Cost, result.Duration.Round(1000000))
	}

	if scanErr := scanner.Err(); scanErr != nil {
		deps.logger.WarnContext(ctx, "cli.run.scanner.error", slog.String("error", scanErr.Error()))
	}

	printSummary(deps.ledger)
	return nil
}
