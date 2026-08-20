package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type testLLMProvider struct{}

func (m *testLLMProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "ok", ToolCalls: []providers.ToolCall{}}, nil
}

func (m *testLLMProvider) GetDefaultModel() string {
	return "mock-model"
}

func newTestServer(t *testing.T, withAgentLoop bool) (*Server, *httptest.Server) {
	t.Helper()

	msgBus := bus.NewMessageBus()
	var loop *agent.AgentLoop
	if withAgentLoop {
		cfg := config.DefaultConfig()
		cfg.Agents.Defaults.Workspace = t.TempDir()
		cfg.Agents.Defaults.Provider = "mock"
		loop = agent.NewAgentLoop(cfg, msgBus, &testLLMProvider{})
	}

	s := NewServer(loop, msgBus, "test", "127.0.0.1", 0)
	ts := httptest.NewServer(s.httpServer.Handler)

	t.Cleanup(func() {
		ts.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := s.Stop(ctx)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("server stop failed: %v", err)
		}
	})

	return s, ts
}

func postJSON(t *testing.T, client *http.Client, url string, payload any) *http.Response {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post request failed: %v", err)
	}

	return resp
}

func decodeJSONMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode json response: %v", err)
	}
	return out
}

func TestHandleMessage_ValidRequest(t *testing.T) {
	_, ts := newTestServer(t, true)

	resp := postJSON(t, ts.Client(), ts.URL+"/v1/message", map[string]any{
		"chat_id":  "chat-1",
		"text":     "hello",
		"user":     "user",
		"username": "username",
	})

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, resp.StatusCode)
	}

	body := decodeJSONMap(t, resp)
	if body["status"] != "processing" {
		t.Fatalf("expected status processing, got %v", body["status"])
	}
	streamID, ok := body["stream_id"].(string)
	if !ok || streamID == "" {
		t.Fatalf("expected non-empty stream_id, got %v", body["stream_id"])
	}
}

func TestHandleMessage_MissingChatID(t *testing.T) {
	_, ts := newTestServer(t, false)

	resp := postJSON(t, ts.Client(), ts.URL+"/v1/message", map[string]any{
		"text": "hello",
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}

	body := decodeJSONMap(t, resp)
	if body["error"] != "chat_id and text are required" {
		t.Fatalf("unexpected error: %v", body["error"])
	}
}

func TestHandleMessage_MissingText(t *testing.T) {
	_, ts := newTestServer(t, false)

	resp := postJSON(t, ts.Client(), ts.URL+"/v1/message", map[string]any{
		"chat_id": "chat-1",
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}

	body := decodeJSONMap(t, resp)
	if body["error"] != "chat_id and text are required" {
		t.Fatalf("unexpected error: %v", body["error"])
	}
}

func TestHandleMessage_InvalidJSON(t *testing.T) {
	_, ts := newTestServer(t, false)

	resp, err := ts.Client().Post(ts.URL+"/v1/message", "application/json", bytes.NewBufferString("{not-json"))
	if err != nil {
		t.Fatalf("post request failed: %v", err)
	}

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}

	body := decodeJSONMap(t, resp)
	if body["error"] != "invalid json" {
		t.Fatalf("unexpected error: %v", body["error"])
	}
}

func TestHandleStream_NotFound(t *testing.T) {
	_, ts := newTestServer(t, false)

	resp, err := ts.Client().Get(ts.URL + "/v1/stream/nonexistent")
	if err != nil {
		t.Fatalf("get request failed: %v", err)
	}

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, resp.StatusCode)
	}

	body := decodeJSONMap(t, resp)
	if body["error"] != "stream not found" {
		t.Fatalf("unexpected error: %v", body["error"])
	}
}

func TestHandleCancel_ValidRequest(t *testing.T) {
	s, ts := newTestServer(t, false)

	cancelled := false
	stream := &Stream{ID: "stream-1", ChatID: "chat-1", Events: make(chan Event, 1), CreatedAt: time.Now()}

	s.mu.Lock()
	s.streams[stream.ID] = stream
	s.chatStreams[stream.ChatID] = stream.ID
	s.runs[stream.ChatID] = &chatRun{
		id: stream.ID, chatID: stream.ChatID, stream: stream,
		cancel: func() { cancelled = true }, done: make(chan struct{}),
	}
	s.mu.Unlock()

	resp := postJSON(t, ts.Client(), ts.URL+"/v1/cancel", map[string]any{
		"chat_id": "chat-1",
	})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	body := decodeJSONMap(t, resp)
	if body["status"] != "cancelled" {
		t.Fatalf("expected status cancelled, got %v", body["status"])
	}
	if !cancelled {
		t.Fatal("expected cancel function to be called")
	}
	if got := s.getStream(stream.ID); got != nil {
		t.Fatalf("expected stream to be removed, got %+v", got)
	}
	if got := s.getStreamIDByChat(stream.ChatID); got != "" {
		t.Fatalf("expected chat stream mapping removed, got %q", got)
	}
}

func TestHandleCancel_MissingChatID(t *testing.T) {
	_, ts := newTestServer(t, false)

	resp := postJSON(t, ts.Client(), ts.URL+"/v1/cancel", map[string]any{})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.StatusCode)
	}

	body := decodeJSONMap(t, resp)
	if body["error"] != "chat_id is required" {
		t.Fatalf("unexpected error: %v", body["error"])
	}
}

func TestHandleSessionReset_PublicSeam(t *testing.T) {
	s, ts := newTestServer(t, true)
	s.channel = "telegram"

	messageResp := postJSON(t, ts.Client(), ts.URL+"/v1/message", map[string]any{
		"chat_id":  "chat-reset",
		"text":     "hello",
		"user":     "user",
		"username": "username",
	})
	if messageResp.StatusCode != http.StatusAccepted {
		t.Fatalf("message status = %d, want %d", messageResp.StatusCode, http.StatusAccepted)
	}
	messageResp.Body.Close()
	s.wg.Wait()

	resp := postJSON(t, ts.Client(), ts.URL+"/v1/session/reset", map[string]any{
		"chat_id": "chat-reset",
		"mode":    "soft",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body := decodeJSONMap(t, resp)
	if body["status"] != "reset" {
		t.Fatalf("status = %v, want reset", body["status"])
	}
	if body["session_key"] != "telegram:chat-reset" {
		t.Fatalf("session_key = %v, want telegram:chat-reset", body["session_key"])
	}
	if body["cleared_messages"] != float64(2) {
		t.Fatalf("cleared_messages = %v, want 2", body["cleared_messages"])
	}
}

func TestHandleStatus_Empty(t *testing.T) {
	_, ts := newTestServer(t, false)

	resp, err := ts.Client().Get(ts.URL + "/v1/status")
	if err != nil {
		t.Fatalf("get request failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	defer resp.Body.Close()
	var body struct {
		ActiveChats   []string `json:"active_chats"`
		ActiveStreams []string `json:"active_streams"`
		Uptime        string   `json:"uptime"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode json response: %v", err)
	}

	if len(body.ActiveChats) != 0 {
		t.Fatalf("expected no active chats, got %v", body.ActiveChats)
	}
	if len(body.ActiveStreams) != 0 {
		t.Fatalf("expected no active streams, got %v", body.ActiveStreams)
	}
}
