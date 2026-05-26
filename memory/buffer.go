package memory

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/nareg/goagent/observability"
)

// Buffer is an in-process short-term memory store bounded by capacity.
// Recall uses keyword-overlap scoring — no embeddings required.
// When the buffer is full the oldest entry is evicted (FIFO).
// Safe for concurrent use.
type Buffer struct {
	mu       sync.Mutex
	entries  []MemoryEntry
	capacity int // 0 = unlimited
}

// NewBuffer returns a Buffer with the given maximum capacity (0 = unlimited).
func NewBuffer(capacity int) *Buffer {
	return &Buffer{capacity: capacity}
}

// Len returns the current number of stored entries.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// Store adds an entry, auto-generating ID and CreatedAt if unset.
// When over capacity the oldest entry is silently evicted.
func (b *Buffer) Store(ctx context.Context, entry MemoryEntry) error {
	ctx, span := otel.Tracer("go-agent").Start(ctx, "memory.store",
		trace.WithAttributes(
			attribute.Int("memory.capacity", b.capacity),
		),
	)
	defer span.End()

	log := observability.LoggerFrom(ctx)
	start := time.Now()

	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	entry.Score = 0

	b.mu.Lock()
	if b.capacity > 0 && len(b.entries) >= b.capacity {
		b.entries = b.entries[1:] // evict oldest
	}
	b.entries = append(b.entries, entry)
	count := len(b.entries)
	b.mu.Unlock()

	dur := time.Since(start)
	span.SetAttributes(
		attribute.String("memory.entry_id", entry.ID),
		attribute.Int("memory.count", count),
		attribute.Int64("latency_ms", dur.Milliseconds()),
		attribute.String("status", "ok"),
	)
	observability.MemoryStoreLatency.Observe(dur.Seconds())

	log.InfoContext(ctx, "memory.store",
		slog.String("entry_id", entry.ID),
		slog.String("content_preview", observability.Truncate(entry.Content, 200)),
		slog.Int("count", count),
		slog.Int64("latency_ms", dur.Milliseconds()),
		slog.String("status", "ok"),
	)
	return nil
}

// Recall returns up to topK entries ranked by keyword-overlap score against
// query. Entries with zero overlap are excluded. topK <= 0 returns all matches.
func (b *Buffer) Recall(ctx context.Context, query string, topK int) ([]MemoryEntry, error) {
	ctx, span := otel.Tracer("go-agent").Start(ctx, "memory.recall",
		trace.WithAttributes(
			attribute.String("memory.query", observability.Truncate(query, 100)),
			attribute.Int("memory.top_k", topK),
		),
	)
	defer span.End()

	log := observability.LoggerFrom(ctx)
	start := time.Now()

	b.mu.Lock()
	snapshot := make([]MemoryEntry, len(b.entries))
	copy(snapshot, b.entries)
	b.mu.Unlock()

	queryWords := tokenise(query)
	scored := make([]MemoryEntry, 0, len(snapshot))
	for _, e := range snapshot {
		if s := overlapScore(queryWords, e.Content); s > 0 {
			e.Score = s
			scored = append(scored, e)
		}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}

	dur := time.Since(start)
	hit := len(scored) > 0
	span.SetAttributes(
		attribute.Int("memory.results", len(scored)),
		attribute.Bool("memory.hit", hit),
		attribute.Int64("latency_ms", dur.Milliseconds()),
		attribute.String("status", "ok"),
	)
	observability.MemoryRecallLatency.Observe(dur.Seconds())

	if hit {
		observability.MemoryRecallHitsTotal.Add(float64(len(scored)))
		log.InfoContext(ctx, "memory.recall.hit",
			slog.String("query", observability.Truncate(query, 200)),
			slog.Int("results", len(scored)),
			slog.Int64("latency_ms", dur.Milliseconds()),
			slog.String("status", "ok"),
		)
	} else {
		observability.MemoryRecallMissesTotal.Inc()
		log.InfoContext(ctx, "memory.recall.miss",
			slog.String("query", observability.Truncate(query, 200)),
			slog.Int64("latency_ms", dur.Milliseconds()),
			slog.String("status", "ok"),
		)
	}

	return scored, nil
}

// tokenise lowercases s and splits it into words, stripping punctuation.
func tokenise(s string) []string {
	words := strings.Fields(strings.ToLower(s))
	out := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.Trim(w, `.,!?;:"'()-`)
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// overlapScore returns the fraction of queryWords present in content (0–1).
// A score of 1.0 means every query word appears in the content.
func overlapScore(queryWords []string, content string) float64 {
	if len(queryWords) == 0 {
		return 0
	}
	contentSet := make(map[string]struct{}, len(content)/5)
	for _, w := range tokenise(content) {
		contentSet[w] = struct{}{}
	}
	var matches int
	for _, w := range queryWords {
		if _, ok := contentSet[w]; ok {
			matches++
		}
	}
	return float64(matches) / float64(len(queryWords))
}
