# Go AI Agent — Project Context

## What We're Building
A production-grade AI agent runtime in **pure Go** — no Python frameworks, no LangChain-style abstractions.
The goal is a framework-level feature set (tool use, memory, guardrails, context management, and full
observability) built from idiomatic Go primitives: interfaces, goroutines, channels, and the standard library.

---

## Architecture Overview

```
agent/
├── core/
│   ├── agent.go          # Main agent loop (plan → act → observe → repeat)
│   ├── context.go        # Conversation window, token tracking, truncation
│   └── runner.go         # Step executor, concurrency orchestration
├── llm/
│   ├── client.go         # LLM provider interface
│   ├── anthropic.go      # Anthropic Claude implementation
│   └── stream.go         # Streaming response handler
├── tools/
│   ├── registry.go       # Tool registration and dispatch
│   ├── schema.go         # JSON schema generation via reflect
│   └── builtin/          # Built-in tools (bash, file, http, etc.)
├── memory/
│   ├── buffer.go         # Short-term in-process message buffer
│   ├── episodic.go       # Long-term vector store integration
│   └── retrieval.go      # Embedding + recall strategy
├── guardrails/
│   ├── middleware.go     # Chainable middleware interface
│   ├── budget.go         # Step/token/cost budget enforcement
│   ├── loop.go           # Loop detection (repeated tool calls)
│   └── output.go         # Structured output validation + retry
├── observability/
│   ├── logger.go         # Structured slog setup (JSON + text handlers)
│   ├── tracer.go         # Span-based tracing (OpenTelemetry or hand-rolled)
│   ├── metrics.go        # Prometheus metrics (counters, histograms, gauges)
│   ├── cost.go           # Token cost accounting per model/provider
│   └── middleware.go     # Obs middleware: wraps every tool call + LLM call
└── main.go
```

---

## Core Design Principles

### 1. Everything is an interface
```go
type LLMClient interface {
    Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)
    Stream(ctx context.Context, req *CompletionRequest) (<-chan StreamChunk, error)
}

type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage
    Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

type Memory interface {
    Store(ctx context.Context, entry MemoryEntry) error
    Recall(ctx context.Context, query string, topK int) ([]MemoryEntry, error)
}

type Middleware func(ctx context.Context, step *AgentStep, next StepHandler) (*AgentStep, error)
```

### 2. Agent loop is explicit and inspectable
```go
for !done {
    step, err := agent.Plan(ctx, history)        // LLM decides next action
    step, err = agent.ApplyGuardrails(ctx, step) // middleware chain
    result, err := agent.Execute(ctx, step)      // run tool or return
    history = agent.UpdateContext(history, step, result)
    done = agent.IsTerminal(step, result)
}
```

### 3. Parallel tool execution via goroutines
```go
func (r *Runner) ExecuteParallel(ctx context.Context, calls []ToolCall) []ToolResult {
    results := make([]ToolResult, len(calls))
    var wg sync.WaitGroup
    for i, call := range calls {
        wg.Add(1)
        go func(i int, call ToolCall) {
            defer wg.Done()
            results[i] = r.registry.Dispatch(ctx, call)
        }(i, call)
    }
    wg.Wait()
    return results
}
```

---

## Key Implementation Details

### Context / Token Management
- Track token count per message using a token estimator (4 chars ≈ 1 token, or call a real tokenizer)
- When window approaches limit: summarize oldest N messages via a cheap LLM call, replace with summary
- Always preserve: system prompt, last N turns, any active tool results
- Strategy: `FIFO eviction with summary` — never hard-truncate mid-thought

### Tool Schema Generation
- Use `reflect` to inspect Go structs tagged with `json` and `description` struct tags
- Generate OpenAI-compatible JSON Schema automatically — no manual schema writing
- Register tools at startup via `registry.Register(tool)`, dispatch by name at runtime

### Memory Architecture
- **Short-term**: `[]Message` slice in the `ConversationBuffer`, bounded by token budget
- **Long-term**: Embeddings stored in pgvector (Postgres) or Qdrant; retrieval at each turn
- **Injection**: Recalled memories prepended to system context, ranked by cosine similarity
- Go client options: `pgx` for pgvector, official Qdrant Go client for Qdrant

