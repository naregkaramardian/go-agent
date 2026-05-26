package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/nareg/goagent/core"
	"github.com/nareg/goagent/guardrails"
	"github.com/nareg/goagent/llm"
	"github.com/nareg/goagent/observability"
	"github.com/nareg/goagent/tools"
	"github.com/nareg/goagent/tools/builtin"
)

const (
	serviceName = "go-agent"
	version     = "0.1.0"

	providerAnthropic = "anthropic"
	providerOpenAI    = "openai"
)

// defaultModel returns a sensible default model for the given provider.
func defaultModel(provider string) string {
	if provider == providerOpenAI {
		return "gpt-4o-mini"
	}
	return "claude-haiku-4-5"
}

type globalFlags struct {
	logFormat    string
	metricsAddr  string
	otlpEndpoint string
	provider     string // "anthropic" | "openai" | "" (auto-detect)
	model        string
	system       string
	maxSteps     int
	maxTokens    int
	costBudget   float64
	tokenBudget  int
}

var flags globalFlags

var rootCmd = &cobra.Command{
	Use:   "goagent",
	Short: "A production-grade Go AI agent runtime",
	Long: `goagent is a pure-Go AI agent with tool use, memory, guardrails,
and full observability. Run 'goagent run' for an interactive REPL or
'goagent ask <message>' for a single-turn query.

Provider auto-detection (checked in order):
  1. --provider flag (explicit)
  2. ANTHROPIC_API_KEY env var → anthropic
  3. OPENAI_API_KEY env var    → openai`,
}

func init() {
	f := rootCmd.PersistentFlags()
	f.StringVar(&flags.logFormat, "log-format", "text", "Log format: text|json")
	f.StringVar(&flags.metricsAddr, "metrics-addr", ":9090", "Address for Prometheus /metrics endpoint")
	f.StringVar(&flags.otlpEndpoint, "otlp-endpoint", "", "OTLP collector endpoint (empty = stdout exporter in dev)")
	f.StringVar(&flags.provider, "provider", "", "LLM provider: anthropic|openai (default: auto-detect from env)")
	f.StringVar(&flags.model, "model", "", "Model ID (default depends on provider: gpt-4o-mini or claude-haiku-4-5)")
	f.StringVar(&flags.system, "system", "You are a helpful AI assistant. Be concise and accurate.", "Agent system prompt")
	f.IntVar(&flags.maxSteps, "max-steps", 20, "Maximum agent loop iterations per run")
	f.IntVar(&flags.maxTokens, "max-tokens", 2048, "Max tokens per LLM response")
	f.Float64Var(&flags.costBudget, "cost-budget", 1.0, "USD cost budget per run (0 = unlimited)")
	f.IntVar(&flags.tokenBudget, "token-budget", 100_000, "Token budget for conversation context")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(askCmd)
}

// Execute is the CLI entry point called from main.
func Execute() {
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		os.Exit(1)
	}
}

// infraDeps holds the runtime infrastructure created during bootstrap.
type infraDeps struct {
	logger   *slog.Logger
	ledger   *observability.CostLedger
	agent    *core.Agent
	agentID  string
	provider string
	cleanup  func()
}

