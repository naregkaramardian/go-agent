package memory

import (
	"context"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// EmbeddingClient generates dense vector embeddings for text.
// The returned slice has length Dims() and is normalised to unit length.
type EmbeddingClient interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Dims() int
}

// OpenAIEmbedder implements EmbeddingClient using text-embedding-3-small (1536 dims).
// Note: Anthropic has no native embedding API, so this client is used regardless
// of which provider handles chat completions.
type OpenAIEmbedder struct {
	client openai.Client
}

// NewOpenAIEmbedder returns an embedder backed by the given OpenAI API key.
func NewOpenAIEmbedder(apiKey string) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		client: openai.NewClient(option.WithAPIKey(apiKey)),
	}
}

// Dims returns the vector dimension produced by text-embedding-3-small.
func (e *OpenAIEmbedder) Dims() int { return 1536 }

// Embed returns the embedding vector for text.
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String(text),
		},
		Model:          openai.EmbeddingModelTextEmbedding3Small,
		EncodingFormat: openai.EmbeddingNewParamsEncodingFormatFloat,
	})
	if err != nil {
		return nil, fmt.Errorf("memory.embed: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("memory.embed: empty response from API")
	}
	raw := resp.Data[0].Embedding
	out := make([]float32, len(raw))
	for i, f := range raw {
		out[i] = float32(f)
	}
	return out, nil
}
