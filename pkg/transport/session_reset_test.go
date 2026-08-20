package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func newResetServer(t *testing.T, provider providers.LLMProvider) (*Server, *httptest.Server, string) {
	t.Helper()
	workspace := t.TempDir()
	msgBus := bus.NewMessageBus()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.Provider = "mock"
	cfg.Agents.Defaults.Model = "mock-model"
	cfg.Agents.Defaults.MaxToolIterations = 3
	loop := agent.NewAgentLoop(cfg, msgBus, provider)
	s := NewServer(loop, msgBus, "telegram", "127.0.0.1", 0)
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
	return s, ts, workspace
}

func sendTestMessage(t *testing.T, ts *httptest.Server, chatID, text string) *http.Response {
	t.Helper()
	return postJSON(t, ts.Client(), ts.URL+"/v1/message", map[string]any{
		"chat_id": chatID, "text": text, "user": "user", "username": "tester",
	})
}

func sendReset(t *testing.T, ts *httptest.Server, chatID string, mode agent.ResetMode) *http.Response {
	t.Helper()
	return postJSON(t, ts.Client(), ts.URL+"/v1/session/reset", map[string]any{
		"chat_id": chatID, "mode": mode,
	})
}

type asyncResetResponse struct {
	resp *http.Response
	err  error
}

func sendResetAsync(ts *httptest.Server, chatID string, mode agent.ResetMode) asyncResetResponse {
	body, err := json.Marshal(map[string]any{"chat_id": chatID, "mode": mode})
	if err != nil {
		return asyncResetResponse{err: err}
	}
	resp, err := ts.Client().Post(ts.URL+"/v1/session/reset", "application/json", bytes.NewReader(body))
	return asyncResetResponse{resp: resp, err: err}
}