// bootstrap initialises all infrastructure and wires up the agent.
// The caller must call cleanup() when done, even on error.
func bootstrap(ctx context.Context) (context.Context, *infraDeps, error) {
	logger := observability.NewLogger(flags.logFormat)
	slog.SetDefault(logger)
	ctx = observability.WithLogger(ctx, logger)

	if flags.otlpEndpoint != "" {
		os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", flags.otlpEndpoint)
	}

	shutdown, err := observability.InitTracer(ctx, serviceName, version)
	if err != nil {
		return ctx, nil, fmt.Errorf("cli.bootstrap: tracer: %w", err)
	}

	ledger := observability.NewCostLedger()
	ctx = observability.WithCostLedger(ctx, ledger)

	observability.ServeMetrics(flags.metricsAddr)
	logger.InfoContext(ctx, "metrics.started", slog.String("addr", flags.metricsAddr))

	cleanup := func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutErr := shutdown(shutCtx); shutErr != nil {
			logger.Error("tracer.shutdown", slog.String("error", shutErr.Error()))
		}
	}

	provider, apiKey, err := resolveProvider()
	if err != nil {
		// No key available — return deps without an agent; callers handle this.
		return ctx, &infraDeps{logger: logger, ledger: ledger, cleanup: cleanup}, nil
	}

	model := flags.model
	if model == "" {
		model = defaultModel(provider)
	}

	// NewAnthropicClient / NewOpenAIClient already prepend ObserveLLM() internally.
	var client llm.LLMClient
	switch provider {
	case providerOpenAI:
		client = llm.NewOpenAIClient(llm.OpenAIConfig{APIKey: apiKey})
	default:
		client = llm.NewAnthropicClient(llm.AnthropicConfig{APIKey: apiKey})
	}

	logger.InfoContext(ctx, "provider.selected",
		slog.String("provider", provider),
		slog.String("model", model),
	)

	buf := core.NewConversationBuffer(flags.tokenBudget)
	agent := core.NewAgent(core.AgentConfig{
		Model:     model,
		System:    flags.system,
		MaxSteps:  flags.maxSteps,
		MaxTokens: flags.maxTokens,
	}, client, buildRegistry(), buf)

	agent.Use(
		guardrails.MaxSteps(flags.maxSteps),
		guardrails.TokenBudget(flags.tokenBudget),
		guardrails.LoopDetection(5),
	)
	if flags.costBudget > 0 {
		agent.Use(guardrails.CostBudget(flags.costBudget))
	}

	deps := &infraDeps{
		logger:   logger,
		ledger:   ledger,
		agent:    agent,
		agentID:  agent.ID(),
		provider: provider,
		cleanup:  cleanup,
	}
	return ctx, deps, nil
}

// resolveProvider returns (provider, apiKey, error).
// Priority: --provider flag → ANTHROPIC_API_KEY → OPENAI_API_KEY.
func resolveProvider() (string, string, error) {
	switch flags.provider {
	case providerAnthropic:
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return "", "", fmt.Errorf("--provider=anthropic but ANTHROPIC_API_KEY is not set")
		}
		return providerAnthropic, key, nil
	case providerOpenAI:
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return "", "", fmt.Errorf("--provider=openai but OPENAI_API_KEY is not set")
		}
		return providerOpenAI, key, nil
	default:
		// Auto-detect: prefer Anthropic, fall back to OpenAI.
		if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
			return providerAnthropic, key, nil
		}
		if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			return providerOpenAI, key, nil
		}
		return "", "", fmt.Errorf("no API key found (set ANTHROPIC_API_KEY or OPENAI_API_KEY)")
	}
}

// buildRegistry creates the tool registry with all built-in tools.
func buildRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	if bash, err := builtin.NewBashTool(); err == nil {
		reg.Register(bash)
	}
	if h, err := builtin.NewHTTPTool(); err == nil {
		reg.Register(h)
	}
	if fr, err := builtin.NewFileReadTool(); err == nil {
		reg.Register(fr)
	}
	if fw, err := builtin.NewFileWriteTool(); err == nil {
		reg.Register(fw)
	}
	return reg
}

// printSummary prints the cost ledger summary to stdout.
func printSummary(ledger *observability.CostLedger) {
	summary := ledger.Summary()
	if summary.TotalCostUSD == 0 {
		return
	}
	fmt.Println("\n--- Session Summary ---")
	for model, u := range summary.ByModel {
		fmt.Printf("  %-30s  %6d in + %5d out tokens  $%.6f\n",
			model, u.InputTokens, u.OutputTokens, u.CostUSD)
	}
	fmt.Printf("  Total cost: $%.6f\n", summary.TotalCostUSD)
}
