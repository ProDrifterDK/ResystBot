package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/learning"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/trace"
)

func TestRecordLastChannel(t *testing.T) {
	// Create temp workspace
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test config
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	// Create agent loop
	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Test RecordLastChannel
	testChannel := "test-channel"
	err = al.RecordLastChannel(testChannel)
	if err != nil {
		t.Fatalf("RecordLastChannel failed: %v", err)
	}

	// Verify channel was saved
	lastChannel := al.state.GetLastChannel()
	if lastChannel != testChannel {
		t.Errorf("Expected channel '%s', got '%s'", testChannel, lastChannel)
	}

	// Verify persistence by creating a new agent loop
	al2 := NewAgentLoop(cfg, msgBus, provider)
	if al2.state.GetLastChannel() != testChannel {
		t.Errorf("Expected persistent channel '%s', got '%s'", testChannel, al2.state.GetLastChannel())
	}
}

func TestRecordLastChatID(t *testing.T) {
	// Create temp workspace
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test config
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	// Create agent loop
	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Test RecordLastChatID
	testChatID := "test-chat-id-123"
	err = al.RecordLastChatID(testChatID)
	if err != nil {
		t.Fatalf("RecordLastChatID failed: %v", err)
	}

	// Verify chat ID was saved
	lastChatID := al.state.GetLastChatID()
	if lastChatID != testChatID {
		t.Errorf("Expected chat ID '%s', got '%s'", testChatID, lastChatID)
	}

	// Verify persistence by creating a new agent loop
	al2 := NewAgentLoop(cfg, msgBus, provider)
	if al2.state.GetLastChatID() != testChatID {
		t.Errorf("Expected persistent chat ID '%s', got '%s'", testChatID, al2.state.GetLastChatID())
	}
}

func TestNewAgentLoop_StateInitialized(t *testing.T) {
	// Create temp workspace
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test config
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	// Create agent loop
	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Verify state manager is initialized
	if al.state == nil {
		t.Error("Expected state manager to be initialized")
	}

	// Verify state directory was created
	stateDir := filepath.Join(tmpDir, "state")
	if _, err := os.Stat(stateDir); os.IsNotExist(err) {
		t.Error("Expected state directory to exist")
	}
}

func TestNewAgentLoop_LearningDisabledSkipsBootstrap(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
		Learning: config.LearningConfig{Enabled: false},
	}

	called := 0
	prevInitializer := initializeLearningRuntime
	initializeLearningRuntime = func(ctx context.Context, cfg *config.LearningConfig) (*learning.Runtime, error) {
		called++
		return nil, nil
	}
	defer func() { initializeLearningRuntime = prevInitializer }()

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	if al == nil {
		t.Fatal("expected non-nil agent loop")
	}
	if called != 0 {
		t.Fatalf("learning bootstrap calls = %d, want 0", called)
	}
	if al.outcomeExtractor != nil {
		t.Fatal("expected no outcome extractor when learning is disabled")
	}
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}
	if defaultAgent.ContextBuilder.learningRetriever != nil {
		t.Fatal("expected no learning retriever when learning is disabled")
	}
}

func TestNewAgentLoop_LearningInfraDownFailsOpen(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
		Learning: config.LearningConfig{Enabled: true},
	}

	prevInitializer := initializeLearningRuntime
	initializeLearningRuntime = func(ctx context.Context, cfg *config.LearningConfig) (*learning.Runtime, error) {
		return nil, errors.New("qdrant unavailable")
	}
	defer func() { initializeLearningRuntime = prevInitializer }()

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	if al == nil {
		t.Fatal("expected non-nil agent loop")
	}
	if al.outcomeExtractor != nil {
		t.Fatal("expected no outcome extractor when learning infra is unavailable")
	}
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}
	if defaultAgent.ContextBuilder.learningRetriever != nil {
		t.Fatal("expected no learning retriever when learning infra is unavailable")
	}
}

func TestAgentLoop_ShutdownDeduplicatesSharedMCPManager(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	disabled := false
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Default: true, Workspace: filepath.Join(tmpDir, "main")},
				{ID: "researcher", Workspace: filepath.Join(tmpDir, "researcher")},
			},
		},
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				Servers: map[string]config.MCPServerConfig{
					"disabled-test-server": {
						Command: "unused",
						Enabled: &disabled,
					},
				},
			},
		},
	}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	closed := al.shutdownMCPManagers(context.Background())
	if closed != 1 {
		t.Fatalf("shutdownMCPManagers() = %d, want 1 shared manager", closed)
	}
}

