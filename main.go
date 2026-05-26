package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	metricsAddr = ":9090"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env := os.Getenv("LOG_FORMAT")
	if env == "" {
		env = "dev"
	}
	logger := observability.NewLogger(env)
	slog.SetDefault(logger)
	ctx = observability.WithLogger(ctx, logger)

	shutdown, err := observability.InitTracer(ctx, serviceName, version)
	if err != nil {
		logger.Error("tracer init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutErr := shutdown(shutCtx); shutErr != nil {
			logger.Error("tracer shutdown", slog.String("error", shutErr.Error()))
		}
	}()

	ledger := observability.NewCostLedger()
	ctx = observability.WithCostLedger(ctx, ledger)

	observability.ServeMetrics(metricsAddr)
	logger.Info("metrics.started", slog.String("addr", metricsAddr))

	// Build the tool registry with built-in tools.
	reg := tools.NewRegistry()

	bashTool, err := builtin.NewBashTool()
	if err != nil {
		logger.Error("bash tool init", slog.String("error", err.Error()))
		os.Exit(1)
	}
	httpTool, err := builtin.NewHTTPTool()
	if err != nil {
		logger.Error("http tool init", slog.String("error", err.Error()))
		os.Exit(1)
	}
	fileRead, err := builtin.NewFileReadTool()
	if err != nil {
		logger.Error("file read tool init", slog.String("error", err.Error()))
		os.Exit(1)
	}
	fileWrite, err := builtin.NewFileWriteTool()
	if err != nil {
		logger.Error("file write tool init", slog.String("error", err.Error()))
		os.Exit(1)
	}
	reg.Register(bashTool)
	reg.Register(httpTool)
	reg.Register(fileRead)
	reg.Register(fileWrite)

	// Build the Anthropic LLM client.
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		logger.Warn("ANTHROPIC_API_KEY not set — running in demo mode (no real LLM calls)")
		runDemoMode(ctx, reg, ledger)
		return
	}

	client := llm.NewAnthropicClient(llm.AnthropicConfig{APIKey: apiKey}, llm.ObserveLLM())

	buf := core.NewConversationBuffer(100_000)
	agent := core.NewAgent(core.AgentConfig{
		Model:     "claude-haiku-4-5",
		System:    "You are a helpful AI agent. Answer concisely.",
		MaxSteps:  20,
		MaxTokens: 1024,
	}, client, reg, buf)
	agent.Use(
		guardrails.MaxSteps(20),
		guardrails.TokenBudget(100_000),
		guardrails.CostBudget(1.00),
		guardrails.LoopDetection(5),
	)

	result, err := agent.Run(ctx, "What is 2+2? Reply with just the number.")
	if err != nil {
		logger.Error("agent run failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	fmt.Printf("\nAgent output: %s\n", result.Output)
	fmt.Printf("Steps: %d | Cost: $%.6f | Duration: %s\n",
		result.Steps, result.Cost, result.Duration.Round(time.Millisecond))

	summary := ledger.Summary()
	fmt.Printf("Total cost: $%.6f\n", summary.TotalCostUSD)
	fmt.Printf("Metrics: http://localhost%s/metrics\n", metricsAddr)
}

func runDemoMode(ctx context.Context, _ *tools.Registry, ledger *observability.CostLedger) {
	log := observability.LoggerFrom(ctx)
	log.InfoContext(ctx, "demo.mode", slog.String("status", "ok"))

	cost := observability.CostLedgerFrom(ctx).Record("claude-haiku-4-5", 500, 100)
	log.InfoContext(ctx, "cost.updated",
		slog.String("model", "claude-haiku-4-5"),
		slog.Float64("cost_usd", cost),
	)

	summary := ledger.Summary()
	fmt.Printf("\n[demo] Cost ledger: $%.6f total\n", summary.TotalCostUSD)
	fmt.Printf("[demo] Metrics:     http://localhost%s/metrics\n", metricsAddr)
	fmt.Println("[demo] Set ANTHROPIC_API_KEY to run the agent for real.")
}
