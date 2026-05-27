package memory_test

import (
	"context"
	"os"
	"testing"

	"github.com/nareg/goagent/memory"
)

// stubEmbedder returns fixed-length zero vectors so tests don't hit the network.
type stubEmbedder struct{}

func (s *stubEmbedder) Dims() int { return 1536 }
func (s *stubEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	// Return a non-zero vector so cosine similarity doesn't blow up.
	v := make([]float32, 1536)
	v[0] = 1.0
	return v, nil
}

// skipUnlessDSN skips the test if MEMORY_DSN is not set.
// Run integration tests with: MEMORY_DSN=postgres://goagent:goagent@localhost:5432/goagent go test ./memory/...
func skipUnlessDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MEMORY_DSN")
	if dsn == "" {
		t.Skip("MEMORY_DSN not set — skipping pgvector integration test")
	}
	return dsn
}

func TestPgVectorMemory_StoreAndRecall(t *testing.T) {
	dsn := skipUnlessDSN(t)
	ctx := context.Background()

	m, err := memory.NewPgVectorMemory(ctx, dsn, &stubEmbedder{}, "test-agent")
	if err != nil {
		t.Fatalf("NewPgVectorMemory: %v", err)
	}
	defer m.Close()

	entry := memory.MemoryEntry{
		Content: "Go uses goroutines for concurrency",
		Tags:    []string{"go", "concurrency"},
	}
	if err := m.Store(ctx, entry); err != nil {
		t.Fatalf("Store: %v", err)
	}

	results, err := m.Recall(ctx, "goroutines", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result")
	}
	if results[0].Content != entry.Content {
		t.Errorf("content mismatch: got %q", results[0].Content)
	}
	if results[0].Score < 0 || results[0].Score > 1 {
		t.Errorf("score out of range [0,1]: %f", results[0].Score)
	}
}

func TestPgVectorMemory_RecallMiss(t *testing.T) {
	dsn := skipUnlessDSN(t)
	ctx := context.Background()

	// Use a unique agent ID so we don't see results from other tests.
	m, err := memory.NewPgVectorMemory(ctx, dsn, &stubEmbedder{}, "test-agent-empty-"+t.Name())
	if err != nil {
		t.Fatalf("NewPgVectorMemory: %v", err)
	}
	defer m.Close()

	results, err := m.Recall(ctx, "anything", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results for empty agent, got %d", len(results))
	}
}

func TestPgVectorMemory_TopKLimit(t *testing.T) {
	dsn := skipUnlessDSN(t)
	ctx := context.Background()

	m, err := memory.NewPgVectorMemory(ctx, dsn, &stubEmbedder{}, "test-agent-topk")
	if err != nil {
		t.Fatalf("NewPgVectorMemory: %v", err)
	}
	defer m.Close()

	for i := 0; i < 10; i++ {
		_ = m.Store(ctx, memory.MemoryEntry{Content: "memory entry"})
	}

	results, err := m.Recall(ctx, "memory", 3)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("expected at most 3 results with topK=3, got %d", len(results))
	}
}

func TestOpenAIEmbedder_SkipsWhenNoKey(t *testing.T) {
	// Just verify the constructor doesn't panic with an empty key.
	embedder := memory.NewOpenAIEmbedder("")
	if embedder == nil {
		t.Error("NewOpenAIEmbedder returned nil")
	}
	if embedder.Dims() != 1536 {
		t.Errorf("expected 1536 dims, got %d", embedder.Dims())
	}
}
