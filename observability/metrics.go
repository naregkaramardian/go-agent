package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// LLM metrics.
var (
	LLMRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_llm_requests_total",
		Help: "Total LLM API calls by model and status.",
	}, []string{"model", "status"})

	LLMLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "goagent_llm_latency_seconds",
		Help:    "LLM call latency distribution.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
	}, []string{"model"})

	LLMTokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_llm_tokens_total",
		Help: "Total tokens consumed, by model and direction (input|output).",
	}, []string{"model", "direction"})

	LLMCostUSDTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_llm_cost_usd_total",
		Help: "Cumulative LLM cost in USD by model.",
	}, []string{"model"})
)

// Tool metrics.
var (
	ToolCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_tool_calls_total",
		Help: "Total tool invocations by tool name and status.",
	}, []string{"tool", "status"})

	ToolLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "goagent_tool_latency_seconds",
		Help:    "Tool execution latency distribution.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5},
	}, []string{"tool"})

	ToolErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_tool_errors_total",
		Help: "Total tool errors by tool name.",
	}, []string{"tool"})

	ToolTimeoutsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_tool_timeouts_total",
		Help: "Total tool timeouts by tool name.",
	}, []string{"tool"})
)

// Agent metrics.
var (
	AgentStepsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_agent_steps_total",
		Help: "Total agent loop iterations.",
	}, []string{"agent_id", "status"})

	AgentRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_agent_runs_total",
		Help: "Total agent runs by status.",
	}, []string{"status"})

	AgentRunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "goagent_agent_run_duration_seconds",
		Help:    "End-to-end agent run duration.",
		Buckets: prometheus.DefBuckets,
	}, []string{"status"})
)

// Memory metrics.
var (
	MemoryRecallLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "goagent_memory_recall_latency_seconds",
		Help:    "Vector memory recall latency.",
		Buckets: []float64{0.005, 0.01, 0.05, 0.1, 0.5},
	})

	MemoryStoreLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "goagent_memory_store_latency_seconds",
		Help:    "Vector memory store latency.",
		Buckets: []float64{0.005, 0.01, 0.05, 0.1, 0.5},
	})

	MemoryRecallHitsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "goagent_memory_recall_hits_total",
		Help: "Total memory recall results returned.",
	})

	MemoryRecallMissesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "goagent_memory_recall_misses_total",
		Help: "Total memory recall queries that returned no results.",
	})
)

// Guardrail metrics.
var (
	GuardrailTriggersTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_guardrail_triggers_total",
		Help: "Times each guardrail fired, by name and action (warned|blocked).",
	}, []string{"guardrail", "action"})
)

// Context metrics.
var (
	ContextCompactionsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "goagent_context_compactions_total",
		Help: "Total conversation context compaction events.",
	})

	ContextCurrentTokens = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "goagent_context_current_token_count",
		Help: "Current number of tokens in the active conversation context.",
	})
)

// Orchestration metrics.
var (
	OrchestrationRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "goagent_orchestration_runs_total",
		Help: "Total orchestration runs by workflow and status.",
	}, []string{"workflow", "status"})

	OrchestrationDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "goagent_orchestration_duration_seconds",
		Help:    "End-to-end orchestration duration.",
		Buckets: prometheus.DefBuckets,
	}, []string{"workflow"})

	OrchestrationStepDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "goagent_orchestration_step_duration_seconds",
		Help:    "Per-step duration within an orchestration.",
		Buckets: prometheus.DefBuckets,
	}, []string{"workflow", "step"})
)

// ServeMetrics starts the Prometheus /metrics HTTP endpoint on addr (e.g. ":9090").
// Runs in a background goroutine; does not block.
func ServeMetrics(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(addr, mux) //nolint:errcheck
}
