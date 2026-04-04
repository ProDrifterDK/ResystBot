package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// EmbeddingClient calls LM Studio's OpenAI-compatible /v1/embeddings endpoint.
type EmbeddingClient struct {
	apiBase string
	model   string
	client  *http.Client
}

// NewEmbeddingClient creates an EmbeddingClient with a 30-second HTTP timeout.
func NewEmbeddingClient(apiBase, model string) *EmbeddingClient {
	return &EmbeddingClient{
		apiBase: apiBase,
		model:   model,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed embeds a retrieval query, prepending "search_query: " to the text.
func (e *EmbeddingClient) Embed(ctx context.Context, text string) ([]float64, error) {
	return e.embed(ctx, "search_query: "+text)
}

// EmbedForIndexing embeds text for storage, prepending "search_document: ".
func (e *EmbeddingClient) EmbedForIndexing(ctx context.Context, text string) ([]float64, error) {
	return e.embed(ctx, "search_document: "+text)
}

// EmbedBatch embeds multiple texts for storage (batch POST with "search_document: " prefix).
func (e *EmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = "search_document: " + t
	}
	return e.embedBatch(ctx, prefixed)
}

// Ping performs a GET /models request to verify the service is available.
func (e *EmbeddingClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.apiBase+"/models", nil)
	if err != nil {
		return fmt.Errorf("embedding ping: build request: %w", err)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("embedding ping: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("embedding ping: server returned %d", resp.StatusCode)
	}
	return nil
}

// embeddingRequest is the JSON body for single-input embedding calls.
type embeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// embeddingBatchRequest is the JSON body for multi-input embedding calls.
type embeddingBatchRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingAPIResponse mirrors the OpenAI embeddings response shape.
type embeddingAPIResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
}

// embed performs the actual HTTP POST to /embeddings for a single input.
func (e *EmbeddingClient) embed(ctx context.Context, input string) ([]float64, error) {
	body, err := json.Marshal(embeddingRequest{Model: e.model, Input: input})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apiBase+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed: server returned %d: %s", resp.StatusCode, raw)
	}

	var apiResp embeddingAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	if len(apiResp.Data) == 0 {
		return nil, fmt.Errorf("embed: empty data in response")
	}
	return apiResp.Data[0].Embedding, nil
}

// embedBatch performs the actual HTTP POST to /embeddings for multiple inputs.
func (e *EmbeddingClient) embedBatch(ctx context.Context, inputs []string) ([][]float64, error) {
	body, err := json.Marshal(embeddingBatchRequest{Model: e.model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("embedBatch: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apiBase+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedBatch: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedBatch: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedBatch: server returned %d: %s", resp.StatusCode, raw)
	}

	var apiResp embeddingAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("embedBatch: decode response: %w", err)
	}

	// Sort by index to guarantee order matches inputs.
	result := make([][]float64, len(apiResp.Data))
	for _, d := range apiResp.Data {
		if d.Index < 0 || d.Index >= len(result) {
			return nil, fmt.Errorf("embedBatch: out-of-range index %d", d.Index)
		}
		result[d.Index] = d.Embedding
	}
	return result, nil
}