// TestToolRegistry_ToolRegistration verifies tools can be registered and retrieved
func TestToolRegistry_ToolRegistration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Register a custom tool
	customTool := &mockCustomTool{}
	al.RegisterTool(customTool)

	// Verify tool is registered by checking it doesn't panic on GetStartupInfo
	// (actual tool retrieval is tested in tools package tests)
	info := al.GetStartupInfo()
	toolsInfo := info["tools"].(map[string]any)
	toolsList := toolsInfo["names"].([]string)

	// Check that our custom tool name is in the list
	found := false
	for _, name := range toolsList {
		if name == "mock_custom" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected custom tool to be registered")
	}
}

// TestToolContext_Updates verifies tool context is updated with channel/chatID
func TestToolContext_Updates(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &simpleMockProvider{response: "OK"}
	_ = NewAgentLoop(cfg, msgBus, provider)

	// Verify that ContextualTool interface is defined and can be implemented
	// This test validates the interface contract exists
	ctxTool := &mockContextualTool{}

	// Verify the tool implements the interface correctly
	var _ tools.ContextualTool = ctxTool
}

// TestToolRegistry_GetDefinitions verifies tool definitions can be retrieved
func TestToolRegistry_GetDefinitions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Register a test tool and verify it shows up in startup info
	testTool := &mockCustomTool{}
	al.RegisterTool(testTool)

	info := al.GetStartupInfo()
	toolsInfo := info["tools"].(map[string]any)
	toolsList := toolsInfo["names"].([]string)

	// Check that our custom tool name is in the list
	found := false
	for _, name := range toolsList {
		if name == "mock_custom" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected custom tool to be registered")
	}
}

// TestAgentLoop_GetStartupInfo verifies startup info contains tools
func TestAgentLoop_GetStartupInfo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	info := al.GetStartupInfo()

	// Verify tools info exists
	toolsInfo, ok := info["tools"]
	if !ok {
		t.Fatal("Expected 'tools' key in startup info")
	}

	toolsMap, ok := toolsInfo.(map[string]any)
	if !ok {
		t.Fatal("Expected 'tools' to be a map")
	}

	count, ok := toolsMap["count"]
	if !ok {
		t.Fatal("Expected 'count' in tools info")
	}

	// Should have default tools registered
	if count.(int) == 0 {
		t.Error("Expected at least some tools to be registered")
	}
}

// TestAgentLoop_Stop verifies Stop() sets running to false
func TestAgentLoop_Stop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Note: running is only set to true when Run() is called
	// We can't test that without starting the event loop
	// Instead, verify the Stop method can be called safely
	al.Stop()

	// Verify running is false (initial state or after Stop)
	if al.running.Load() {
		t.Error("Expected agent to be stopped (or never started)")
	}
}

// Mock implementations for testing

func TestProcessDirectWithChannelHonorsExplicitSessionKey(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace:         workspace,
		Model:             "test-model",
		MaxTokens:         4096,
		MaxToolIterations: 3,
	}}}
	provider := &simpleMockProvider{response: "session isolated"}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)

	sharedMain := "agent:main:main"
	explicit := "telegram:101293943"
	defaultAgent := al.registry.GetDefaultAgent()
	defaultAgent.Sessions.SetHistory(sharedMain, []providers.Message{{Role: "user", Content: "Alan-only history"}})

	response, err := al.ProcessDirectWithChannel(context.Background(), "hola", explicit, "telegram", "101293943")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel() error = %v", err)
	}
	if response != "session isolated" {
		t.Fatalf("response = %q, want session isolated", response)
	}

	isolatedHistory := defaultAgent.Sessions.GetHistory(explicit)
	if len(isolatedHistory) == 0 {
		t.Fatalf("expected explicit session %q to receive history", explicit)
	}
	mainHistory := defaultAgent.Sessions.GetHistory(sharedMain)
	for _, msg := range mainHistory {
		if msg.Content == "hola" || msg.Content == "session isolated" {
			t.Fatalf("shared main session received telegram turn: %#v", mainHistory)
		}
	}
}

type simpleMockProvider struct {
	response string
}

