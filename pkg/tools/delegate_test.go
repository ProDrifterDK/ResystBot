package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// recordingProvider is a minimal LLMProvider that records the model it was
// asked to run with, so tests can assert which agent profile was used.
type recordingProvider struct {
	content string
	err     error
	calls   int
	models  []string
}

func (p *recordingProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	p.models = append(p.models, model)
	if p.err != nil {
		return nil, p.err
	}
	content := p.content
	if content == "" {
		content = "default response"
	}
	// Echo the last user message so tests can assert the task was forwarded.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			content = content + " :: " + messages[i].Content
			break
		}
	}
	return &providers.LLMResponse{Content: content}, nil
}

func (p *recordingProvider) GetDefaultModel() string { return "recording-model" }
func (p *recordingProvider) SupportsTools() bool     { return false }
func (p *recordingProvider) GetContextWindow() int   { return 4096 }

func newDelegateTestManager(t *testing.T) (*SubagentManager, *recordingProvider) {
	t.Helper()
	provider := &recordingProvider{content: "done"}
	manager := NewSubagentManager(provider, "main-model", t.TempDir(), nil)
	return manager, provider
}

func TestDelegateTool_Name(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	tool := NewDelegateTool(manager)
	if got := tool.Name(); got != "delegate" {
		t.Fatalf("Name() = %q, want %q", got, "delegate")
	}
	if d := tool.Description(); !strings.Contains(d, "another agent") {
		t.Errorf("Description should mention another agent, got: %s", d)
	}
}

func TestDelegateTool_Parameters(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	tool := NewDelegateTool(manager)

	params := tool.Parameters()
	if params["type"] != "object" {
		t.Fatalf("type = %v, want object", params["type"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties should be a map")
	}
	for _, key := range []string{"agent_id", "task"} {
		p, ok := props[key].(map[string]any)
		if !ok {
			t.Fatalf("%s parameter missing", key)
		}
		if p["type"] != "string" {
			t.Errorf("%s type = %v, want string", key, p["type"])
		}
	}
	required, ok := params["required"].([]string)
	if !ok || len(required) != 2 {
		t.Fatalf("required = %v, want [agent_id task]", params["required"])
	}
}

func TestDelegateTool_IsSequential(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	if !IsSequential(NewDelegateTool(manager)) {
		t.Error("delegate must be sequential: it blocks and stores origin context on the receiver")
	}
}

// TestDelegateTool_ImplementsContextualTool guards the wiring in
// AgentLoop.updateToolContexts: delegate must accept SetContext so the delegated
// run is attributed to the originating channel/chat.
func TestDelegateTool_ImplementsContextualTool(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	var _ ContextualTool = NewDelegateTool(manager)
}

func TestDelegateTool_Execute_Success(t *testing.T) {
	manager, provider := newDelegateTestManager(t)
	manager.RegisterAgentProfile("specialist", provider, "specialist-model", nil, nil, nil, 0)

	tool := NewDelegateTool(manager)
	tool.SetContext("telegram", "chat-1")

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "specialist",
		"task":     "summarize the logs",
	})

	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if result.Async {
		t.Error("delegate must be synchronous")
	}
	if !strings.Contains(result.ForLLM, `[Response from agent "specialist"]`) {
		t.Errorf("ForLLM should be attributed to the target agent, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "summarize the logs") {
		t.Errorf("ForLLM should carry the task result, got: %s", result.ForLLM)
	}
	if !strings.HasPrefix(result.ForUser, "[specialist] ") {
		t.Errorf("ForUser should be attributed to the target agent, got: %s", result.ForUser)
	}
	if provider.calls == 0 {
		t.Fatal("target agent provider was never called")
	}
}

// TestDelegateTool_Execute_UsesTargetAgentProfile is the core promise of the
// tool: the task must run with the *target* agent's model, not the caller's.
func TestDelegateTool_Execute_UsesTargetAgentProfile(t *testing.T) {
	manager, callerProvider := newDelegateTestManager(t)
	targetProvider := &recordingProvider{content: "target answer"}
	manager.RegisterAgentProfile("specialist", targetProvider, "specialist-model", nil, nil, nil, 0)

	tool := NewDelegateTool(manager)
	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "specialist",
		"task":     "do the thing",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if callerProvider.calls != 0 {
		t.Errorf("caller provider should not be used when a target profile exists, got %d calls", callerProvider.calls)
	}
	if len(targetProvider.models) != 1 || targetProvider.models[0] != "specialist-model" {
		t.Errorf("target models = %v, want [specialist-model]", targetProvider.models)
	}
}

func TestDelegateTool_Execute_UnknownAgentFallsBackToDefaults(t *testing.T) {
	manager, provider := newDelegateTestManager(t)
	tool := NewDelegateTool(manager)

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "ghost",
		"task":     "anything",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if len(provider.models) != 1 || provider.models[0] != "main-model" {
		t.Errorf("models = %v, want [main-model]", provider.models)
	}
}

func TestDelegateTool_Execute_SelfDelegationRejected(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	tool := NewDelegateTool(manager)
	tool.SetSelfAgentID("main")

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "main",
		"task":     "loop",
	})
	if !result.IsError {
		t.Fatal("self-delegation must be rejected")
	}
	if !strings.Contains(result.ForLLM, "cannot delegate to self") {
		t.Errorf("got: %s", result.ForLLM)
	}
}

