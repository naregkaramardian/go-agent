package cli

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var askCmd = &cobra.Command{
	Use:   "ask [message...]",
	Short: "Ask the agent a single question (non-interactive)",
	Long: `Sends a single message to the agent and prints the response.
If no message is provided, reads from stdin (pipe-friendly).

Examples:
  goagent ask "What is the current date?"
  echo "Summarise this file" | goagent ask`,
	SilenceUsage: true,
	RunE:         runAsk,
}

func runAsk(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	ctx, deps, err := bootstrap(ctx)
	if err != nil {
		return err
	}
	defer deps.cleanup()

	if deps.agent == nil {
		fmt.Fprintln(os.Stderr, "Error: ANTHROPIC_API_KEY environment variable is required.")
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	var message string
	if len(args) > 0 {
		message = strings.Join(args, " ")
	} else {
		// Read from stdin.
		var sb strings.Builder
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			sb.WriteString(scanner.Text())
			sb.WriteString("\n")
		}
		if scanErr := scanner.Err(); scanErr != nil {
			return fmt.Errorf("cli.ask: reading stdin: %w", scanErr)
		}
		message = strings.TrimSpace(sb.String())
	}

	if message == "" {
		return fmt.Errorf("cli.ask: no message provided")
	}

	result, runErr := deps.agent.RunStreaming(ctx, message, os.Stdout)
	if runErr != nil {
		deps.logger.WarnContext(ctx, "agent.run.error", slog.String("error", runErr.Error()))
		fmt.Fprintf(os.Stderr, "\n[error] %v\n", runErr)
		return runErr
	}

	fmt.Printf("\n\n[steps: %d | cost: $%.6f | %s]\n",
		result.Steps, result.Cost, result.Duration.Round(1000000))

	printSummary(deps.ledger)
	return nil
}