### Guardrails as Middleware Chain
```go
agent.Use(guardrails.MaxSteps(25))
agent.Use(guardrails.TokenBudget(100_000))
agent.Use(guardrails.LoopDetection(windowSize: 5))
agent.Use(guardrails.RequireStructuredOutput(schema))
agent.Use(guardrails.RetryOnMalformedJSON(maxRetries: 3))
```
Each middleware returns the (possibly mutated) step or an error that halts the loop.

### Structured Output / Retry
- Request JSON mode from the LLM
- Parse with `encoding/json`; on failure, re-inject the raw output + error into the prompt and retry
- After N retries, escalate to a `GuardrailError` and surface it to the caller

---

## Observability Design

### Philosophy
Every significant event in the agent lifecycle emits three signals simultaneously:
1. A **structured log line** (machine-parseable, queryable in any log platform)
2. A **trace span** (causal chain from request → plan → tool → result)
3. A **metric update** (counters/histograms for dashboards and alerting)

No `fmt.Println`. No ad-hoc logging. Every log carries a trace ID.

---

### Structured Logging (`observability/logger.go`)

Use `log/slog` (stdlib, Go 1.21+) with a JSON handler in production and a human-readable
`tint` handler in development. Logger is constructed once and threaded via `context.Context`.

```go
// Setup (main.go)
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelDebug,
    ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
        if a.Key == slog.TimeKey {
            a.Value = slog.StringValue(time.Now().UTC().Format(time.RFC3339Nano))
        }
        return a
    },
}))
slog.SetDefault(logger)

// Attach to context
ctx = observability.WithLogger(ctx, logger)

// Retrieve and use
log := observability.LoggerFrom(ctx)
log.Info("tool.executed",
    slog.String("tool",       call.Name),
    slog.String("trace_id",   traceID),
    slog.String("span_id",    spanID),
    slog.Int64 ("latency_ms", latencyMs),
    slog.Int   ("input_tokens",  resp.Usage.InputTokens),
    slog.Int   ("output_tokens", resp.Usage.OutputTokens),
    slog.String("status",     "ok"),
)
```

**Mandatory fields on every log line:**

| Field           | Type    | Description                              |
|-----------------|---------|------------------------------------------|
| `trace_id`      | string  | UUID per agent run (set at run start)    |
| `span_id`       | string  | UUID per operation                       |
| `agent_id`      | string  | Which agent instance                     |
| `step`          | int     | Which loop iteration                     |
| `event`         | string  | Dot-namespaced: `tool.start`, `llm.complete`, `memory.recall` |
| `latency_ms`    | int64   | Wall-clock duration of the operation     |
| `status`        | string  | `ok` / `error` / `timeout` / `cancelled` |

**Event taxonomy (always use these exact names):**

```
agent.run.start       agent.run.complete     agent.run.error
llm.request.start     llm.request.complete   llm.request.error   llm.stream.chunk
tool.call.start       tool.call.complete     tool.call.error      tool.call.timeout
memory.store          memory.recall          memory.recall.hit    memory.recall.miss
guardrail.triggered   guardrail.blocked      context.compacted    context.evicted
cost.updated          budget.warning         budget.exceeded
```

---

### Tracing (`observability/tracer.go`)

Use **OpenTelemetry** (`go.opentelemetry.io/otel`) — the defacto standard. Wire to any
backend (Jaeger, Tempo, Honeycomb, Datadog) via the OTLP exporter. Fall back to a
`stdout` span exporter in dev mode.