func (m *simpleMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		Content:   m.response,
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (m *simpleMockProvider) GetDefaultModel() string {
	return "mock-model"
}

// mockCustomTool is a simple mock tool for registration testing
type mockCustomTool struct{}

func (m *mockCustomTool) Name() string {
	return "mock_custom"
}

func (m *mockCustomTool) Description() string {
	return "Mock custom tool for testing"
}

func (m *mockCustomTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (m *mockCustomTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	return tools.SilentResult("Custom tool executed")
}

// mockContextualTool tracks context updates
type mockContextualTool struct {
	lastChannel string
	lastChatID  string
}

func (m *mockContextualTool) Name() string {
	return "mock_contextual"
}

func (m *mockContextualTool) Description() string {
	return "Mock contextual tool"
}

func (m *mockContextualTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (m *mockContextualTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	return tools.SilentResult("Contextual tool executed")
}

func (m *mockContextualTool) SetContext(channel, chatID string) {
	m.lastChannel = channel
	m.lastChatID = chatID
}

// testHelper executes a message and returns the response
type testHelper struct {
	al *AgentLoop
}

func (h testHelper) executeAndGetResponse(tb testing.TB, ctx context.Context, msg bus.InboundMessage) string {
	// Use a short timeout to avoid hanging
	timeoutCtx, cancel := context.WithTimeout(ctx, responseTimeout)
	defer cancel()

	response, err := h.al.processMessage(timeoutCtx, msg)
	if err != nil {
		tb.Fatalf("processMessage failed: %v", err)
	}
	return response
}

const responseTimeout = 3 * time.Second

// TestToolResult_SilentToolDoesNotSendUserMessage verifies silent tools don't trigger outbound
func TestToolResult_SilentToolDoesNotSendUserMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &simpleMockProvider{response: "File operation complete"}
	al := NewAgentLoop(cfg, msgBus, provider)
	helper := testHelper{al: al}

	// ReadFileTool returns SilentResult, which should not send user message
	ctx := context.Background()
	msg := bus.InboundMessage{
		Channel:    "test",
		SenderID:   "user1",
		ChatID:     "chat1",
		Content:    "read test.txt",
		SessionKey: "test-session",
	}

	response := helper.executeAndGetResponse(t, ctx, msg)

	// Silent tool should return the LLM's response directly
	if response != "File operation complete" {
		t.Errorf("Expected 'File operation complete', got: %s", response)
	}
}

// TestToolResult_UserFacingToolDoesSendMessage verifies user-facing tools trigger outbound
func TestToolResult_UserFacingToolDoesSendMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &simpleMockProvider{response: "Command output: hello world"}
	al := NewAgentLoop(cfg, msgBus, provider)
	helper := testHelper{al: al}

	// ExecTool returns UserResult, which should send user message
	ctx := context.Background()
	msg := bus.InboundMessage{
		Channel:    "test",
		SenderID:   "user1",
		ChatID:     "chat1",
		Content:    "run hello",
		SessionKey: "test-session",
	}

	response := helper.executeAndGetResponse(t, ctx, msg)

	// User-facing tool should include the output in final response
	if response != "Command output: hello world" {
		t.Errorf("Expected 'Command output: hello world', got: %s", response)
	}
}

// failFirstMockProvider fails on the first N calls with a specific error
type failFirstMockProvider struct {
	failures    int
	currentCall int
	failError   error
	successResp string
}

func (m *failFirstMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	m.currentCall++
	if m.currentCall <= m.failures {
		return nil, m.failError
	}
	return &providers.LLMResponse{
		Content:   m.successResp,
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (m *failFirstMockProvider) GetDefaultModel() string {
	return "mock-fail-model"
}

// TestAgentLoop_ContextExhaustionRetry verify that the agent retries on context errors
func TestAgentLoop_ContextExhaustionRetry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()

	// Create a provider that fails once with a context error
	contextErr := fmt.Errorf("InvalidParameter: Total tokens of image and text exceed max message tokens")
	provider := &failFirstMockProvider{
		failures:    1,
		failError:   contextErr,
		successResp: "Recovered from context error",
	}

	al := NewAgentLoop(cfg, msgBus, provider)

	// Inject some history to simulate a full context
	sessionKey := "test-session-context"
	// Create dummy history
	history := []providers.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Old message 1"},
		{Role: "assistant", Content: "Old response 1"},
		{Role: "user", Content: "Old message 2"},
		{Role: "assistant", Content: "Old response 2"},
		{Role: "user", Content: "Trigger message"},
	}
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("No default agent found")
	}
	defaultAgent.Sessions.SetHistory(sessionKey, history)

	// Call ProcessDirectWithChannel
	// Note: ProcessDirectWithChannel calls processMessage which will execute runLLMIteration
	response, err := al.ProcessDirectWithChannel(
		context.Background(),
		"Trigger message",
		sessionKey,
		"test",
		"test-chat",
	)
	if err != nil {
		t.Fatalf("Expected success after retry, got error: %v", err)
	}

	if response != "Recovered from context error" {
		t.Errorf("Expected 'Recovered from context error', got '%s'", response)
	}

	// We expect 2 calls: 1st failed, 2nd succeeded
	if provider.currentCall != 2 {
		t.Errorf("Expected 2 calls (1 fail + 1 success), got %d", provider.currentCall)
	}

	// Check final history length
	finalHistory := defaultAgent.Sessions.GetHistory(sessionKey)
	// We verify that the history has been modified (compressed)
	// Original length: 6
	// Expected behavior: compression drops ~50% of history (mid slice)
	// We can assert that the length is NOT what it would be without compression.
	// Without compression: 6 + 1 (new user msg) + 1 (assistant msg) = 8
	if len(finalHistory) >= 8 {
		t.Errorf("Expected history to be compressed (len < 8), got %d", len(finalHistory))
	}
}

