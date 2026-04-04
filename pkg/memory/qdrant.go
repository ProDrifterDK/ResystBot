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

// QdrantPoint represents a point to upsert into Qdrant.
type QdrantPoint struct {
	ID      string        `json:"id"`
	Vector  []float64     `json:"vector"`
	Payload QdrantPayload `json:"payload"`
}

// QdrantSearchResult represents a single search hit from Qdrant.
type QdrantSearchResult struct {
	ID      string        `json:"id"`
	Score   float64       `json:"score"`
	Vector  []float64     `json:"vector"`
	Payload QdrantPayload `json:"payload"`
}

// QdrantFilter holds optional filter fields for search queries.
type QdrantFilter struct {
	SourceType *string `json:"source_type,omitempty"`
}

// QdrantClient is a REST client for the Qdrant vector database.
type QdrantClient struct {
	baseURL    string
	collection string
	client     *http.Client
}

// NewQdrantClient creates a QdrantClient with a 30-second HTTP timeout.
func NewQdrantClient(baseURL, collection string) *QdrantClient {
	return &QdrantClient{
		baseURL:    baseURL,
		collection: collection,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Ping checks that Qdrant is reachable via GET /healthz.
func (q *QdrantClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.baseURL+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("qdrant ping: %w", err)
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant ping: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qdrant ping: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// EnsureCollection checks whether the collection exists and creates it if
// missing. After creation it adds payload indexes for full-text search on
// "text", keyword filtering on "source_type", datetime on "created_at", and
// integer on "importance".
func (q *QdrantClient) EnsureCollection(ctx context.Context, vectorSize int) error {
	// Check if collection already exists.
	checkURL := fmt.Sprintf("%s/collections/%s", q.baseURL, q.collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		return fmt.Errorf("qdrant ensure collection: %w", err)
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant ensure collection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil // Collection already exists.
	}

	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant ensure collection: unexpected status %d: %s", resp.StatusCode, body)
	}

	// Create the collection.
	createBody := map[string]any{
		"vectors": map[string]any{
			"size":     vectorSize,
			"distance": "Cosine",
		},
	}
	if err := q.putJSON(ctx, checkURL, createBody); err != nil {
		return fmt.Errorf("qdrant create collection: %w", err)
	}

	// Create payload indexes.
	indexURL := fmt.Sprintf("%s/collections/%s/index", q.baseURL, q.collection)

	indexes := []map[string]any{
		{
			"field_name": "text",
			"field_schema": map[string]any{
				"type":     "text",
				"tokenizer": "word",
				"min_token_len": 2,
			},
		},
		{
			"field_name":   "source_type",
			"field_schema": "keyword",
		},
		{
			"field_name":   "created_at",
			"field_schema": "datetime",
		},
		{
			"field_name":   "importance",
			"field_schema": "integer",
		},
		{
			"field_name":   "decay_score",
			"field_schema": "float",
		},
	}

	for _, idx := range indexes {
		if err := q.putJSON(ctx, indexURL, idx); err != nil {
			return fmt.Errorf("qdrant create index %v: %w", idx["field_name"], err)
		}
	}

	return nil
}

// Upsert inserts or updates points in the collection.
func (q *QdrantClient) Upsert(ctx context.Context, points []QdrantPoint) error {
	url := fmt.Sprintf("%s/collections/%s/points", q.baseURL, q.collection)
	body := map[string]any{
		"points": points,
	}
	return q.putJSON(ctx, url, body)
}

// Search performs a vector similarity search, optionally filtered by source type.
func (q *QdrantClient) Search(ctx context.Context, vector []float64, limit int, filter *QdrantFilter) ([]QdrantSearchResult, error) {
	url := fmt.Sprintf("%s/collections/%s/points/query", q.baseURL, q.collection)

	body := map[string]any{
		"query":        vector,
		"limit":        limit,
		"with_payload": true,
		"with_vectors": true,
	}

	if filter != nil && filter.SourceType != nil {
		body["filter"] = map[string]any{
			"must": []map[string]any{
				{
					"key": "source_type",
					"match": map[string]any{
						"value": *filter.SourceType,
					},
				},
			},
		}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("qdrant search marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("qdrant search: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qdrant search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("qdrant search: status %d: %s", resp.StatusCode, respBody)
	}

	// Response shape: {"result": {"points": [...]}}
	var raw struct {
		Result struct {
			Points []struct {
				ID      any           `json:"id"`
				Score   float64       `json:"score"`
				Vector  []float64     `json:"vector"`
				Payload QdrantPayload `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("qdrant search decode: %w", err)
	}

	results := make([]QdrantSearchResult, 0, len(raw.Result.Points))
	for _, p := range raw.Result.Points {
		results = append(results, QdrantSearchResult{
			ID:      anyToString(p.ID),
			Score:   p.Score,
			Vector:  p.Vector,
			Payload: p.Payload,
		})
	}

	return results, nil
}

// UpdatePayload updates specific payload fields on a point.
func (q *QdrantClient) UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error {
	url := fmt.Sprintf("%s/collections/%s/points/payload", q.baseURL, q.collection)
	body := map[string]any{
		"payload": fields,
		"points":  []string{pointID},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("qdrant update payload marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("qdrant update payload: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant update payload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant update payload: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// DeleteBySource deletes all points matching the given source value.
func (q *QdrantClient) DeleteBySource(ctx context.Context, source string) error {
	url := fmt.Sprintf("%s/collections/%s/points/delete", q.baseURL, q.collection)
	body := map[string]any{
		"filter": map[string]any{
			"must": []map[string]any{
				{
					"key": "source",
					"match": map[string]any{
						"value": source,
					},
				},
			},
		},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("qdrant delete by source marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("qdrant delete by source: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant delete by source: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant delete by source: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// ScrollPoint represents a point returned by the scroll API, including its vector.
type ScrollPoint struct {
	ID      string        `json:"id"`
	Vector  []float64     `json:"vector"`
	Payload QdrantPayload `json:"payload"`
}

// Scroll fetches points from the collection with pagination.
// Returns points, next page offset (nil if no more pages), and error.
func (q *QdrantClient) Scroll(ctx context.Context, limit int, offset *string, withVectors bool) ([]ScrollPoint, *string, error) {
	url := fmt.Sprintf("%s/collections/%s/points/scroll", q.baseURL, q.collection)

	body := map[string]any{
		"limit":        limit,
		"with_payload": true,
		"with_vectors": withVectors,
	}
	if offset != nil {
		body["offset"] = *offset
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal scroll request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, nil, fmt.Errorf("create scroll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("scroll request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("scroll returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Result struct {
			Points         []ScrollPoint `json:"points"`
			NextPageOffset *string       `json:"next_page_offset"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, fmt.Errorf("decode scroll response: %w", err)
	}

	return result.Result.Points, result.Result.NextPageOffset, nil
}

// DeleteByIDs deletes points by their IDs.
func (q *QdrantClient) DeleteByIDs(ctx context.Context, ids []string) error {
	url := fmt.Sprintf("%s/collections/%s/points/delete", q.baseURL, q.collection)

	body := map[string]any{
		"points": ids,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal delete request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create delete request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// putJSON sends a PUT request with a JSON body, returning an error on
// non-2xx status codes.
func (q *QdrantClient) putJSON(ctx context.Context, url string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// anyToString converts a value (string or number) to its string representation.
// Qdrant can return IDs as strings or integers.
func anyToString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	default:
		return fmt.Sprintf("%v", v)
	}
}
