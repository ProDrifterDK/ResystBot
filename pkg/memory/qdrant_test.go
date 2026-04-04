package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestQdrantClient_Ping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "test_collection")
	err := c.Ping(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestQdrantClient_PingFail(t *testing.T) {
	// Use a URL that will refuse the connection.
	c := NewQdrantClient("http://127.0.0.1:1", "test_collection")
	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable server, got nil")
	}
}

func TestQdrantClient_EnsureCollection(t *testing.T) {
	var mu sync.Mutex
	var getReceived, putReceived bool
	var indexCreates []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.URL.Path == "/collections/test_col" && r.Method == http.MethodGet:
			getReceived = true
			// Simulate collection does not exist.
			w.WriteHeader(http.StatusNotFound)

		case r.URL.Path == "/collections/test_col" && r.Method == http.MethodPut:
			putReceived = true
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			json.Unmarshal(body, &payload)

			vectors, ok := payload["vectors"].(map[string]any)
			if !ok {
				t.Error("expected vectors object in create request")
			}
			if size, _ := vectors["size"].(float64); int(size) != 1536 {
				t.Errorf("expected vector size 1536, got %v", size)
			}
			if dist, _ := vectors["distance"].(string); dist != "Cosine" {
				t.Errorf("expected distance Cosine, got %v", dist)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":true}`))

		case r.URL.Path == "/collections/test_col/index" && r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			json.Unmarshal(body, &payload)
			fieldName, _ := payload["field_name"].(string)
			indexCreates = append(indexCreates, fieldName)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":true}`))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "test_col")
	err := c.EnsureCollection(context.Background(), 1536)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	if !getReceived {
		t.Error("expected GET to check collection existence")
	}
	if !putReceived {
		t.Error("expected PUT to create collection after 404")
	}

	// Verify indexes were created for text, source_type, created_at, importance.
	expectedIndexes := map[string]bool{
		"text":        false,
		"source_type": false,
		"created_at":  false,
		"importance":  false,
	}
	for _, name := range indexCreates {
		if _, ok := expectedIndexes[name]; ok {
			expectedIndexes[name] = true
		}
	}
	for name, found := range expectedIndexes {
		if !found {
			t.Errorf("expected index creation for field %q", name)
		}
	}
}

func TestQdrantClient_EnsureCollection_AlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/existing" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":{"status":"green"}}`))
			return
		}
		// PUT should NOT be called when collection already exists.
		if r.Method == http.MethodPut {
			t.Error("PUT should not be called when collection already exists")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "existing")
	err := c.EnsureCollection(context.Background(), 1536)
	if err != nil {
		t.Fatalf("EnsureCollection failed for existing collection: %v", err)
	}
}

func TestQdrantClient_Upsert(t *testing.T) {
	var receivedPoints int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test_col/points" && r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Points []json.RawMessage `json:"points"`
			}
			json.Unmarshal(body, &payload)
			receivedPoints = len(payload.Points)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":{"status":"completed"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "test_col")
	points := []QdrantPoint{
		{
			ID:     "point-1",
			Vector: []float64{0.1, 0.2, 0.3},
			Payload: QdrantPayload{
				Text:       "hello world",
				Source:     "test.md",
				SourceType: "memory_file",
			},
		},
		{
			ID:     "point-2",
			Vector: []float64{0.4, 0.5, 0.6},
			Payload: QdrantPayload{
				Text:       "second chunk",
				Source:     "test.md",
				SourceType: "memory_file",
			},
		},
		{
			ID:     "point-3",
			Vector: []float64{0.7, 0.8, 0.9},
			Payload: QdrantPayload{
				Text:       "third chunk",
				Source:     "test2.md",
				SourceType: "conversation",
			},
		},
	}

	err := c.Upsert(context.Background(), points)
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if receivedPoints != 3 {
		t.Errorf("expected 3 points, server received %d", receivedPoints)
	}
}

func TestQdrantClient_Search(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test_col/points/query" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			json.Unmarshal(body, &req)

			// Verify with_payload is set.
			if wp, ok := req["with_payload"].(bool); !ok || !wp {
				t.Error("expected with_payload to be true")
			}

			resp := `{
				"result": {
					"points": [
						{
							"id": "abc-123",
							"score": 0.95,
							"payload": {
								"text": "important memory",
								"source": "notes.md",
								"source_type": "memory_file",
								"chunk_type": "section",
								"importance": 7,
								"created_at": "2024-01-01T00:00:00Z",
								"last_accessed": "2024-01-02T00:00:00Z",
								"access_count": 3,
								"tags": ["project", "urgent"]
							}
						},
						{
							"id": 42,
							"score": 0.82,
							"payload": {
								"text": "another memory",
								"source": "chat.log",
								"source_type": "conversation",
								"chunk_type": "turn",
								"importance": 4,
								"created_at": "2024-01-03T00:00:00Z",
								"last_accessed": "",
								"access_count": 0,
								"tags": []
							}
						}
					]
				}
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(resp))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "test_col")
	results, err := c.Search(context.Background(), []float64{0.1, 0.2}, 5, nil)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First result: string ID.
	if results[0].ID != "abc-123" {
		t.Errorf("expected ID abc-123, got %s", results[0].ID)
	}
	if results[0].Score != 0.95 {
		t.Errorf("expected score 0.95, got %f", results[0].Score)
	}
	if results[0].Payload.Text != "important memory" {
		t.Errorf("expected text 'important memory', got %s", results[0].Payload.Text)
	}
	if results[0].Payload.Importance != 7 {
		t.Errorf("expected importance 7, got %d", results[0].Payload.Importance)
	}

	// Second result: integer ID (should be converted to string).
	if results[1].ID != "42" {
		t.Errorf("expected ID '42', got %s", results[1].ID)
	}
	if results[1].Score != 0.82 {
		t.Errorf("expected score 0.82, got %f", results[1].Score)
	}
}

