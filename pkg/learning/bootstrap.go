package learning

import (
	"context"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
)

const (
	learningBootstrapProbeText     = "learning bootstrap collection dimension probe"
	maxBootstrapPingRetries        = 5
	bootstrapPingRetryDelay        = 2 * time.Second
	bootstrapPingTimeout           = 5 * time.Second
)

type runtimeEmbeddingClient interface {
	embeddingClient
	Ping(ctx context.Context) error
}

type runtimeQdrantStore interface {
	qdrantStore
	Ping(ctx context.Context) error
	EnsureCollection(ctx context.Context, vectorSize int) error
}

type Runtime struct {
	Encoder          *Encoder
	Retriever        *LearningRetriever
	OutcomeExtractor *OutcomeExtractor
	VectorSize       int
}

var newRuntimeEmbeddingClient = func(cfg *config.LearningConfig) runtimeEmbeddingClient {
	return memory.NewEmbeddingClient(cfg.GetEmbeddingURL(), cfg.GetEmbeddingModel())
}

var newRuntimeQdrantStore = func(cfg *config.LearningConfig) runtimeQdrantStore {
	return memory.NewQdrantClient(cfg.GetQdrantURL(), cfg.GetCollectionName())
}

func InitializeRuntime(ctx context.Context, cfg *config.LearningConfig) (*Runtime, error) {
	if cfg == nil {
		return nil, fmt.Errorf("learning bootstrap: nil config")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	embedder := newRuntimeEmbeddingClient(cfg)
	store := newRuntimeQdrantStore(cfg)

	// Retry pings with backoff — embedding server may still be starting
	var embedErr, qdrantErr error
	for attempt := 0; attempt < maxBootstrapPingRetries; attempt++ {
		pingCtx, pingCancel := context.WithTimeout(ctx, bootstrapPingTimeout)
		if embedErr == nil {
			embedErr = embedder.Ping(pingCtx)
		}
		if qdrantErr == nil {
			qdrantErr = store.Ping(pingCtx)
		}
		pingCancel()
		if embedErr == nil && qdrantErr == nil {
			break
		}
		if attempt < maxBootstrapPingRetries-1 {
			time.Sleep(bootstrapPingRetryDelay)
		}
	}
	if embedErr != nil {
		return nil, fmt.Errorf("learning bootstrap: embedding ping after %d retries: %w", maxBootstrapPingRetries, embedErr)
	}
	if qdrantErr != nil {
		return nil, fmt.Errorf("learning bootstrap: qdrant ping after %d retries: %w", maxBootstrapPingRetries, qdrantErr)
	}

	vector, err := embedder.EmbedForIndexing(ctx, learningBootstrapProbeText)
	if err != nil {
		return nil, fmt.Errorf("learning bootstrap: derive vector size: %w", err)
	}
	if len(vector) == 0 {
		return nil, fmt.Errorf("learning bootstrap: derive vector size: empty embedding")
	}
	if err := store.EnsureCollection(ctx, len(vector)); err != nil {
		return nil, fmt.Errorf("learning bootstrap: ensure collection: %w", err)
	}

	encoder := NewEncoder(store, embedder, cfg)
	retriever := NewLearningRetriever(store, embedder, cfg)
	return &Runtime{
		Encoder:          encoder,
		Retriever:        retriever,
		OutcomeExtractor: NewOutcomeExtractor(encoder, cfg),
		VectorSize:       len(vector),
	}, nil
}