```go
// Tracer setup
tp := trace.NewTracerProvider(
    trace.WithBatcher(otlpExporter),
    trace.WithResource(resource.NewWithAttributes(
        semconv.SchemaURL,
        semconv.ServiceNameKey.String("go-agent"),
        semconv.ServiceVersionKey.String(version),
    )),
)
otel.SetTracerProvider(tp)
tracer := otel.Tracer("go-agent")

// In the agent loop — every tool call gets its own span
ctx, span := tracer.Start(ctx, "tool.call",
    trace.WithAttributes(
        attribute.String("tool.name", call.Name),
        attribute.String("agent.id",  agentID),
        attribute.Int   ("step",      stepNum),
    ),
)
defer span.End()

// LLM call span
ctx, llmSpan := tracer.Start(ctx, "llm.complete",
    trace.WithAttributes(
        attribute.String("llm.model",         req.Model),
        attribute.Int   ("llm.input_tokens",  req.EstimatedTokens),
        attribute.String("llm.provider",      "anthropic"),
    ),
)
// After response:
llmSpan.SetAttributes(
    attribute.Int("llm.output_tokens", resp.Usage.OutputTokens),
    attribute.Float64("llm.cost_usd",  cost),
)
llmSpan.End()
```

**Span hierarchy per agent run:**
```
agent.run  [trace root]
  └─ agent.step[0]
       ├─ llm.complete          (planning call)
       ├─ tool.call[bash]       (parallel)
       ├─ tool.call[file_read]  (parallel)
       └─ memory.recall
  └─ agent.step[1]
       ├─ llm.complete
       └─ agent.run.complete
```

Always propagate the trace context through goroutines — pass `ctx` explicitly, never store it.

---

### Metrics (`observability/metrics.go`)

Use **Prometheus** (`github.com/prometheus/client_golang`). Expose `/metrics` endpoint.
All metrics are namespaced under `goagent_`.

```go
var (
    // LLM
    llmRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "goagent_llm_requests_total",
        Help: "Total LLM API calls by model and status.",
    }, []string{"model", "status"})

    llmLatencyHistogram = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "goagent_llm_latency_seconds",
        Help:    "LLM call latency distribution.",
        Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
    }, []string{"model"})

    llmTokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "goagent_llm_tokens_total",
        Help: "Total tokens consumed, by model and direction.",
    }, []string{"model", "direction"}) // direction: input | output

    // Cost
    llmCostUSDTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "goagent_llm_cost_usd_total",
        Help: "Cumulative LLM cost in USD by model.",
    }, []string{"model"})

    // Tools
    toolCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "goagent_tool_calls_total",
        Help: "Total tool invocations by tool name and status.",
    }, []string{"tool", "status"})

    toolLatencyHistogram = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "goagent_tool_latency_seconds",
        Help:    "Tool execution latency distribution.",
        Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5},
    }, []string{"tool"})

    // Agent
    agentStepsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "goagent_steps_total",
        Help: "Total agent loop iterations.",
    }, []string{"agent_id", "status"})

    agentRunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "goagent_run_duration_seconds",
        Help:    "End-to-end agent run duration.",
        Buckets: prometheus.DefBuckets,
    }, []string{"status"})

    // Memory
    memoryRecallLatency = promauto.NewHistogram(prometheus.HistogramOpts{
        Name:    "goagent_memory_recall_latency_seconds",
        Help:    "Vector memory recall latency.",
        Buckets: []float64{0.005, 0.01, 0.05, 0.1, 0.5},
    })

    // Guardrails
    guardrailTriggersTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "goagent_guardrail_triggers_total",
        Help: "Number of times each guardrail fired.",
    }, []string{"guardrail", "action"}) // action: warned | blocked
)
```

---

### Cost Tracking (`observability/cost.go`)

Maintain a `CostLedger` that accumulates token usage and computes USD cost per model.
Update it after every LLM call. Surface it in logs, spans, and metrics simultaneously.

```go
// Pricing table (USD per 1M tokens) — update when Anthropic changes pricing
var modelPricing = map[string]TokenPrice{
    "claude-opus-4-5":    {Input: 15.00, Output: 75.00},
    "claude-sonnet-4-5":  {Input:  3.00, Output: 15.00},
    "claude-haiku-4-5":   {Input:  0.80, Output:  4.00},
}

type CostLedger struct {
    mu           sync.Mutex
    inputTokens  map[string]int64   // model → total input tokens
    outputTokens map[string]int64   // model → total output tokens
    costUSD      map[string]float64 // model → cumulative USD
}

func (l *CostLedger) Record(model string, inputTok, outputTok int) float64 {
    p := modelPricing[model]
    cost := float64(inputTok)/1e6*p.Input + float64(outputTok)/1e6*p.Output
    l.mu.Lock()
    l.inputTokens[model]  += int64(inputTok)
    l.outputTokens[model] += int64(outputTok)
    l.costUSD[model]      += cost
    l.mu.Unlock()
    // Update Prometheus counter
    llmCostUSDTotal.WithLabelValues(model).Add(cost)
    return cost
}

func (l *CostLedger) Summary() CostSummary { ... }
```