func TestDelegateTool_Execute_AllowlistDenied(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	tool := NewDelegateTool(manager)
	tool.SetAllowlistChecker(func(string) bool { return false })

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "specialist",
		"task":     "anything",
	})
	if !result.IsError {
		t.Fatal("denied delegation must fail")
	}
	if !strings.Contains(result.ForLLM, "not allowed to delegate") {
		t.Errorf("got: %s", result.ForLLM)
	}
}

func TestDelegateTool_Execute_AllowlistAllows(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	tool := NewDelegateTool(manager)
	tool.SetAllowlistChecker(func(id string) bool { return id == "specialist" })

	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "specialist",
		"task":     "anything",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
}

func TestDelegateTool_Execute_Validation(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	tool := NewDelegateTool(manager)

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing agent_id", map[string]any{"task": "x"}, "agent_id is required"},
		{"blank agent_id", map[string]any{"agent_id": "   ", "task": "x"}, "agent_id is required"},
		{"missing task", map[string]any{"agent_id": "specialist"}, "task is required"},
		{"blank task", map[string]any{"agent_id": "specialist", "task": "\t"}, "task is required"},
		{"wrong types", map[string]any{"agent_id": 1, "task": 2}, "agent_id is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := tool.Execute(context.Background(), tc.args)
			if !result.IsError {
				t.Fatal("expected error")
			}
			if !strings.Contains(result.ForLLM, tc.want) {
				t.Errorf("got %q, want substring %q", result.ForLLM, tc.want)
			}
		})
	}
}

func TestDelegateTool_Execute_NilManager(t *testing.T) {
	tool := NewDelegateTool(nil)
	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "specialist",
		"task":     "x",
	})
	if !result.IsError {
		t.Fatal("expected error for nil manager")
	}
	if !strings.Contains(result.ForLLM, "not configured") {
		t.Errorf("got: %s", result.ForLLM)
	}
}

func TestDelegateTool_Execute_ErrorPreserved(t *testing.T) {
	manager, provider := newDelegateTestManager(t)
	sentinel := errors.New("boom")
	provider.err = sentinel

	tool := NewDelegateTool(manager)
	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "specialist",
		"task":     "x",
	})
	if !result.IsError {
		t.Fatal("expected error result")
	}
	if result.Err == nil {
		t.Fatal("underlying error must be preserved via WithError")
	}
	if !errors.Is(result.Err, sentinel) {
		t.Errorf("Err = %v, want wrapped %v", result.Err, sentinel)
	}
}

