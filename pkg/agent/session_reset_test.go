package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func newSessionResetLoop(t *testing.T, provider providers.LLMProvider) *AgentLoop {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Provider = "mock"
	cfg.Agents.Defaults.Model = "mock-model"
	cfg.Agents.Defaults.MaxToolIterations = 3
	return NewAgentLoop(cfg, bus.NewMessageBus(), provider)
}

func telegramResetMessage(chatID, content string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:    "telegram",
		SenderID:   "tester",
		ChatID:     chatID,
		Content:    content,
		SessionKey: "telegram:" + chatID,
		Metadata:   map[string]string{},
	}
}

type resetSpyContextManager struct {
	ContextManager
	req   *ResetRequest
	agent *AgentInstance
}

func (m *resetSpyContextManager) Reset(ctx context.Context, req *ResetRequest) (*ResetResult, error) {
	m.req = req
	m.agent, _ = legacyContextAgentFromContext(ctx)
	return &ResetResult{ClearedMessages: 7, SummaryAction: "preserved"}, nil
}

type unsupportedResetContextManager struct {
	ContextManager
}

func TestAgentLoopResetSessionUsesTelegramMessageTarget(t *testing.T) {
	al := newSessionResetLoop(t, &simpleMockProvider{response: "ok"})
	msg := telegramResetMessage("target-1", "hello")
	_, err := al.ProcessMessage(context.Background(), msg)
	require.NoError(t, err)

	spy := &resetSpyContextManager{}
	al.contextMgr = spy
	result, err := al.ResetSession(context.Background(), msg, ResetModeSoft)
	require.NoError(t, err)
	assert.Equal(t, "telegram:target-1", result.SessionKey)
	require.NotNil(t, spy.req)
	assert.Equal(t, "telegram:target-1", spy.req.SessionKey)
	assert.Equal(t, ResetModeSoft, spy.req.Mode)
	assert.Same(t, al.registry.GetDefaultAgent(), spy.agent)
}

func TestAgentLoopResetSessionCancelledContextDoesNotMutate(t *testing.T) {
	al := newSessionResetLoop(t, &simpleMockProvider{response: "ok"})
	agent := al.registry.GetDefaultAgent()
	key := "telegram:cancelled-reset"
	agent.Sessions.AddMessage(key, "user", "keep")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := al.ResetSession(ctx, telegramResetMessage("cancelled-reset", ""), ResetModeHard)
	require.ErrorIs(t, err, context.Canceled)
	assert.Len(t, agent.Sessions.GetHistory(key), 1)
}

func TestAgentLoopResetSessionUnsupportedIsTyped(t *testing.T) {
	al := newSessionResetLoop(t, &simpleMockProvider{response: "ok"})
	al.contextMgr = &unsupportedResetContextManager{}

	_, err := al.ResetSession(context.Background(), telegramResetMessage("unsupported", ""), ResetModeSoft)
	require.Error(t, err)
	var unsupported *SessionResetUnsupportedError
	assert.True(t, errors.As(err, &unsupported))
}

type blockingResetProvider struct {
	started chan string
	release chan struct{}
	once    sync.Once
}

func (p *blockingResetProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	for _, msg := range messages {
		if strings.Contains(msg.Content, "block-a") {
			p.once.Do(func() { p.started <- "a" })
			<-p.release
			break
		}
	}
	return &providers.LLMResponse{Content: "late", ToolCalls: []providers.ToolCall{}}, nil
}

func (p *blockingResetProvider) GetDefaultModel() string { return "mock-model" }

func TestAgentLoopSerializesMessageResetAndKeepsChatsIndependent(t *testing.T) {
	provider := &blockingResetProvider{started: make(chan string, 1), release: make(chan struct{})}
	al := newSessionResetLoop(t, provider)

	messageDone := make(chan error, 1)
	go func() {
		_, err := al.ProcessMessage(context.Background(), telegramResetMessage("a", "block-a"))
		messageDone <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("same-session message did not enter provider")
	}

	resetDone := make(chan error, 1)
	go func() {
		_, err := al.ResetSession(context.Background(), telegramResetMessage("a", ""), ResetModeHard)
		resetDone <- err
	}()
	select {
	case err := <-resetDone:
		t.Fatalf("same-session reset completed before message: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	otherCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := al.ResetSession(otherCtx, telegramResetMessage("b", ""), ResetModeSoft)
	require.NoError(t, err, "different session should not wait")

	close(provider.release)
	require.NoError(t, <-messageDone)
	require.NoError(t, <-resetDone)
	assert.Empty(t, al.registry.GetDefaultAgent().Sessions.GetHistory("telegram:a"))
}

type summarizerBlockingProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *summarizerBlockingProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.once.Do(func() { close(p.started) })
	<-p.release
	return &providers.LLMResponse{Content: "stale summary", ToolCalls: []providers.ToolCall{}}, nil
}

func (p *summarizerBlockingProvider) GetDefaultModel() string { return "mock-model" }

func TestAgentLoopSerializesSummarizerAndReset(t *testing.T) {
	provider := &summarizerBlockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	al := newSessionResetLoop(t, provider)
	agent := al.registry.GetDefaultAgent()
	key := "telegram:summarize"
	for i := 0; i < 6; i++ {
		agent.Sessions.AddMessage(key, []string{"user", "assistant"}[i%2], "history")
	}

	summaryDone := make(chan struct{})
	go func() {
		al.summarizeSession(agent, key)
		close(summaryDone)
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("summarizer did not enter provider")
	}

	resetDone := make(chan error, 1)
	go func() {
		_, err := al.ResetSession(context.Background(), telegramResetMessage("summarize", ""), ResetModeHard)
		resetDone <- err
	}()
	select {
	case err := <-resetDone:
		t.Fatalf("reset completed before summarizer quiesced: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	_, err := al.ResetSession(context.Background(), telegramResetMessage("other", ""), ResetModeSoft)
	require.NoError(t, err)
	close(provider.release)
	<-summaryDone
	require.NoError(t, <-resetDone)
	assert.Empty(t, agent.Sessions.GetHistory(key))
	assert.Empty(t, agent.Sessions.GetSummary(key))
}

func TestLegacyContextManagerClearDelegatesToHardReset(t *testing.T) {
	al := newSessionResetLoop(t, &simpleMockProvider{response: "ok"})
	agent := al.registry.GetDefaultAgent()
	key := "telegram:legacy-clear"
	agent.Sessions.AddMessage(key, "user", "old")
	agent.Sessions.SetSummary(key, "old summary")

	err := al.ContextManager().Clear(withLegacyContextAgent(context.Background(), agent), key)
	require.NoError(t, err)
	assert.Empty(t, agent.Sessions.GetHistory(key))
	assert.Empty(t, agent.Sessions.GetSummary(key))
}
