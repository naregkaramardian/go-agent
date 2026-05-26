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
	Use:          "run",
	Short:        "Start an interactive chat session with the agent",
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
		fmt.Fprintln(os.Stderr, "Error: no API key found. Set ANTHROPIC_API_KEY or OPENAI_API_KEY.")
		fmt.Fprintln(os.Stderr, "Use --provider to specify which one to use.")
		return fmt.Errorf("no API key found")
	}

	ctx, span := otel.Tracer("go-agent").Start(ctx, "cli.run.session",
		trace.WithAttributes(),
	)
	defer span.End()

	model, _, _, _ := resolveAgentConfig(deps.provider)
	agentLabel := flags.agentType
	if agentLabel == "" {
		agentLabel = model
	}

	// ── Header ────────────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stdout, "goagent · %s · %s\n", agentLabel, deps.provider)
	fmt.Fprintln(os.Stdout, "Ctrl+D or /exit to quit")
	fmt.Fprintln(os.Stdout, strings.Repeat("─", 50))
	fmt.Fprintln(os.Stdout)

	if flags.logFile != "" {
		fmt.Fprintf(os.Stdout, "  logs → %s\n\n", flags.logFile)
	}

	// ── Conversation loop ──────────────────────────────────────────────────────
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(os.Stdout, "You: ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			break
		}

		fmt.Fprint(os.Stdout, "\nAgent: ")
		result, runErr := deps.agent.RunStreaming(ctx, line, os.Stdout)
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout)

		if runErr != nil {
			deps.logger.WarnContext(ctx, "agent.run.error", slog.String("error", runErr.Error()))
			fmt.Fprintf(os.Stderr, "Error: %v\n\n", runErr)
			continue
		}

		if flags.verbose {
			fmt.Fprintf(os.Stdout, "  [%d step(s) · $%.6f · %s]\n\n",
				result.Steps, result.Cost, result.Duration.Round(1_000_000))
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		deps.logger.WarnContext(ctx, "cli.run.scanner.error", slog.String("error", scanErr.Error()))
	}

	// ── Exit summary ──────────────────────────────────────────────────────────
	fmt.Fprintln(os.Stdout, strings.Repeat("─", 50))
	printSummary(deps.ledger)
	return nil
}