func TestDelegateTool_Execute_TrimmedAgentIDMatchesProfile(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	targetProvider := &recordingProvider{content: "ok"}
	manager.RegisterAgentProfile("specialist", targetProvider, "specialist-model", nil, nil, nil, 0)

	tool := NewDelegateTool(manager)
	result := tool.Execute(context.Background(), map[string]any{
		"agent_id": "  specialist  ",
		"task":     "x",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if len(targetProvider.models) != 1 {
		t.Errorf("trimmed agent id should resolve to the profile, models=%v", targetProvider.models)
	}
	if !strings.Contains(result.ForLLM, `[Response from agent "specialist"]`) {
		t.Errorf("attribution should use the trimmed id, got: %s", result.ForLLM)
	}
}

// reentrantProvider calls back into the same manager's Delegate from inside a
// Chat call, simulating a delegation cycle (A → B → A → …).
type reentrantProvider struct {
	manager *SubagentManager
	err     error
	calls   int
}

func (p *reentrantProvider) Chat(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	_, err := p.manager.Delegate(ctx, "recurse", "main", "", "cli", "direct")
	if err != nil {
		p.err = err
		return &providers.LLMResponse{Content: "stopped: " + err.Error()}, nil
	}
	return &providers.LLMResponse{Content: "nested"}, nil
}

func (p *reentrantProvider) GetDefaultModel() string { return "reentrant" }
func (p *reentrantProvider) SupportsTools() bool     { return false }
func (p *reentrantProvider) GetContextWindow() int   { return 4096 }

func TestSubagentManager_Delegate_CycleGuard(t *testing.T) {
	provider := &reentrantProvider{}
	manager := NewSubagentManager(provider, "main-model", t.TempDir(), nil)
	provider.manager = manager

	result, err := manager.Delegate(context.Background(), "start", "main", "", "cli", "direct")
	if err != nil {
		t.Fatalf("outer delegate should complete, got error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// The cycle must terminate: the guard rejects the (maxDepth+1)-th re-entry,
	// so the provider runs exactly maxDepth times instead of recursing forever.
	if provider.calls != defaultMaxDelegateDepth {
		t.Errorf("Chat calls = %d, want %d (cycle stopped at the depth limit)",
			provider.calls, defaultMaxDelegateDepth)
	}
	if provider.err == nil || !strings.Contains(provider.err.Error(), "delegation cycle detected") {
		t.Fatalf("cycle rejection not surfaced: %v", provider.err)
	}
	if depth := manager.delegateDepth.Load(); depth != 0 {
		t.Errorf("delegateDepth should return to 0, got %d", depth)
	}
}

func TestSubagentManager_Delegate_CycleGuardConfigurable(t *testing.T) {
	provider := &reentrantProvider{}
	manager := NewSubagentManager(provider, "main-model", t.TempDir(), nil)
	provider.manager = manager
	manager.SetMaxDelegateDepth(1)

	if _, err := manager.Delegate(context.Background(), "start", "main", "", "cli", "direct"); err != nil {
		t.Fatalf("outer delegate should complete, got error: %v", err)
	}
	if provider.calls != 1 {
		t.Errorf("Chat calls = %d, want 1 with maxDelegateDepth=1", provider.calls)
	}
}

func TestSubagentManager_Delegate_EmptyTask(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	if _, err := manager.Delegate(context.Background(), "   ", "main", "", "cli", "direct"); err == nil {
		t.Fatal("expected error for empty task")
	}
}

func TestSubagentManager_SetMaxDelegateDepth(t *testing.T) {
	manager, _ := newDelegateTestManager(t)
	if manager.maxDelegateDepth != defaultMaxDelegateDepth {
		t.Fatalf("default depth = %d, want %d", manager.maxDelegateDepth, defaultMaxDelegateDepth)
	}
	manager.SetMaxDelegateDepth(7)
	if manager.maxDelegateDepth != 7 {
		t.Errorf("depth = %d, want 7", manager.maxDelegateDepth)
	}
	manager.SetMaxDelegateDepth(0)
	if manager.maxDelegateDepth != defaultMaxDelegateDepth {
		t.Errorf("depth = %d, want default after non-positive input", manager.maxDelegateDepth)
	}
}
