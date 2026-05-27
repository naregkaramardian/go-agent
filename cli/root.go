package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/nareg/goagent/agents"
	"github.com/nareg/goagent/core"
	"github.com/nareg/goagent/guardrails"
	"github.com/nareg/goagent/llm"
	"github.com/nareg/goagent/memory"
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
	envFile      string // path to .env file; "" = disabled
	logFormat    string // "text" | "json"
	logFile      string // write logs here; "" = discard (unless --verbose)
	verbose      bool   // write logs to stderr
	metricsAddr  string
	otlpEndpoint string
	provider     string // "anthropic" | "openai" | "" (auto-detect)
	agentType    string // preset ID from agents package; "" = custom
	model        string
	system       string
	maxSteps     int
	maxTokens    int
	costBudget   float64
	tokenBudget  int
	memoryDSN    string // postgres DSN for pgvector long-term memory; "" = disabled
	embedKey     string // OpenAI API key for embeddings; defaults to OPENAI_API_KEY
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
	f.StringVar(&flags.envFile, "env-file", ".env", "Path to .env file (empty string disables it)")
	f.StringVar(&flags.logFormat, "log-format", "text", "Log format: text|json")
	f.StringVar(&flags.logFile, "log-file", "", "Write logs to this file (default: discard)")
	f.BoolVarP(&flags.verbose, "verbose", "v", false, "Print logs to stderr (overrides --log-file for stderr output)")
	f.StringVar(&flags.metricsAddr, "metrics-addr", ":9090", "Address for Prometheus /metrics endpoint")
	f.StringVar(&flags.otlpEndpoint, "otlp-endpoint", "", "OTLP collector endpoint (empty = stdout exporter in dev)")
	f.StringVar(&flags.provider, "provider", "", "LLM provider: anthropic|openai (default: auto-detect from env)")
	f.StringVar(&flags.agentType, "agent-type", "", "Agent preset ID (see 'goagent agents list'). Overrides --model, --system, --max-steps, --max-tokens.")
	f.StringVar(&flags.model, "model", "", "Model ID (default depends on provider: gpt-4o-mini or claude-haiku-4-5)")
	f.StringVar(&flags.system, "system", "You are a helpful AI assistant. Be concise and accurate.", "Agent system prompt")
	f.IntVar(&flags.maxSteps, "max-steps", 20, "Maximum agent loop iterations per run")
	f.IntVar(&flags.maxTokens, "max-tokens", 2048, "Max tokens per LLM response")
	f.Float64Var(&flags.costBudget, "cost-budget", 1.0, "USD cost budget per run (0 = unlimited)")
	f.IntVar(&flags.tokenBudget, "token-budget", 100_000, "Token budget for conversation context")
	f.StringVar(&flags.memoryDSN, "memory-dsn", "", "PostgreSQL DSN for pgvector long-term memory (e.g. postgres://goagent:goagent@localhost:5432/goagent)")
	f.StringVar(&flags.embedKey, "embed-key", "", "OpenAI API key for embeddings (defaults to OPENAI_API_KEY env var)")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(askCmd)
	rootCmd.AddCommand(orchestrateCmd)
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
	builder  func(agents.Preset) *core.Agent
	mem      *memory.PgVectorMemory // nil when --memory-dsn is not set
	cleanup  func()
}