func waitForRun(t *testing.T, s *Server, chatID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		run := s.runs[chatID]
		s.mu.RUnlock()
		if run == nil {
			return
		}
		select {
		case <-run.done:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatalf("run for %s did not finish", chatID)
}

func TestSessionResetValidationAndMethod(t *testing.T) {
	_, ts, _ := newResetServer(t, &testLLMProvider{})

	t.Run("method", func(t *testing.T) {
		resp, err := ts.Client().Get(ts.URL + "/v1/session/reset")
		require.NoError(t, err)
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
		assert.Equal(t, "method_not_allowed", decodeJSONMap(t, resp)["code"])
	})

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "invalid json", body: "{"},
		{name: "missing fields", body: `{}`},
		{name: "blank chat", body: `{"chat_id":"  ","mode":"soft"}`},
		{name: "missing mode", body: `{"chat_id":"1"}`},
		{name: "unknown mode", body: `{"chat_id":"1","mode":"medium"}`},
		{name: "unknown field", body: `{"chat_id":"1","mode":"soft","extra":true}`},
		{name: "trailing json", body: `{"chat_id":"1","mode":"soft"} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := ts.Client().Post(ts.URL+"/v1/session/reset", "application/json", strings.NewReader(tc.body))
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.Equal(t, "invalid_request", decodeJSONMap(t, resp)["code"])
		})
	}
}

func TestSessionResetSuccessPersistsExactCount(t *testing.T) {
	s, ts, workspace := newResetServer(t, &testLLMProvider{})

	resp := sendTestMessage(t, ts, "persist", "hello")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
	waitForRun(t, s, "persist")

	resp = sendReset(t, ts, "persist", agent.ResetModeSoft)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeJSONMap(t, resp)
	assert.Equal(t, "reset", body["status"])
	assert.Equal(t, "telegram:persist", body["session_key"])
	assert.Equal(t, "soft", body["mode"])
	assert.Equal(t, float64(2), body["cleared_messages"])
	assert.Equal(t, "preserved", body["summary_action"])
	assert.Equal(t, false, body["cancelled_in_flight"])

	reloaded := session.NewSessionManager(filepath.Join(workspace, "sessions"))
	defer reloaded.Index().Close()
	assert.Empty(t, reloaded.GetHistory("telegram:persist"))

	resp = sendReset(t, ts, "persist", agent.ResetModeHard)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body = decodeJSONMap(t, resp)
	assert.Equal(t, float64(0), body["cleared_messages"])
	assert.Equal(t, "cleared", body["summary_action"])
}

type cancelAwareProvider struct {
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (p *cancelAwareProvider) Chat(ctx context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	close(p.canceled)
	return nil, ctx.Err()
}

func (p *cancelAwareProvider) GetDefaultModel() string { return "mock-model" }

func TestSessionResetCancelsAndWaitsForProvider(t *testing.T) {
	provider := &cancelAwareProvider{started: make(chan struct{}), canceled: make(chan struct{})}
	_, ts, _ := newResetServer(t, provider)
	resp := sendTestMessage(t, ts, "cancel", "block")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}

	resp = sendReset(t, ts, "cancel", agent.ResetModeHard)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeJSONMap(t, resp)
	assert.Equal(t, true, body["cancelled_in_flight"])
	assert.Equal(t, float64(1), body["cleared_messages"])
	select {
	case <-provider.canceled:
	default:
		t.Fatal("reset returned before provider observed cancellation")
	}
}

type delayedCancellationProvider struct {
	started     chan struct{}
	sawCancel   chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	cancelOnce  sync.Once
}

func (p *delayedCancellationProvider) Chat(ctx context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	isBlocked := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, "block-a") {
			isBlocked = true
			break
		}
	}
	if !isBlocked {
		return &providers.LLMResponse{Content: "fast", ToolCalls: []providers.ToolCall{}}, nil
	}
	p.startedOnce.Do(func() { close(p.started) })
	<-ctx.Done()
	p.cancelOnce.Do(func() { close(p.sawCancel) })
	<-p.release
	return &providers.LLMResponse{Content: "late stale reply", ToolCalls: []providers.ToolCall{}}, nil
}

func (p *delayedCancellationProvider) GetDefaultModel() string { return "mock-model" }

func TestSessionResetWaitsForDelayedCancellationAndBlocksSameChat(t *testing.T) {
	provider := &delayedCancellationProvider{
		started: make(chan struct{}), sawCancel: make(chan struct{}), release: make(chan struct{}),
	}
	s, ts, workspace := newResetServer(t, provider)
	resp := sendTestMessage(t, ts, "a", "block-a")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
	<-provider.started

	resetResult := make(chan asyncResetResponse, 1)
	go func() { resetResult <- sendResetAsync(ts, "a", agent.ResetModeHard) }()
	select {
	case <-provider.sawCancel:
	case <-time.After(time.Second):
		t.Fatal("provider did not observe cancellation")
	}
	select {
	case result := <-resetResult:
		if result.resp != nil {
			result.resp.Body.Close()
		}
		t.Fatalf("reset returned before delayed provider quiesced: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}

	resp = sendTestMessage(t, ts, "a", "same chat")
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "session_resetting", decodeJSONMap(t, resp)["code"])

	resp = sendTestMessage(t, ts, "b", "different chat")
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
	waitForRun(t, s, "b")

	close(provider.release)
	result := <-resetResult
	require.NoError(t, result.err)
	resp = result.resp
	require.Equal(t, http.StatusOK, resp.StatusCode)
	decodeJSONMap(t, resp)

	reloaded := session.NewSessionManager(filepath.Join(workspace, "sessions"))
	defer reloaded.Index().Close()
	assert.Empty(t, reloaded.GetHistory("telegram:a"))
}

type predecessorProvider struct {
	firstStarted  chan struct{}
	firstCanceled chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
	releaseSecond chan struct{}
}

func (p *predecessorProvider) Chat(ctx context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		switch {
		case strings.Contains(msg.Content, "second"):
			close(p.secondStarted)
			<-p.releaseSecond
			return &providers.LLMResponse{Content: "second done", ToolCalls: []providers.ToolCall{}}, nil
		case strings.Contains(msg.Content, "first"):
			close(p.firstStarted)
			<-ctx.Done()
			close(p.firstCanceled)
			<-p.releaseFirst
			return nil, ctx.Err()
		}
	}
	return &providers.LLMResponse{Content: "ok", ToolCalls: []providers.ToolCall{}}, nil
}

func (p *predecessorProvider) GetDefaultModel() string { return "mock-model" }

func TestMessagePredecessorAndIdentitySafeCleanup(t *testing.T) {
	provider := &predecessorProvider{
		firstStarted: make(chan struct{}), firstCanceled: make(chan struct{}), releaseFirst: make(chan struct{}),
		secondStarted: make(chan struct{}), releaseSecond: make(chan struct{}),
	}
	s, ts, _ := newResetServer(t, provider)
	resp := sendTestMessage(t, ts, "ordered", "first")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	firstBody := decodeJSONMap(t, resp)
	<-provider.firstStarted

	resp = sendTestMessage(t, ts, "ordered", "second")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	secondBody := decodeJSONMap(t, resp)
	<-provider.firstCanceled
	select {
	case <-provider.secondStarted:
		t.Fatal("successor entered before predecessor completed")
	case <-time.After(50 * time.Millisecond):
	}

	s.mu.RLock()
	current := s.runs["ordered"]
	s.mu.RUnlock()
	require.NotNil(t, current)
	assert.Equal(t, secondBody["stream_id"], current.id)
	assert.NotEqual(t, firstBody["stream_id"], current.id)

	close(provider.releaseFirst)
	select {
	case <-provider.secondStarted:
	case <-time.After(time.Second):
		t.Fatal("successor did not start after predecessor completed")
	}
	s.mu.RLock()
	current = s.runs["ordered"]
	s.mu.RUnlock()
	require.NotNil(t, current, "old cleanup removed the newer run")
	assert.Equal(t, secondBody["stream_id"], current.id)
	close(provider.releaseSecond)
	waitForRun(t, s, "ordered")
}

func TestFinishRunDoesNotRemoveSameIDSuccessorStream(t *testing.T) {
	for _, tc := range []struct {
		name          string
		oldChatID     string
		successorChat string
	}{
		{name: "same chat", oldChatID: "chat-a", successorChat: "chat-a"},
		{name: "cross chat", oldChatID: "chat-a", successorChat: "chat-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const reusedID = "client-reused-id"
			oldStream := &Stream{ID: reusedID, ChatID: tc.oldChatID, Events: make(chan Event, 1)}
			oldRun := &chatRun{
				id: reusedID, chatID: tc.oldChatID, stream: oldStream,
				cancel: func() {}, done: make(chan struct{}),
			}
			successorStream := &Stream{ID: reusedID, ChatID: tc.successorChat, Events: make(chan Event, 1)}
			successorRun := &chatRun{
				id: reusedID, chatID: tc.successorChat, stream: successorStream,
				cancel: func() {}, done: make(chan struct{}),
			}
			s := &Server{
				streams:     map[string]*Stream{reusedID: successorStream},
				chatStreams: map[string]string{tc.successorChat: reusedID},
				runs:        map[string]*chatRun{tc.successorChat: successorRun},
			}

			s.finishRun(oldRun)

			assert.Same(t, successorStream, s.streams[reusedID], "stale predecessor cleanup erased successor stream")
			assert.Equal(t, reusedID, s.chatStreams[tc.successorChat])
			assert.Same(t, successorRun, s.runs[tc.successorChat])
			select {
			case _, ok := <-successorStream.Events:
				if !ok {
					t.Fatal("stale predecessor cleanup closed successor stream")
				}
			default:
			}
		})
	}
}

func TestSessionResetErrorMapping(t *testing.T) {
	t.Run("nil loop", func(t *testing.T) {
		_, ts := newTestServer(t, false)
		resp := sendReset(t, ts, "1", agent.ResetModeSoft)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, "agent_loop_unavailable", decodeJSONMap(t, resp)["code"])
	})

	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "unsupported", err: &agent.SessionResetUnsupportedError{Manager: "test"}, status: http.StatusNotImplemented, code: "session_reset_unsupported"},
		{name: "persistence", err: errors.New("disk failed"), status: http.StatusInternalServerError, code: "session_reset_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ts, _ := newResetServer(t, &testLLMProvider{})
			s.resetSession = func(context.Context, bus.InboundMessage, agent.ResetMode) (*agent.ResetResult, error) {
				return nil, tc.err
			}
			resp := sendReset(t, ts, "1", agent.ResetModeSoft)
			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Equal(t, tc.code, decodeJSONMap(t, resp)["code"])
		})
	}
}

func TestSessionResetTimeoutDoesNotReportSuccess(t *testing.T) {
	provider := &delayedCancellationProvider{
		started: make(chan struct{}), sawCancel: make(chan struct{}), release: make(chan struct{}),
	}
	s, ts, _ := newResetServer(t, provider)
	s.resetTimeout = 50 * time.Millisecond
	resp := sendTestMessage(t, ts, "timeout", "block-a")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
	<-provider.started

	resp = sendReset(t, ts, "timeout", agent.ResetModeSoft)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "session_busy", decodeJSONMap(t, resp)["code"])
	close(provider.release)
	waitForRun(t, s, "timeout")
}

func TestDecodeStrictJSONRejectsMultipleValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"chat_id":"1","mode":"soft"}{}`))
	var input sessionResetInput
	assert.Error(t, decodeStrictJSON(req, &input))
}

func TestSessionResetResponseIsJSON(t *testing.T) {
	s, ts, _ := newResetServer(t, &testLLMProvider{})
	s.resetSession = func(_ context.Context, msg bus.InboundMessage, mode agent.ResetMode) (*agent.ResetResult, error) {
		return &agent.ResetResult{SessionKey: msg.SessionKey, ClearedMessages: 4, SummaryAction: "preserved"}, nil
	}
	resp := sendReset(t, ts, "json", agent.ResetModeSoft)
	defer resp.Body.Close()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Equal(t, float64(4), body["cleared_messages"])
}
