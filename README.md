# go-agent

A production-grade AI agent runtime in pure Go — no Python frameworks, no LangChain-style abstractions.

## What it is

Framework-level features built from idiomatic Go primitives: interfaces, goroutines, channels, and the standard library.

| Feature | Status |
|---|---|
| Observability (logs, traces, metrics, cost) | ✅ Done |
| LLM client (Anthropic) + streaming | 🔲 Next |
| Tool interface + registry + built-ins | 🔲 Planned |
| Agent loop (plan → act → observe) | 🔲 Planned |
| Conversation context + token management | 🔲 Planned |
| Guardrails middleware chain | 🔲 Planned |
| Short-term + long-term memory | 🔲 Planned |
| CLI harness (interactive REPL) | 🔲 Planned |

## Architecture

```
agent/
├── core/            # Agent loop, context/token management, step runner
├── llm/             # LLMClient interface + Anthropic implementation + streaming
├── tools/           # Tool interface, registry, JSON schema generation, built-ins
├── memory/          # Short-term buffer + long-term vector store (pgvector/Qdrant)
├── guardrails/      # Chainable middleware: budgets, loop detection, structured output
├── observability/   # slog + OpenTelemetry + Prometheus + cost ledger
├── cli/             # Cobra REPL with streaming output
└── main.go
```

## Observability

Every operation emits three signals simultaneously:

- **Structured logs** via `log/slog` — JSON in production, coloured text in dev
- **Distributed traces** via OpenTelemetry — OTLP export or stdout in dev
- **Prometheus metrics** — scraped at `GET /metrics` (default `:9090`)

`trace_id` and `span_id` are injected automatically into every log line via a custom `slog.Handler` wrapper. Use `log.InfoContext(ctx, ...)` variants to carry the active span.

## Running

```bash
# Dev mode (coloured logs, stdout traces)
go run .

# Production mode (JSON logs, OTLP export)
LOG_FORMAT=production OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318 go run .

# Scrape metrics
curl http://localhost:9090/metrics | grep goagent_
```

## Configuration

| Env var | Default | Description |
|---|---|---|
| `LOG_FORMAT` | `dev` | `dev` (tint) or `production` (JSON) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(unset)_ | OTLP HTTP endpoint; unset = stdout exporter |

## Tech stack

| Concern | Library |
|---|---|
| LLM (Anthropic) | `github.com/anthropics/anthropic-sdk-go` |
| Logging | `log/slog` + `github.com/lmittmann/tint` |
| Tracing | `go.opentelemetry.io/otel` + OTLP HTTP exporter |
| Metrics | `github.com/prometheus/client_golang` |
| Config | `github.com/spf13/viper` |
| CLI | `github.com/spf13/cobra` |
| Vector DB | `github.com/jackc/pgx/v5` (pgvector) |
| Testing | `github.com/stretchr/testify` |

## Module

```
github.com/nareg/goagent   Go 1.25+
```
