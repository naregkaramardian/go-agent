package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nareg/goagent/agents"
)

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "Manage and list available agent presets",
}

var agentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available agent presets",
	RunE: func(cmd *cobra.Command, _ []string) error {
		provider, _, _ := resolveProvider()
		if provider == "" {
			provider = "anthropic"
		}

		fmt.Printf("%-20s  %-12s  %-14s  %s\n", "ID", "TIER", "DEFAULT MODEL", "DESCRIPTION")
		fmt.Printf("%-20s  %-12s  %-14s  %s\n",
			"────────────────────", "────────────", "──────────────", "───────────────────────────────────────────────")

		for _, p := range agents.All() {
			model := agents.ModelForTier(provider, p.RecommendedTier)
			fmt.Printf("%-20s  %-12s  %-14s  %s\n",
				p.ID, string(p.RecommendedTier), model, p.Description)
		}

		fmt.Printf("\nUsage: goagent run --agent-type <ID>\n")
		fmt.Printf("       goagent ask --agent-type <ID> \"<message>\"\n")
		return nil
	},
}

func init() {
	agentsCmd.AddCommand(agentsListCmd)
	rootCmd.AddCommand(agentsCmd)
}
