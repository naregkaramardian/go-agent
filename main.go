package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/nareg/goagent/observability"
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
		if err := shutdown(shutCtx); err != nil {
			logger.Error("tracer shutdown", slog.String("error", err.Error()))
		}
	}()

	ledger := observability.NewCostLedger()
	ctx = observability.WithCostLedger(ctx, ledger)

	observability.ServeMetrics(metricsAddr)
	logger.Info("metrics.started", slog.String("addr", metricsAddr))

	ctx, span := observability.StartSpan(ctx, "agent.run",
		attribute.String("agent.id", "smoke-test"),
	)
	defer span.End()

	log := observability.LoggerFrom(ctx)
	log.InfoContext(ctx, "agent.run.start",
		slog.String("agent_id", "smoke-test"),
		slog.String("status", "ok"),
	)

	cost := observability.CostLedgerFrom(ctx).Record("claude-sonnet-4-5", 1_000, 500)
	log.InfoContext(ctx, "cost.updated",
		slog.String("model", "claude-sonnet-4-5"),
		slog.Float64("cost_usd", cost),
	)

	summary := ledger.Summary()
	fmt.Printf("\nCost summary: $%.6f total\n", summary.TotalCostUSD)
	fmt.Printf("Metrics:      http://localhost%s/metrics\n", metricsAddr)
	fmt.Println("Press Ctrl+C to exit.")

	<-ctx.Done()
	log.InfoContext(ctx, "agent.run.complete", slog.String("status", "ok"))
}
