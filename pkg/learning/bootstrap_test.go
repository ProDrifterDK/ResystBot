package learning

import (
	"context"
	"fmt"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
)

type fakeRuntimeEmbedder struct {
	order  *[]string
	vector []float64
	err    error
	text   string
}

func (f *fakeRuntimeEmbedder) Ping(ctx context.Context) error {
	*f.order = append(*f.order, "embed_ping")
	return nil
}

func (f *fakeRuntimeEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	return nil, fmt.Errorf("unexpected query embed: %s", text)
}

func (f *fakeRuntimeEmbedder) EmbedForIndexing(ctx context.Context, text string) ([]float64, error) {
	*f.order = append(*f.order, "embed_index")
	f.text = text
	if f.err != nil {
		return nil, f.err
	}
	return append([]float64(nil), f.vector...), nil
}

type fakeRuntimeStore struct {
	order      *[]string
	ensureSize int
	err        error
}

func (f *fakeRuntimeStore) Ping(ctx context.Context) error {
	*f.order = append(*f.order, "qdrant_ping")
	return nil
}

func (f *fakeRuntimeStore) EnsureCollection(ctx context.Context, vectorSize int) error {
	*f.order = append(*f.order, "ensure_collection")
	f.ensureSize = vectorSize
	return f.err
}

func (f *fakeRuntimeStore) Search(ctx context.Context, vector []float64, limit int, filter *memory.QdrantFilter) ([]memory.QdrantSearchResult, error) {
	return nil, nil
}

func (f *fakeRuntimeStore) Upsert(ctx context.Context, points []memory.QdrantPoint) error {
	return nil
}

func (f *fakeRuntimeStore) UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error {
	return nil
}

func TestInitializeRuntimeEnsuresCollectionBeforeWiring(t *testing.T) {
	t.Parallel()

	order := []string{}
	embedder := &fakeRuntimeEmbedder{order: &order, vector: []float64{0.1, 0.2, 0.3, 0.4}}
	store := &fakeRuntimeStore{order: &order}
	prevEmbedderFactory := newRuntimeEmbeddingClient
	prevStoreFactory := newRuntimeQdrantStore
	newRuntimeEmbeddingClient = func(cfg *config.LearningConfig) runtimeEmbeddingClient {
		if got, want := cfg.GetCollectionName(), "resystbot_learnings"; got != want {
			t.Fatalf("learning collection = %q, want %q", got, want)
		}
		return embedder
	}
	newRuntimeQdrantStore = func(cfg *config.LearningConfig) runtimeQdrantStore {
		return store
	}
	defer func() {
		newRuntimeEmbeddingClient = prevEmbedderFactory
		newRuntimeQdrantStore = prevStoreFactory
	}()

	runtime, err := InitializeRuntime(context.Background(), &config.LearningConfig{Enabled: true})
	if err != nil {
		t.Fatalf("InitializeRuntime() error = %v", err)
	}
	if runtime == nil {
		t.Fatal("expected runtime")
	}
	if runtime.Encoder == nil || runtime.Retriever == nil || runtime.OutcomeExtractor == nil {
		t.Fatal("expected encoder, retriever, and outcome extractor to be wired")
	}
	if runtime.VectorSize != 4 {
		t.Fatalf("vector size = %d, want 4", runtime.VectorSize)
	}
	if store.ensureSize != 4 {
		t.Fatalf("EnsureCollection size = %d, want 4", store.ensureSize)
	}
	if len(order) != 4 {
		t.Fatalf("bootstrap order length = %d, want 4 (%v)", len(order), order)
	}
	wantOrder := []string{"embed_ping", "qdrant_ping", "embed_index", "ensure_collection"}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("bootstrap order = %v, want %v", order, wantOrder)
		}
	}
	if embedder.text != learningBootstrapProbeText {
		t.Fatalf("bootstrap probe text = %q, want %q", embedder.text, learningBootstrapProbeText)
	}
}
