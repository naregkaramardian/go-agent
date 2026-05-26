package memory

import (
	"context"
	"time"
)

// MemoryEntry is a single stored memory.
type MemoryEntry struct {
	ID        string
	Content   string
	Tags      []string
	CreatedAt time.Time
	Score     float64 // populated by Recall; zero when stored
}

// Memory is the interface every memory backend must implement.
type Memory interface {
	Store(ctx context.Context, entry MemoryEntry) error
	Recall(ctx context.Context, query string, topK int) ([]MemoryEntry, error)
}