// bootstrap initialises all infrastructure and wires up the agent.
// The caller must call cleanup() when done, even on error.
func bootstrap(ctx context.Context) (context.Context, *infraDeps, error) {
	// Load .env before anything else so env vars are available to all setup code.
	loadEnvFile(flags.envFile)

	// LOG_FORMAT may come from the .env file, so re-read it after loading.
	if envFmt := os.Getenv("LOG_FORMAT"); envFmt != "" && flags.logFormat == "text" {
		flags.logFormat = envFmt
	}

	logWriter, logCloser := resolveLogWriter()
	logger := observability.NewLoggerTo(flags.logFormat, logWriter)
	slog.SetDefault(logger)
	ctx = observability.WithLogger(ctx, logger)

	if flags.otlpEndpoint != "" {
		os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", flags.otlpEndpoint)
	}

	// In verbose mode, write spans to stderr alongside logs.
	// Otherwise InitTracer uses a no-op exporter (no output).
	var spanWriter io.Writer
	if flags.verbose {
		spanWriter = os.Stderr
	}
	shutdown, err := observability.InitTracer(ctx, serviceName, version, spanWriter)
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
		logCloser()
	}

	provider, apiKey, err := resolveProvider()
	if err != nil {
		// No key available — return deps without an agent; callers handle this.
		return ctx, &infraDeps{logger: logger, ledger: ledger, cleanup: cleanup}, nil
	}

	// Resolve effective config: preset values are used as defaults, then
	// explicit flags override them so power users can still tweak a preset.
	model, system, maxSteps, maxTokens := resolveAgentConfig(provider)

	// NewAnthropicClient / NewOpenAIClient already prepend ObserveLLM() internally.
	var client llm.LLMClient
	switch provider {
	case providerOpenAI:
		client = llm.NewOpenAIClient(llm.OpenAIConfig{APIKey: apiKey})
	default:
		client = llm.NewAnthropicClient(llm.AnthropicConfig{APIKey: apiKey})
	}

	logger.DebugContext(ctx, "provider.selected",
		slog.String("provider", provider),
		slog.String("model", model),
		slog.String("agent_type", flags.agentType),
	)

	// Wire long-term memory when a DSN is provided.
	var mem *memory.PgVectorMemory
	if flags.memoryDSN != "" {
		embedKey := flags.embedKey
		if embedKey == "" {
			embedKey = os.Getenv("OPENAI_API_KEY")
		}
		if embedKey == "" {
			return ctx, nil, fmt.Errorf("--memory-dsn requires an OpenAI API key for embeddings (set --embed-key or OPENAI_API_KEY)")
		}
		embedder := memory.NewOpenAIEmbedder(embedKey)
		var memErr error
		mem, memErr = memory.NewPgVectorMemory(ctx, flags.memoryDSN, embedder, "default")
		if memErr != nil {
			return ctx, nil, fmt.Errorf("cli.bootstrap: memory: %w", memErr)
		}
		logger.InfoContext(ctx, "memory.pgvector.connected", slog.String("dsn_host", flags.memoryDSN))
	}

	buf := core.NewConversationBuffer(flags.tokenBudget)
	agent := core.NewAgent(core.AgentConfig{
		Model:     model,
		System:    system,
		MaxSteps:  maxSteps,
		MaxTokens: maxTokens,
		Memory:    mem,
	}, client, buildRegistry(), buf)

	agent.Use(
		guardrails.MaxSteps(maxSteps),
		guardrails.TokenBudget(flags.tokenBudget),
		guardrails.LoopDetection(5),
	)
	if flags.costBudget > 0 {
		agent.Use(guardrails.CostBudget(flags.costBudget))
	}

	// builder creates a fresh agent for each orchestration step using the
	// detected provider and API key.  Each call returns a new agent with an
	// empty conversation buffer so steps do not bleed state.
	builder := func(preset agents.Preset) *core.Agent {
		var stepClient llm.LLMClient
		switch provider {
		case providerOpenAI:
			stepClient = llm.NewOpenAIClient(llm.OpenAIConfig{APIKey: apiKey})
		default:
			stepClient = llm.NewAnthropicClient(llm.AnthropicConfig{APIKey: apiKey})
		}
		presetModel := agents.ModelForTier(provider, preset.RecommendedTier)
		a := core.NewAgent(core.AgentConfig{
			Model:     presetModel,
			System:    preset.System,
			MaxSteps:  preset.MaxSteps,
			MaxTokens: preset.MaxTokens,
		}, stepClient, buildRegistry(), core.NewConversationBuffer(flags.tokenBudget))
		a.Use(
			guardrails.MaxSteps(preset.MaxSteps),
			guardrails.TokenBudget(flags.tokenBudget),
			guardrails.LoopDetection(5),
		)
		if flags.costBudget > 0 {
			a.Use(guardrails.CostBudget(flags.costBudget))
		}
		return a
	}

	// Wrap cleanup to also close the memory pool.
	baseCleanup := cleanup
	cleanup = func() {
		if mem != nil {
			mem.Close()
		}
		baseCleanup()
	}

	deps := &infraDeps{
		logger:   logger,
		ledger:   ledger,
		agent:    agent,
		agentID:  agent.ID(),
		provider: provider,
		builder:  builder,
		mem:      mem,
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

// resolveAgentConfig returns (model, system, maxSteps, maxTokens) by merging
// the selected preset with any explicit CLI flag overrides.
// Precedence (highest→lowest): explicit flag → preset value → built-in default.
func resolveAgentConfig(provider string) (model, system string, maxSteps, maxTokens int) {
	// Start from built-in defaults.
	model = defaultModel(provider)
	system = flags.system
	maxSteps = flags.maxSteps
	maxTokens = flags.maxTokens

	if flags.agentType != "" {
		preset, ok := agents.ByID(flags.agentType)
		if !ok {
			// Unknown preset — bootstrap will log a warning; fall through to defaults.
			return
		}
		// Apply preset values.
		model = agents.ModelForTier(provider, preset.RecommendedTier)
		system = preset.System
		maxSteps = preset.MaxSteps
		maxTokens = preset.MaxTokens
	}

	// Explicit flags always win (non-zero / non-default values override preset).
	if flags.model != "" {
		model = flags.model
	}
	// system flag default is non-empty, so only override if user explicitly
	// changed it from its init() default.
	if flags.agentType == "" && flags.system != "" {
		system = flags.system
	}
	if flags.maxSteps != 20 { // 20 is the init() default
		maxSteps = flags.maxSteps
	}
	if flags.maxTokens != 2048 { // 2048 is the init() default
		maxTokens = flags.maxTokens
	}
	return
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

// resolveLogWriter returns the io.Writer logs should be written to, plus a
// closer that must be called when the program exits.
//
// Priority:
//   --verbose          → stderr  (developer mode, no file)
//   --log-file PATH    → PATH    (operator mode)
//   neither            → io.Discard (clean interactive UI, default)
func resolveLogWriter() (io.Writer, func()) {
	if flags.verbose {
		return os.Stderr, func() {}
	}
	if flags.logFile != "" {
		f, err := os.OpenFile(flags.logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot open log file %s: %v — logs discarded\n", flags.logFile, err)
			return io.Discard, func() {}
		}
		return f, func() { _ = f.Close() }
	}
	return io.Discard, func() {}
}

// loadEnvFile loads variables from path into the process environment.
// Existing env vars are NOT overridden (godotenv.Load semantics).
// Silently skips if path is empty or the file does not exist.
func loadEnvFile(path string) {
	if path == "" {
		return
	}
	err := godotenv.Load(path)
	if err == nil {
		return
	}
	// Missing file is normal (user may rely on real env vars instead).
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	// Any other error (malformed file, permissions) is worth surfacing.
	fmt.Fprintf(os.Stderr, "warning: could not load %s: %v\n", path, err)
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
