---
description: Start or resume building the pure-Go AI agent. Pass a layer name to focus on a specific component, or run with no args to build the next incomplete layer automatically.
argument-hint: "[layer: llm | tools | context | memory | guardrails | observability | cli]"
allowed-tools: Bash(go mod init *), Bash(go mod tidy), Bash(go get *), Bash(mkdir -p *), Bash(cat *), Bash(ls *), Bash(find *), Bash(go build ./...), Bash(go test ./...), Bash(go vet ./...)
---

# Build Pure-Go AI Agent

## Context
You are building a production-grade AI agent runtime in **pure Go** — no frameworks.
Read `CLAUDE.md` in the project root for the full architecture, design principles, conventions,
observability design, and current build status before doing anything else.

## Task

The user invoked `/build-go-agent $ARGUMENTS`.

---

### Step 1 — Orient
1. Read `CLAUDE.md` fully — pay attention to the Observability Design section.
2. Run `find . -name "*.go" | head -60` to map what already exists.
3. Check `go.mod` for the module name and current dependencies.

---

### Step 2 — Decide what to build

If `$ARGUMENTS` is **empty**:
- Scan CLAUDE.md's `## Current Status` checklist for the first unchecked `[ ]` item.
- Announce which layer you're tackling and why it's the logical next step.

If `$ARGUMENTS` names a **specific layer**:
- Focus entirely on that layer.
- Honour all existing interfaces, naming, and conventions in the codebase.

---

### Step 3 — Scaffold if needed

If `go.mod` doesn't exist, initialise the module:
```bash
go mod init github.com/yourname/goagent
```

Create directories from CLAUDE.md if missing:
```bash
mkdir -p core llm tools/builtin memory guardrails observability
```

---

### Step 4 — Implement

Write idiomatic, production-quality Go. For every file:

1. **Interface first** — define the contract before any implementation.
2. **Constructor injection** — no global mutable state; pass all deps in `New*()`.
3. **Context-first signatures** — `ctx context.Context` is always arg #1.
4. **Explicit errors** — `fmt.Errorf("pkg.Op: %w", err)`; no panics in library code.
5. **Observability on every operation** — see the mandatory pattern below.
6. **Table-driven tests** — `_test.go` alongside every new file.

---

### MANDATORY: Observability Pattern

**Every tool call, LLM call, memory operation, and guardrail check must emit all three signals.**
Never add bare `slog.Info` calls outside this pattern.

```go
func doOperation(ctx context.Context, ...) (Result, error) {
    // 1. Start span
    ctx, span := otel.Tracer("go-agent").Start(ctx, "subsystem.operation",
        trace.WithAttributes(
            attribute.String("key", value),
        ),
    )
    defer span.End()

    // 2. Log start
    log := observability.LoggerFrom(ctx)
    log.Debug("subsystem.operation.start", slog.String("key", value))

    // 3. Do the work, measure wall time
    start := time.Now()
    result, err := actualWork(ctx, ...)
    dur := time.Since(start)

    // 4. Record outcome on span and metrics
    status := "ok"
    if err != nil {
        status = "error"
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        subsystemErrorsTotal.WithLabelValues(operationName).Inc()
    }
    span.SetAttributes(
        attribute.Int64("latency_ms", dur.Milliseconds()),
        attribute.String("status", status),
    )
    subsystemLatency.WithLabelValues(operationName).Observe(dur.Seconds())

    // 5. Log completion
    log.Info("subsystem.operation.complete",
        slog.String("status",     status),
        slog.Int64 ("latency_ms", dur.Milliseconds()),
    )

    return result, err
}
```

**Log fields — always include:**
- `trace_id` and `span_id` (auto-injected by traceHandler — do NOT add manually)
- `status` — always `"ok"` | `"error"` | `"timeout"` | `"cancelled"`
- `latency_ms` — always present on `.complete` events
- operation-specific fields (tool name, model, token counts, etc.)

