.PHONY: run ask orchestrate build down reset logs test

# ── Primary commands ──────────────────────────────────────────────────────────

## Start the agent (interactive REPL). Boots postgres first if not running.
run:
	docker compose up -d postgres
	docker compose run --rm --service-ports goagent run

## Ask a single question. Usage: make ask MSG="explain goroutines"
ask:
	docker compose up -d postgres
	docker compose run --rm --service-ports goagent ask "$(MSG)"

## Run a multi-agent workflow. Usage: make orchestrate WORKFLOW=feature-build MSG="build a REST API"
orchestrate:
	docker compose up -d postgres
	docker compose run --rm --service-ports goagent orchestrate run --workflow $(WORKFLOW) "$(MSG)"

## Start the agent HTTP API server. Usage: make server API_KEY=mysecret
server:
	docker compose up -d postgres
	docker compose run --rm --service-ports \
	  -e API_KEY=$(API_KEY) goagent server

## List available workflows and agent presets.
list:
	docker compose run --rm goagent orchestrate list
	docker compose run --rm goagent agents list

# ── Image management ──────────────────────────────────────────────────────────

## Build (or rebuild) the goagent Docker image.
build:
	docker compose build goagent

# ── Lifecycle ─────────────────────────────────────────────────────────────────

## Stop all services (data is preserved).
down:
	docker compose down

## Wipe all stored memories and stop all services.
reset:
	docker compose down -v

## Tail postgres logs.
logs:
	docker compose logs -f postgres

# ── Development ───────────────────────────────────────────────────────────────

## Run the full test suite locally (requires Go).
test:
	go test ./...

## Run integration tests against the Docker postgres instance.
test-integration:
	docker compose up -d postgres
	MEMORY_DSN=postgres://goagent:goagent@localhost:5432/goagent go test ./memory/...