Store the `CostLedger` in `context.Context` so any layer can call `cost.RecordFrom(ctx, ...)`.
Log a `cost.updated` event after each LLM call. Emit a `budget.warning` at 80% of the
configured USD budget; `budget.exceeded` halts the run via a guardrail.

---

### Observability Middleware (`observability/middleware.go`)

Wrap tool dispatch and LLM calls in a single middleware that handles all three signals at once.
This keeps the core agent loop clean — it calls the middleware, not the raw implementations.

```go
// ObserveTool wraps any Tool.Execute call with logging, tracing, and metrics.
func ObserveTool(tracer trace.Tracer, ledger *CostLedger) tools.Middleware {
    return func(ctx context.Context, call ToolCall, next tools.Handler) ToolResult {
        ctx, span := tracer.Start(ctx, "tool.call",
            trace.WithAttributes(attribute.String("tool.name", call.Name)))
        log := LoggerFrom(ctx)
        log.Info("tool.call.start", slog.String("tool", call.Name), slog.String("input", truncate(call.Input, 200)))

        start := time.Now()
        result := next(ctx, call)
        dur := time.Since(start)

        status := "ok"
        if result.Error != nil {
            status = "error"
            span.RecordError(result.Error)
            span.SetStatus(codes.Error, result.Error.Error())
        }

        span.SetAttributes(
            attribute.Int64 ("latency_ms", dur.Milliseconds()),
            attribute.String("status",     status),
        )
        span.End()

        toolCallsTotal.WithLabelValues(call.Name, status).Inc()
        toolLatencyHistogram.WithLabelValues(call.Name).Observe(dur.Seconds())

        log.Info("tool.call.complete",
            slog.String("tool",       call.Name),
            slog.String("status",     status),
            slog.Int64 ("latency_ms", dur.Milliseconds()),
            slog.String("output",     truncate(string(result.Output), 200)),
        )
        return result
    }
}

// ObserveLLM wraps any LLMClient.Complete call.
func ObserveLLM(tracer trace.Tracer, ledger *CostLedger) llm.Middleware { ... }
```

---

### Trace Context Propagation

Inject `trace_id` and `span_id` into every `slog` log line automatically via a custom
`slog.Handler` wrapper:

```go
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
    if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
        r.AddAttrs(
            slog.String("trace_id", sc.TraceID().String()),
            slog.String("span_id",  sc.SpanID().String()),
        )
    }
    return h.Handler.Handle(ctx, r)
}
```

This means every log line is automatically correlated to its trace — no manual field
passing required after setup.

---

## Tech Stack & Libraries

| Concern              | Library                                        |
|----------------------|------------------------------------------------|
| LLM (Anthropic)      | `github.com/anthropics/anthropic-sdk-go`       |
| HTTP client          | `net/http` (stdlib)                            |
| JSON                 | `encoding/json` (stdlib)                       |
| Config               | `github.com/spf13/viper`                       |
| Logging              | `log/slog` (stdlib, Go 1.21+)                  |
| Dev log formatting   | `github.com/lmittmann/tint`                    |
| Tracing              | `go.opentelemetry.io/otel` + OTLP exporter     |
| Metrics              | `github.com/prometheus/client_golang/promauto` |
| Postgres/pgvector    | `github.com/jackc/pgx/v5`                      |
| Vector DB alt.       | Qdrant Go client                               |
| Testing              | `testing` + `github.com/stretchr/testify`      |
| CLI harness          | `github.com/spf13/cobra`                       |

---