**Never log at INFO or above:**
- Raw user input longer than 200 chars — truncate with `observability.Truncate(s, 200)`
- Secrets, API keys, auth tokens
- High-cardinality values as Prometheus label values (user IDs, full input strings)

---

### Layer-Specific Guidance

#### `observability/` — Build this early; everything else depends on it

**`logger.go`**
```go
// NewLogger returns a *slog.Logger with:
// - JSON handler in production (LOG_FORMAT=json)
// - tint (coloured) handler in development
// - traceHandler wrapper that auto-injects trace_id + span_id
func NewLogger(env string) *slog.Logger

// WithLogger / LoggerFrom — store/retrieve logger in context
func WithLogger(ctx context.Context, l *slog.Logger) context.Context
func LoggerFrom(ctx context.Context) *slog.Logger

// traceHandler — wraps any slog.Handler to inject OTel span context
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

**`tracer.go`**
```go
// InitTracer sets up the global OTel TracerProvider.
// Uses OTLP gRPC exporter when OTEL_EXPORTER_OTLP_ENDPOINT is set,
// falls back to stdout exporter in dev.
func InitTracer(ctx context.Context, serviceName, version string) (func(), error)

// Span helpers
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span)
```

**`metrics.go`**
Define ALL Prometheus metrics here. Group by subsystem. Register with `promauto` (auto-registers
on import). Expose `ServeMetrics(addr string)` which starts `/metrics` HTTP endpoint.

Required metric groups:
- `goagent_llm_*` — requests_total, latency_seconds, tokens_total (by direction), cost_usd_total
- `goagent_tool_*` — calls_total, latency_seconds, errors_total, timeout_total
- `goagent_agent_*` — steps_total, runs_total, run_duration_seconds
- `goagent_memory_*` — recall_latency_seconds, store_latency_seconds, recall_hits_total, recall_misses_total
- `goagent_guardrail_*` — triggers_total (by guardrail name and action: warned|blocked)
- `goagent_context_*` — compactions_total, current_token_count (gauge)

**`cost.go`**
```go
type TokenPrice struct{ Input, Output float64 } // USD per 1M tokens

// Keep this table current with Anthropic pricing
var ModelPricing = map[string]TokenPrice{
    "claude-opus-4-5":   {Input: 15.00, Output: 75.00},
    "claude-sonnet-4-5": {Input:  3.00, Output: 15.00},
    "claude-haiku-4-5":  {Input:  0.80, Output:  4.00},
}

type CostLedger struct { ... } // thread-safe with sync.Mutex

// Record adds a usage event; returns cost in USD for this call.
func (l *CostLedger) Record(model string, inputTok, outputTok int) float64

// Summary returns totals across all models.
func (l *CostLedger) Summary() CostSummary

// WithCostLedger / CostLedgerFrom — context helpers
func WithCostLedger(ctx context.Context, l *CostLedger) context.Context
func CostLedgerFrom(ctx context.Context) *CostLedger
```

**`middleware.go`**
```go
// ObserveTool returns a tools.Middleware that wraps every tool call with the
// full observability pattern: span + structured log + metrics update.
func ObserveTool(tracer trace.Tracer) tools.Middleware

// ObserveLLM returns an llm.Middleware that wraps every LLM call and also
// records token usage in the CostLedger and emits cost.updated log events.
func ObserveLLM(tracer trace.Tracer) llm.Middleware

