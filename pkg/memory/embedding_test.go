package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// embeddingResponse mirrors the shape returned by the mock server.
type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Model string          `json:"model"`
}

type embeddingData struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

// makeEmbeddingHandler returns an http.HandlerFunc that validates the request
// body against wantInputs and replies with synthetic embeddings.
func makeEmbeddingHandler(t *testing.T, wantModel string, wantInputs []string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}

		var body struct {
			Model string      `json:"model"`
			Input interface{} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if body.Model != wantModel {
			t.Errorf("got model %q, want %q", body.Model, wantModel)
		}

		// Normalise input to []string regardless of whether single or batch.
		var inputs []string
		switch v := body.Input.(type) {
		case string:
			inputs = []string{v}
		case []interface{}:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					t.Errorf("non-string element in batch input: %T", item)
				}
				inputs = append(inputs, s)
			}
		default:
			t.Errorf("unexpected input type: %T", body.Input)
		}

		if len(inputs) != len(wantInputs) {
			t.Errorf("got %d inputs, want %d", len(inputs), len(wantInputs))
		} else {
			for i, in := range inputs {
				if in != wantInputs[i] {
					t.Errorf("input[%d]: got %q, want %q", i, in, wantInputs[i])
				}
			}
		}

		// Build a 3-element synthetic embedding per input.
		resp := embeddingResponse{Model: wantModel}
		for i := range inputs {
			resp.Data = append(resp.Data, embeddingData{
				Embedding: []float64{0.1 * float64(i+1), 0.2 * float64(i+1), 0.3 * float64(i+1)},
				Index:     i,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func TestEmbeddingClient_Embed(t *testing.T) {
	const model = "text-embedding-nomic-embed-text-v1.5"
	wantInput := "search_query: hello world"

	srv := httptest.NewServer(makeEmbeddingHandler(t, model, []string{wantInput}))
	defer srv.Close()

	client := NewEmbeddingClient(srv.URL+"/v1", model)
	vec, err := client.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-element vector, got %d", len(vec))
	}
	if vec[0] != 0.1 || vec[1] != 0.2 || vec[2] != 0.3 {
		t.Errorf("unexpected vector values: %v", vec)
	}
}

func TestEmbeddingClient_EmbedForIndexing(t *testing.T) {
	const model = "text-embedding-nomic-embed-text-v1.5"
	wantInput := "search_document: store this"

	srv := httptest.NewServer(makeEmbeddingHandler(t, model, []string{wantInput}))
	defer srv.Close()

	client := NewEmbeddingClient(srv.URL+"/v1", model)
	vec, err := client.EmbedForIndexing(context.Background(), "store this")
	if err != nil {
		t.Fatalf("EmbedForIndexing returned error: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-element vector, got %d", len(vec))
	}
}

func TestEmbeddingClient_EmbedBatch(t *testing.T) {
	const model = "text-embedding-nomic-embed-text-v1.5"
	texts := []string{"alpha", "beta", "gamma"}
	wantInputs := make([]string, len(texts))
	for i, t2 := range texts {
		wantInputs[i] = "search_document: " + t2
	}

	srv := httptest.NewServer(makeEmbeddingHandler(t, model, wantInputs))
	defer srv.Close()

	client := NewEmbeddingClient(srv.URL+"/v1", model)
	vecs, err := client.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedBatch returned error: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("expected %d vectors, got %d", len(texts), len(vecs))
	}
	for i, vec := range vecs {
		if len(vec) != 3 {
			t.Errorf("vecs[%d]: expected length 3, got %d", i, len(vec))
		}
	}
}

func TestEmbeddingClient_Ping_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewEmbeddingClient(srv.URL+"/v1", "any-model")
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping returned unexpected error: %v", err)
	}
}

func TestEmbeddingClient_ServerDown(t *testing.T) {
	// Use a server that is immediately closed so connections are refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close before the request

	client := NewEmbeddingClient(srv.URL+"/v1", "any-model")
	_, err := client.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error when server is down, got nil")
	}
	_ = strings.Contains(err.Error(), "connection") // silence unused import
}
