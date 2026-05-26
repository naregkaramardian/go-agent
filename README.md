# go-agent

A production-grade AI agent runtime in pure Go — no Python frameworks, no LangChain-style abstractions.

Tool use, memory, guardrails, multi-agent orchestration, and full observability (structured logs, distributed traces, Prometheus metrics) built from idiomatic Go primitives.

## Table of Contents

- [Requirements](#requirements)
- [Setup](#setup)
- [Running the agent](#running-the-agent)
  - [Interactive REPL](#interactive-repl)
  - [Single-turn ask](#single-turn-ask)
  - [Pipe input from stdin](#pipe-input-from-stdin)
- [Agent presets](#agent-presets)
- [Multi-agent workflows](#multi-agent-workflows)
- [All flags](#all-flags)
- [Observability](#observability)
- [Architecture](#architecture)
- [Tech stack](#tech-stack)

---

## Requirements

- Go 1.22+
- An API key for **Anthropic** (`ANTHROPIC_API_KEY`) or **OpenAI** (`OPENAI_API_KEY`)

---

## Setup

**1. Clone and install dependencies**

```bash
git clone <repo-url>
cd go-agent
go mod download
```

**2. Configure your API key**

```bash
cp .env.example .env
```

Open `.env` and fill in your key:

```dotenv
# Use one or both — Anthropic takes priority if both are set
ANTHROPIC_API_KEY=sk-ant-...
OPENAI_API_KEY=sk-...
```

**3. (Optional) Build a binary**

```bash
go build -o goagent .
# Then use ./goagent instead of go run . in the examples below
```

---

## Running the agent

### Interactive REPL

Starts a persistent chat session. Type your messages and press Enter. Use `/exit` or Ctrl+D to quit.

```bash
go run . run
```

```
goagent · gpt-4o-mini · openai
Ctrl+D or /exit to quit
──────────────────────────────────────────────────

You: explain goroutines in one paragraph

Agent: Goroutines are lightweight, concurrently executing functions managed by the
Go runtime rather than the OS...

You:
```

### Single-turn ask

Sends one message, prints the response, and exits. Good for scripts and one-off queries.

```bash
go run . ask "What is the capital of France?"
go run . ask "Write a Go function that debounces a channel"
```

### Pipe input from stdin

```bash
echo "Summarise this in 3 bullet points" | go run . ask
cat myfile.go | go run . ask "Review this code for bugs"
```

---

## Agent presets

Presets are role-specific agents with tuned system prompts, model tiers, and guardrail limits. All presets are SOC2-aligned — they never output credentials, PII, or secrets.

```bash
# List all presets
go run . agents list
```

```
ID                TIER        DEFAULT MODEL       DESCRIPTION
senior-engineer   balanced    claude-sonnet-4-5   Writes, refactors, and reviews production-grade code...
security-reviewer powerful    claude-opus-4-5     Audits code for OWASP risks and SOC2 compliance gaps...
code-reviewer     balanced    claude-sonnet-4-5   Reviews PRs for correctness, coverage, and conventions...
software-architect powerful   claude-opus-4-5     Designs systems, writes ADRs, evaluates trade-offs...
db-specialist     balanced    claude-sonnet-4-5   Designs schemas, writes safe migrations, optimises queries...
test-engineer     balanced    claude-sonnet-4-5   Designs unit, integration, E2E, and security test strategies...
```

**Use a preset in REPL mode:**

```bash
go run . run --agent-type senior-engineer
go run . run --agent-type security-reviewer
go run . run --agent-type software-architect
```

**Use a preset for a single question:**

```bash
go run . ask --agent-type code-reviewer "Review this PR diff: ..."
go run . ask --agent-type db-specialist "Design a schema for a multi-tenant SaaS"
go run . ask --agent-type test-engineer "Write tests for this Go HTTP handler"
```

---

## Multi-agent workflows

Workflows chain multiple specialised agents together. There are three orchestration primitives:

| Primitive | Behaviour |
|---|---|
| **Pipeline** | Steps run sequentially; each step sees all prior outputs |
| **Parallel** | Steps run concurrently on the same input; results collected in order |
| **ReviewLoop** | Worker and reviewer alternate until the reviewer approves or max rounds is reached |

**List available workflows:**

```bash
go run . orchestrate list
```

```
ID              NAME            DESCRIPTION
code-review     Code Review     Senior engineer writes; code reviewer approves or requests changes (up to 3 rounds)
feature-build   Feature Build   Architect designs, senior engineer implements, test engineer writes tests
security-audit  Security Audit  Security reviewer + code reviewer run in parallel; engineer synthesises findings
full-pipeline   Full Pipeline   Architect → (engineer + DB specialist in parallel) → security audit → test plan
```

**Run a workflow:**

```bash
# Code review loop (worker + reviewer, up to 3 rounds)
go run . orchestrate run --workflow code-review "Write a thread-safe LRU cache in Go"

# Feature build pipeline (architect → engineer → test engineer)
go run . orchestrate run --workflow feature-build "Build a REST API for user authentication with JWT"

# Security audit (parallel security + code review, then remediation)
go run . orchestrate run --workflow security-audit "$(cat myhandler.go)"

# Full pipeline (all 4 agents, 2 in parallel)
go run . orchestrate run --workflow full-pipeline "Design a payment processing microservice"
```

**Pipe a task from stdin:**

```bash
cat spec.md | go run . orchestrate run --workflow feature-build
```

---

## All flags

All flags are global and work with every command.

```
--agent-type string      Agent preset ID (overrides --model, --system, --max-steps, --max-tokens)
--cost-budget float      USD cost limit per run; 0 = unlimited (default: 1.00)
--env-file string        Path to .env file; empty string disables loading (default: ".env")
--log-file string        Write logs to this file; default is discard (no log output)
--log-format string      "text" (coloured) or "json" (structured) (default: "text")
--max-steps int          Max agent loop iterations per run (default: 20)
--max-tokens int         Max tokens per LLM response (default: 2048)
--metrics-addr string    Address for Prometheus /metrics endpoint (default: ":9090")
--model string           Override the model ID (default: claude-haiku-4-5 or gpt-4o-mini)
--otlp-endpoint string   OTLP collector endpoint; empty = no-op exporter
--provider string        Force a provider: "anthropic" or "openai" (default: auto-detect)
--system string          Override the agent system prompt
--token-budget int       Conversation context token budget (default: 100000)
-v, --verbose            Print logs to stderr
```

**Common combinations:**

```bash
# Use a more capable model
go run . run --model gpt-4o
go run . run --model claude-sonnet-4-5

# Lower cost limit for experimentation
go run . run --cost-budget 0.10

# Save logs to a file for debugging
go run . run --log-file agent.log

# Print logs to terminal while chatting
go run . run --verbose

# Force a specific provider
go run . run --provider anthropic
go run . run --provider openai

# Use a custom system prompt
go run . run --system "You are a terse Go code reviewer. Be blunt."

# Use a different .env file
go run . run --env-file .env.production
```

---

## Observability

Every operation emits three signals simultaneously:

- **Structured logs** via `log/slog` — coloured text in dev, JSON in production
- **Distributed traces** via OpenTelemetry — `trace_id` and `span_id` auto-injected into every log line
- **Prometheus metrics** — scraped at `/metrics`

**View logs:**

```bash
# Pretty coloured logs to stderr
go run . run --verbose

# JSON logs to a file
go run . run --log-format json --log-file agent.log
tail -f agent.log | jq .
```

**Scrape Prometheus metrics:**

```bash
# Metrics endpoint starts automatically on :9090
curl http://localhost:9090/metrics | grep goagent_

# Key metrics
# goagent_llm_requests_total       — LLM API calls by model and status
# goagent_llm_latency_seconds      — LLM call latency histogram
# goagent_llm_tokens_total         — token usage by model and direction
# goagent_llm_cost_usd_total       — cumulative cost by model
# goagent_tool_calls_total         — tool invocations by name and status
# goagent_agent_steps_total        — agent loop iterations
# goagent_orchestration_runs_total — workflow runs by type and status

# Use a different port
go run . run --metrics-addr :2112
```

**Send traces to an OTLP collector (e.g. Jaeger, Tempo, Honeycomb):**

```bash
# Via flag
go run . run --otlp-endpoint localhost:4318

# Via environment variable
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318 go run . run

# Or add to .env
echo "OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4318" >> .env
```

---

## Architecture

```
go-agent/
├── core/            # Agent loop (plan → act → observe), context/token management
├── llm/             # LLMClient interface, Anthropic + OpenAI implementations, streaming
├── tools/           # Tool interface, registry, JSON schema generation
│   └── builtin/     # bash, file_read, file_write, http
├── memory/          # Short-term keyword-scored buffer; long-term vector store (planned)
├── guardrails/      # Chainable middleware: MaxSteps, TokenBudget, CostBudget, LoopDetection
├── observability/   # slog logger, OTel tracer, Prometheus metrics, cost ledger
├── agents/          # Named agent presets with SOC2-aligned system prompts
├── orchestration/   # Pipeline, Parallel, ReviewLoop primitives + pre-wired workflows
├── cli/             # Cobra commands: run, ask, agents, orchestrate
└── main.go
```

---

## Tech stack

| Concern | Library |
|---|---|
| LLM — Anthropic | `github.com/anthropics/anthropic-sdk-go` |
| LLM — OpenAI | `github.com/openai/openai-go` |
| Logging | `log/slog` + `github.com/lmittmann/tint` |
| Tracing | `go.opentelemetry.io/otel` + OTLP HTTP exporter |
| Metrics | `github.com/prometheus/client_golang` |
| CLI | `github.com/spf13/cobra` |
| Env file | `github.com/joho/godotenv` |
| Testing | `github.com/stretchr/testify` |

```
Module: github.com/nareg/goagent   Go 1.22+
```