// ObserveMemory returns a memory.Middleware that records recall hits/misses.
func ObserveMemory(tracer trace.Tracer) memory.Middleware
```

---

#### `llm/`
- Define `LLMClient` interface first.
- Implement Anthropic using `github.com/anthropics/anthropic-sdk-go`.
- `Stream()` returns `<-chan StreamChunk`; each chunk carries partial token counts.
- After every `Complete()` call: call `CostLedgerFrom(ctx).Record(model, in, out)`.
- Wrap with `observability.ObserveLLM` middleware in the constructor.
- Handle rate limits with exponential backoff; log each retry as `llm.request.retry`.

#### `tools/`
- Define `Tool` interface. Build `Registry` with `Register(Tool)` and `Dispatch(ctx, ToolCall) ToolResult`.
- `Registry.Dispatch` applies the `ObserveTool` middleware automatically for every call.
- Write `schema.go` using `reflect` to auto-generate JSON Schema from struct tags.
- Built-in tools: `BashTool`, `FileReadTool`, `FileWriteTool`, `HTTPTool`.
- Every tool's `Execute` must honour `ctx` cancellation and set a timeout.

#### `core/context.go`
- `ConversationBuffer` with token budget and a gauge metric `goagent_context_current_token_count`.
- FIFO eviction with summary via a cheap LLM call; increment `goagent_context_compactions_total` on each compaction.
- Log `context.compacted` and `context.evicted` events with token counts before/after.

#### `core/agent.go`
- Main loop: `Plan → ApplyGuardrails → Execute → UpdateContext → IsTerminal`.
- Each iteration: start a `agent.step` span as a child of the run span.
- Parallel tool execution via goroutines; each goroutine inherits the parent `ctx` (OTel propagates automatically).
- Log `agent.run.start` at run start with `agent_id`, `trace_id`, configured budgets.
- Log `agent.run.complete` with total steps, total cost, total latency.

#### `memory/`
- Define `Memory` interface: `Store`, `Recall`.
- Wrap both methods with `observability.ObserveMemory`.
- `PgVectorMemory` using `pgx/v5`; log `memory.recall.hit` / `memory.recall.miss` per result.
- `EmbeddingClient` interface for generating vectors (implement via Anthropic or OpenAI embeddings API).

#### `guardrails/`
- Chainable `Middleware`: `func(ctx, step, next) (step, error)`.
- Every time a guardrail fires: increment `goagent_guardrail_triggers_total{guardrail, action}` and log `guardrail.triggered`.
- Implement: `MaxSteps`, `TokenBudget`, `CostBudget` (reads from `CostLedgerFrom(ctx)`),
  `LoopDetection` (hash recent tool calls, detect repeats), `RequireStructuredOutput`, `RetryOnMalformedJSON`.
- `CostBudget` middleware: at 80% of limit → log `budget.warning`; at 100% → log `budget.exceeded` and return error.

#### `cli/`
- Cobra `run` command: interactive REPL with streaming output.
- On startup: print agent ID and trace ID so the user can correlate logs.
- On exit: print `CostLedger.Summary()` — total tokens and USD cost per model.
- `--log-format` flag: `json` (prod) | `text` (dev). Default: `text`.
- `--metrics-addr` flag: address for Prometheus `/metrics` endpoint. Default: `:9090`.
- `--otlp-endpoint` flag: OTLP collector endpoint. Empty = stdout exporter.

---

### Step 5 — Wire it up

After implementing a layer, update `main.go` to show it working end-to-end.
`go run .` must compile and produce meaningful output (logs, a trace, a metric scrape).
For the observability layer specifically, start with a smoke test:
- Make one LLM call
- Verify JSON log lines appear on stdout with `trace_id` and `span_id`
- Hit `localhost:9090/metrics` and confirm `goagent_llm_*` metrics exist

---

### Step 6 — Run checks
```bash
go vet ./...
go build ./...
go test ./...
```
Fix all errors before finishing. Tests for observability components should use
`go.opentelemetry.io/otel/trace/noop` as the tracer and a test Prometheus registry.

---

### Step 7 — Update CLAUDE.md
- Mark completed checklist items as `[x]`.
- Add any pricing updates, design deviations, or new gotchas discovered this session.
- If the pricing table in `observability/cost.go` was changed, update it in CLAUDE.md too.

---

## Output
End your response with a **handoff note** (one paragraph) covering:
- What was built this session and which checklist items are now `[x]`
- Any design decisions that differ from CLAUDE.md (and why)
- The recommended next `/build-go-agent <layer>` call
- Any metrics, log events, or span names that were added or renamed (so dashboards can be updated)
