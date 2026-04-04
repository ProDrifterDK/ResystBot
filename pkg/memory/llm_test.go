package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLMClient_Complete(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": "This is a summary of the fragments.",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewLLMClient(server.URL+"/v1", "test-model", "test-key")
	result, err := client.Complete(context.Background(), "You are a helpful assistant.", "Summarize this.")
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if result != "This is a summary of the fragments." {
		t.Errorf("unexpected result: %s", result)
	}
	if receivedBody["model"] != "test-model" {
		t.Errorf("expected model test-model, got %v", receivedBody["model"])
	}
}

func TestLLMClient_Complete_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	client := NewLLMClient(server.URL+"/v1", "test-model", "test-key")
	_, err := client.Complete(context.Background(), "system", "user")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestLLMClient_Complete_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{},
		})
	}))
	defer server.Close()

	client := NewLLMClient(server.URL+"/v1", "test-model", "test-key")
	_, err := client.Complete(context.Background(), "system", "user")
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}
