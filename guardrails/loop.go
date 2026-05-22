package guardrails

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/nareg/goagent/llm"
	"github.com/nareg/goagent/observability"
)

// LoopDetection fires when the same tool-call fingerprint appears consecutively
// for windowSize steps. Guards against infinite retry loops where the model
// calls the same tool(s) with the same arguments over and over.
// The state is per-middleware instance; create one per Agent.
func LoopDetection(windowSize int) Middleware {
	if windowSize < 2 {
		windowSize = 2
	}
	var (
		mu      sync.Mutex
		history []string
	)
	return func(ctx context.Context, step AgentStep, next StepHandler) (AgentStep, error) {
		s, err := next(ctx, step)
		if err != nil {
			return s, err
		}
		if s.Response == nil || len(s.Response.ToolCalls) == 0 {
			// Terminal or text-only response — reset so future loops start fresh.
			mu.Lock()
			history = history[:0]
			mu.Unlock()
			return s, nil
		}

		fp := toolCallFingerprint(s.Response.ToolCalls)
		mu.Lock()
		history = append(history, fp)
		if len(history) > windowSize {
			history = history[len(history)-windowSize:]
		}
		detected := len(history) == windowSize && allEqual(history)
		mu.Unlock()

		if detected {
			log := observability.LoggerFrom(ctx)
			observability.GuardrailTriggersTotal.WithLabelValues("loop_detection", "blocked").Inc()
			log.WarnContext(ctx, "guardrail.triggered",
				slog.String("guardrail", "loop_detection"),
				slog.String("action", "blocked"),
				slog.String("fingerprint", observability.Truncate(fp, 200)),
				slog.Int("window", windowSize),
				slog.String("status", "error"),
			)
			return s, fmt.Errorf("guardrails.LoopDetection: repeated tool-call pattern over %d consecutive steps", windowSize)
		}
		return s, nil
	}
}

// toolCallFingerprint produces a stable string key for a set of tool calls.
// Sorted so call order doesn't affect the fingerprint (parallel calls may arrive in any order).
func toolCallFingerprint(calls []llm.ToolCall) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		parts[i] = c.Name + ":" + string(c.Input)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func allEqual(ss []string) bool {
	for i := 1; i < len(ss); i++ {
		if ss[i] != ss[0] {
			return false
		}
	}
	return true
}