## Coding Conventions
- Go 1.22+
- `context.Context` is the first argument on every public function — and carries logger, tracer, and cost ledger
- Errors are explicit — wrap with `fmt.Errorf("layer.op: %w", err)` — no panics in library code
- Prefer table-driven tests; mock LLM client for unit tests
- All LLM calls must respect context cancellation
- No global state — pass dependencies via constructor injection
- Every log line must carry `trace_id` and `span_id` (handled automatically by the `traceHandler` wrapper)
- Never log raw user input at INFO level or above — use DEBUG and truncate to 200 chars
- Metric label cardinality must stay low — no user IDs, no full tool inputs as labels

---

## Current Status
[x] Project scaffold and module init
[x] LLM client interface + Anthropic implementation
[x] Streaming response handler
[x] Tool interface + registry + schema generation
[x] Conversation context manager with token tracking
[x] Guardrails middleware chain
[x] Short-term memory buffer
[ ] Long-term memory with vector store
[x] Basic agent loop (plan/act/observe)
[x] Built-in tools (bash, file read/write, HTTP)
[x] CLI harness for interactive testing
[x] observability/logger.go — slog setup + traceHandler wrapper
[x] observability/tracer.go — OpenTelemetry provider + OTLP exporter
[x] observability/metrics.go — Prometheus metrics registry
[x] observability/cost.go — CostLedger with per-model pricing
[x] observability/middleware.go — ObserveTool + ObserveLLM wrappers
[x] /metrics HTTP endpoint (Prometheus scrape target)
[x] Trace context auto-propagated through all goroutines
[x] Cost budget guardrail wired to CostLedger

## Design Notes
- **memory.Buffer vs ConversationBuffer**: `memory.Buffer` (in `memory/buffer.go`) implements the
  `Memory` interface for semantic fact storage with `Store`/`Recall`. It is distinct from
  `core.ConversationBuffer` which manages the sliding message window for LLM context. The two
  complement each other: ConversationBuffer = what was said; memory.Buffer = what should be remembered.
- **Short-term recall uses keyword-overlap scoring**: `overlapScore` computes the fraction of query
  words found in entry content (0–1). No embeddings needed for the in-process tier; long-term memory
  will use vector similarity instead.
- **BashTool registration fixed**: `NewRegistry()` is now called with no args (ObserveTool is already
  prepended internally); passing it again would double-count metrics. BashTool is now registered in main.go.
- **Trace injection requires `*Context` log variants**: `slog.Logger.Info()` uses `context.Background()`
  internally, so `trace_id`/`span_id` are only injected when callers use `log.InfoContext(ctx, ...)`.
  All agent loop code must use the `*Context` variants consistently.
- **observability/middleware.go deferred**: `ObserveTool`/`ObserveLLM` return types (`tools.Middleware`,
  `llm.Middleware`) reference packages that don't exist yet. These wrappers will be added to their
  respective packages (tools/middleware.go, llm/middleware.go) to avoid circular imports.
- **Module**: `github.com/nareg/goagent`, Go 1.25+ (required by OTel v1.43.0)
- **OTLP transport**: HTTP exporter (`otlptracehttp`) used instead of gRPC to avoid managing
  `grpc.DialOption`/credentials boilerplate; `OTEL_EXPORTER_OTLP_ENDPOINT` activates it.
- **CLI harness**: `cli/` package (Cobra) with `run` (interactive REPL) and `ask` (one-shot) subcommands.
  `main.go` is now a 4-line entry point that calls `cli.Execute()`. All flags are global (persistent)
  on the root command. See `go run . --help` for the full flag reference.
- **RunStreaming vs Run**: `Agent.RunStreaming(ctx, msg, out io.Writer)` uses `llm.Stream()` instead of
  `llm.Complete()` for each planning call so text deltas are written to `out` as they arrive. The
  guardrail chain, tool execution, and context management are identical to `Run()`. Observability for
  streaming is done inline in the streaming base handler (`llm.stream.complete` log event, same metrics).
- **Double-ObserveLLM fixed**: `NewAnthropicClient` already prepends `ObserveLLM()` internally.
  Do NOT pass `llm.ObserveLLM()` as an extra middleware — it would double-count metrics and costs.
- **Cobra added**: `github.com/spf13/cobra v1.10.2` added to go.mod. Viper is not yet included
  (flags are wired via pflag directly, which is sufficient for the current flag set).
