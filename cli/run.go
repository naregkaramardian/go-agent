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
		deps.logger.WarnContext(ctx, "no ANTHROPIC_API_KEY set — exiting")
		fmt.Fprintln(os.Stderr, "Error: ANTHROPIC_API_KEY environment variable is required for 'run'.")
		fmt.Fprintln(os.Stderr, "Set it and try again, or use 'goagent ask' for a one-shot query.")
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	// Start a trace for the entire REPL session.
	ctx, span := otel.Tracer("go-agent").Start(ctx, "cli.run.session",
		trace.WithAttributes(),
	)
	defer span.End()

	sc := span.SpanContext()
	fmt.Printf("goagent interactive session\n")
	fmt.Printf("  Agent ID : %s\n", deps.agentID)
	fmt.Printf("  Trace ID : %s\n", sc.TraceID().String())
	fmt.Printf("  Model    : %s\n", flags.model)
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