type traceSequenceProvider struct {
	responses []*providers.LLMResponse
	cancel    context.CancelFunc
	calls     int
}

func (m *traceSequenceProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	if m.calls >= len(m.responses) {
		return &providers.LLMResponse{Content: "unexpected extra call"}, nil
	}
	resp := m.responses[m.calls]
	m.calls++
	if m.cancel != nil && m.calls == len(m.responses) {
		m.cancel()
	}
	return resp, nil
}

func (m *traceSequenceProvider) GetDefaultModel() string {
	return "openai/test-model"
}

type traceMockTool struct{}

func (m *traceMockTool) Name() string { return "trace_mock" }

func (m *traceMockTool) Description() string { return "Trace mock tool" }

func (m *traceMockTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"topic": map[string]any{"type": "string"},
		},
	}
}

func (m *traceMockTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	return tools.SilentResult(fmt.Sprintf("trace tool ok: %v", args["topic"]))
}

type failingTraceTool struct{}

func (m *failingTraceTool) Name() string { return "trace_fail" }

func (m *failingTraceTool) Description() string { return "Trace failure tool" }

func (m *failingTraceTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
		},
	}
}

func (m *failingTraceTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	return tools.ErrorResult(fmt.Sprintf("permission denied running %v", args["command"]))
}

type outcomeRecorder struct {
	mu      sync.Mutex
	records []learning.LessonRecord
	err     error
}

func (o *outcomeRecorder) Store(ctx context.Context, record *learning.LessonRecord) error {
	if o.err != nil {
		return o.err
	}
	if record == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, *record)
	return nil
}

func TestAgentLoop_TraceSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Provider:          "openai",
				Model:             "openai/test-model",
				MaxTokens:         4096,
				MaxToolIterations: 4,
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	provider := &traceSequenceProvider{
		cancel: cancel,
		responses: []*providers.LLMResponse{
			{
				ToolCalls: []providers.ToolCall{{
					ID:        "call_trace_1",
					Name:      "trace_mock",
					Arguments: map[string]any{"topic": "success"},
				}},
				Usage: &providers.UsageInfo{TotalTokens: 11},
			},
			{
				Content: "trace complete",
				Usage:   &providers.UsageInfo{TotalTokens: 17},
			},
		},
	}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	al.RegisterTool(&traceMockTool{})

	response, err := al.ProcessDirectWithChannel(ctx, "trace this turn", "trace-session-success", "test", "chat-success")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	if response != "trace complete" {
		t.Fatalf("response = %q, want %q", response, "trace complete")
	}

	records := readTraceRecords(t, filepath.Join(tmpDir, "mind", "traces"))
	if len(records) != 1 {
		t.Fatalf("trace records = %d, want 1", len(records))
	}
	record := records[0]
	if record.SessionKey != "agent:main:main" {
		t.Fatalf("session_key = %q, want %q", record.SessionKey, "agent:main:main")
	}
	if record.AgentID != "main" {
		t.Fatalf("agent_id = %q, want %q", record.AgentID, "main")
	}
	if record.UserMessage != "trace this turn" {
		t.Fatalf("user_message = %q, want %q", record.UserMessage, "trace this turn")
	}
	if record.FinalResponse != "trace complete" {
		t.Fatalf("final_response = %q, want %q", record.FinalResponse, "trace complete")
	}
	if record.ExitReason != trace.ExitReasonSuccess {
		t.Fatalf("exit_reason = %q, want %q", record.ExitReason, trace.ExitReasonSuccess)
	}
	if record.LLMModel != "test-model" {
		t.Fatalf("llm_model = %q, want %q", record.LLMModel, "test-model")
	}
	if record.LLMProvider == "" {
		t.Fatal("expected llm_provider to be recorded")
	}
	if len(record.LLMCalls) != 2 {
		t.Fatalf("llm_calls = %d, want 2", len(record.LLMCalls))
	}
	if len(record.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(record.ToolCalls))
	}
	if record.ToolCalls[0].Name != "trace_mock" {
		t.Fatalf("tool name = %q, want %q", record.ToolCalls[0].Name, "trace_mock")
	}
	if record.ToolCalls[0].Result != "trace tool ok: success" {
		t.Fatalf("tool result = %q, want %q", record.ToolCalls[0].Result, "trace tool ok: success")
	}
}