func TestQdrantClient_SearchWithFilter(t *testing.T) {
	var receivedFilter bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test_col/points/query" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			json.Unmarshal(body, &req)

			// Verify filter is present.
			if f, ok := req["filter"]; ok && f != nil {
				receivedFilter = true
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":{"points":[]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "test_col")
	st := "memory_file"
	filter := &QdrantFilter{SourceType: &st}
	_, err := c.Search(context.Background(), []float64{0.1}, 5, filter)
	if err != nil {
		t.Fatalf("Search with filter failed: %v", err)
	}
	if !receivedFilter {
		t.Error("expected filter to be sent in search request")
	}
}

func TestQdrantClient_UpdatePayload(t *testing.T) {
	var receivedPayload map[string]any
	var receivedPointIDs []any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test_col/points/payload" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			json.Unmarshal(body, &req)
			if p, ok := req["payload"].(map[string]any); ok {
				receivedPayload = p
			}
			if pts, ok := req["points"].([]any); ok {
				receivedPointIDs = pts
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":{"status":"completed"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "test_col")
	fields := map[string]any{
		"access_count":  5,
		"last_accessed": "2024-06-01T12:00:00Z",
	}
	err := c.UpdatePayload(context.Background(), "point-abc", fields)
	if err != nil {
		t.Fatalf("UpdatePayload failed: %v", err)
	}

	if receivedPayload == nil {
		t.Fatal("expected payload in request body")
	}
	if ac, _ := receivedPayload["access_count"].(float64); int(ac) != 5 {
		t.Errorf("expected access_count 5, got %v", receivedPayload["access_count"])
	}
	if len(receivedPointIDs) != 1 {
		t.Errorf("expected 1 point ID, got %d", len(receivedPointIDs))
	}
}

func TestQdrantClient_DeleteBySource(t *testing.T) {
	var receivedFilter map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test_col/points/delete" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			json.Unmarshal(body, &req)
			if f, ok := req["filter"].(map[string]any); ok {
				receivedFilter = f
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":{"status":"completed"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "test_col")
	err := c.DeleteBySource(context.Background(), "old_notes.md")
	if err != nil {
		t.Fatalf("DeleteBySource failed: %v", err)
	}

	if receivedFilter == nil {
		t.Fatal("expected filter in delete request")
	}
	must, ok := receivedFilter["must"].([]any)
	if !ok || len(must) == 0 {
		t.Fatal("expected must clause in filter")
	}

	// Verify the filter matches source field.
	clause := must[0].(map[string]any)
	key, _ := clause["key"].(string)
	matchVal, _ := clause["match"].(map[string]any)
	value, _ := matchVal["value"].(string)
	if key != "source" {
		t.Errorf("expected filter key 'source', got %q", key)
	}
	if value != "old_notes.md" {
		t.Errorf("expected filter value 'old_notes.md', got %q", value)
	}
}

func TestQdrantClient_UpsertError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status":{"error":"internal error"}}`))
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "test_col")
	err := c.Upsert(context.Background(), []QdrantPoint{
		{ID: "x", Vector: []float64{0.1}},
	})
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestQdrantClient_SearchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"status":{"error":"search failed"}}`)
	}))
	defer srv.Close()

	c := NewQdrantClient(srv.URL, "test_col")
	_, err := c.Search(context.Background(), []float64{0.1}, 5, nil)
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}
