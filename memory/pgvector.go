package memory

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nareg/goagent/observability"
)

// pgvectorSchema creates the extension, table, and HNSW index if they don't
// already exist. HNSW needs no pre-training and works well for small datasets.
const pgvectorSchema = `
CREATE TABLE IF NOT EXISTS goagent_memories (
    id         TEXT        PRIMARY KEY,
    content    TEXT        NOT NULL,
    embedding  vector(1536),
    tags       TEXT[]      NOT NULL DEFAULT '{}',
    agent_id   TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS goagent_memories_embedding_hnsw
    ON goagent_memories
    USING hnsw (embedding vector_cosine_ops);

CREATE INDEX IF NOT EXISTS goagent_memories_agent_id_idx
    ON goagent_memories (agent_id);
`

// PgVectorMemory stores and recalls memories using PostgreSQL + pgvector.
// Recall uses cosine similarity over OpenAI text-embedding-3-small vectors.
type PgVectorMemory struct {
	pool    *pgxpool.Pool
	embedder EmbeddingClient
	agentID  string
}

// NewPgVectorMemory connects to the database, registers the pgvector codec,
// runs the schema migration, and returns a ready-to-use PgVectorMemory.
//
// dsn example: "postgres://goagent:goagent@localhost:5432/goagent"
func NewPgVectorMemory(ctx context.Context, dsn string, embedder EmbeddingClient, agentID string) (*PgVectorMemory, error) {
	// Create the extension before opening the pool. AfterConnect calls
	// pgxvec.RegisterTypes which looks up the vector type in pg_type — that
	// lookup fails if the extension doesn't exist yet.
	setupConn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("memory.pgvector: setup connect: %w", err)
	}
	_, err = setupConn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector")
	_ = setupConn.Close(ctx)
	if err != nil {
		return nil, fmt.Errorf("memory.pgvector: create extension: %w", err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("memory.pgvector: parse DSN: %w", err)
	}

	// Register the pgvector codec on every new connection in the pool.
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("memory.pgvector: create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("memory.pgvector: ping: %w", err)
	}

	m := &PgVectorMemory{pool: pool, embedder: embedder, agentID: agentID}
	if err := m.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return m, nil
}

// Close releases the connection pool.
func (m *PgVectorMemory) Close() { m.pool.Close() }

// Store generates an embedding for entry.Content and inserts a row.
func (m *PgVectorMemory) Store(ctx context.Context, entry MemoryEntry) error {
	ctx, span := otel.Tracer("go-agent").Start(ctx, "memory.pgvector.store",
		trace.WithAttributes(attribute.String("agent_id", m.agentID)),
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
	if entry.Tags == nil {
		entry.Tags = []string{}
	}

	vec, err := m.embedder.Embed(ctx, entry.Content)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("memory.pgvector.store: embed: %w", err)
	}

	_, err = m.pool.Exec(ctx,
		`INSERT INTO goagent_memories (id, content, embedding, tags, agent_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (id) DO NOTHING`,
		entry.ID,
		entry.Content,
		pgvector.NewVector(vec),
		entry.Tags,
		m.agentID,
		entry.CreatedAt,
	)
	dur := time.Since(start)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		observability.MemoryStoreLatency.Observe(dur.Seconds())
		return fmt.Errorf("memory.pgvector.store: insert: %w", err)
	}

	span.SetAttributes(
		attribute.String("memory.entry_id", entry.ID),
		attribute.Int64("latency_ms", dur.Milliseconds()),
		attribute.String("status", "ok"),
	)
	observability.MemoryStoreLatency.Observe(dur.Seconds())

	log.InfoContext(ctx, "memory.store",
		slog.String("backend", "pgvector"),
		slog.String("entry_id", entry.ID),
		slog.String("content_preview", observability.Truncate(entry.Content, 200)),
		slog.Int64("latency_ms", dur.Milliseconds()),
		slog.String("status", "ok"),
	)
	return nil
}

// Recall returns the topK entries most similar to query, ranked by cosine
// similarity (Score = 1 − cosine_distance, range 0–1).
func (m *PgVectorMemory) Recall(ctx context.Context, query string, topK int) ([]MemoryEntry, error) {
	ctx, span := otel.Tracer("go-agent").Start(ctx, "memory.pgvector.recall",
		trace.WithAttributes(
			attribute.String("memory.query", observability.Truncate(query, 100)),
			attribute.Int("memory.top_k", topK),
			attribute.String("agent_id", m.agentID),
		),
	)
	defer span.End()

	log := observability.LoggerFrom(ctx)
	start := time.Now()

	if topK <= 0 {
		topK = 5
	}

	vec, err := m.embedder.Embed(ctx, query)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("memory.pgvector.recall: embed: %w", err)
	}

	rows, err := m.pool.Query(ctx,
		`SELECT id, content, tags, created_at,
		        1 - (embedding <=> $1) AS score
		 FROM   goagent_memories
		 WHERE  agent_id = $2
		   AND  embedding IS NOT NULL
		 ORDER  BY embedding <=> $1
		 LIMIT  $3`,
		pgvector.NewVector(vec),
		m.agentID,
		topK,
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("memory.pgvector.recall: query: %w", err)
	}
	defer rows.Close()

	var entries []MemoryEntry
	for rows.Next() {
		var e MemoryEntry
		var createdAt time.Time
		if err := rows.Scan(&e.ID, &e.Content, &e.Tags, &createdAt, &e.Score); err != nil {
			return nil, fmt.Errorf("memory.pgvector.recall: scan: %w", err)
		}
		e.CreatedAt = createdAt
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory.pgvector.recall: rows: %w", err)
	}

	dur := time.Since(start)
	hit := len(entries) > 0
	span.SetAttributes(
		attribute.Int("memory.results", len(entries)),
		attribute.Bool("memory.hit", hit),
		attribute.Int64("latency_ms", dur.Milliseconds()),
		attribute.String("status", "ok"),
	)
	observability.MemoryRecallLatency.Observe(dur.Seconds())

	if hit {
		observability.MemoryRecallHitsTotal.Add(float64(len(entries)))
		log.InfoContext(ctx, "memory.recall.hit",
			slog.String("backend", "pgvector"),
			slog.String("query", observability.Truncate(query, 200)),
			slog.Int("results", len(entries)),
			slog.Int64("latency_ms", dur.Milliseconds()),
			slog.String("status", "ok"),
		)
	} else {
		observability.MemoryRecallMissesTotal.Inc()
		log.InfoContext(ctx, "memory.recall.miss",
			slog.String("backend", "pgvector"),
			slog.String("query", observability.Truncate(query, 200)),
			slog.Int64("latency_ms", dur.Milliseconds()),
			slog.String("status", "ok"),
		)
	}

	return entries, nil
}

func (m *PgVectorMemory) migrate(ctx context.Context) error {
	_, err := m.pool.Exec(ctx, pgvectorSchema)
	if err != nil {
		return fmt.Errorf("memory.pgvector: migrate: %w", err)
	}
	return nil
}