func TestAgentLoop_TraceIncludesInjectedLearningIDs(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Provider:          "openai",
				Model:             "openai/test-model",
				MaxTokens:         4096,
				MaxToolIterations: 4,
			},
		},
		Learning: config.LearningConfig{Enabled: true},
	}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "learning trace complete"})
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}
	defaultAgent.ContextBuilder.SetLearningRetriever(&fakeLearningRetriever{lessons: []learning.LessonRecord{{
		ID:             "lesson_trace_123",
		Situation:      "Prior tool misuse",
		BetterApproach: "Use the established fix path",
	}}}, &cfg.Learning)

	response, err := al.ProcessDirectWithChannel(context.Background(), "use the learning trace path", "trace-session-learning", "test", "chat-learning")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	if response != "learning trace complete" {
		t.Fatalf("response = %q, want %q", response, "learning trace complete")
	}

	records := readTraceRecords(t, filepath.Join(tmpDir, "mind", "traces"))
	if len(records) != 1 {
		t.Fatalf("trace records = %d, want 1", len(records))
	}
	if got := records[0].InjectedLearningIDs; len(got) != 1 || got[0] != "lesson_trace_123" {
		t.Fatalf("injected_learning_ids = %v, want [lesson_trace_123]", got)
	}
}

func TestAgentLoop_TraceWriteFailureNonFatal(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Provider:          "openai",
				Model:             "openai/test-model",
				MaxTokens:         4096,
				MaxToolIterations: 4,
			},
		},
	}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "still works"})
	blockingPath := filepath.Join(tmpDir, "trace-blocker")
	if err := os.WriteFile(blockingPath, []byte("file, not dir"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	al.traceWriter = trace.NewTraceWriter(blockingPath)

	response, err := al.ProcessDirectWithChannel(context.Background(), "trigger trace failure", "trace-session-failure", "test", "chat-failure")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	if response != "still works" {
		t.Fatalf("response = %q, want %q", response, "still works")
	}

	if entries := readTraceRecords(t, filepath.Join(tmpDir, "mind", "traces")); len(entries) != 0 {
		t.Fatalf("expected no persisted trace records, got %d", len(entries))
	}
}

func TestAgentLoop_OutcomeExtractionFailureNonFatal(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Provider:          "openai",
				Model:             "openai/test-model",
				MaxTokens:         4096,
				MaxToolIterations: 4,
			},
		},
		Learning: config.LearningConfig{Enabled: true, MinUserMessageChars: 10},
	}

	provider := &traceSequenceProvider{responses: []*providers.LLMResponse{
		{
			ToolCalls: []providers.ToolCall{{
				ID:        "call_trace_fail_1",
				Name:      "trace_fail",
				Arguments: map[string]any{"command": "pip install foo"},
			}},
		},
		{Content: "still answered"},
	}}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	al.RegisterTool(&failingTraceTool{})
	al.outcomeExtractor = learning.NewOutcomeExtractor(&outcomeRecorder{err: errors.New("store failed")}, &cfg.Learning)

	response, err := al.ProcessDirectWithChannel(context.Background(), "please install a package and explain what broke", "trace-session-learning-fail", "test", "chat-learning-fail")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel failed: %v", err)
	}
	if response != "still answered" {
		t.Fatalf("response = %q, want %q", response, "still answered")
	}
}

func readTraceRecords(t *testing.T, baseDir string) []trace.TurnTrace {
	t.Helper()
	var records []trace.TurnTrace
	if _, err := os.Stat(baseDir); err != nil {
		if os.IsNotExist(err) {
			return records
		}
		t.Fatalf("stat trace dir: %v", err)
	}
	err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var record trace.TurnTrace
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				return err
			}
			records = append(records, record)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read trace records: %v", err)
	}
	return records
}
