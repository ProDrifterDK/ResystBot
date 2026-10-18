package agent

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// toolCallingMockProvider returns a batch of tool calls on its first Chat call
// and a plain text response afterwards. It captures the messages it receives on
// the second call so tests can assert the order of tool-result messages.
type toolCallingMockProvider struct {
	toolNames []string

	mu                 sync.Mutex
	calls              int
	secondCallMessages []providers.Message
}

func (m *toolCallingMockProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	if n == 1 {
		m.mu.Unlock()
		calls := make([]providers.ToolCall, 0, len(m.toolNames))
		for i, name := range m.toolNames {
			calls = append(calls, providers.ToolCall{
				ID:        fmt.Sprintf("call-%d", i),
				Name:      name,
				Arguments: map[string]any{},
			})
		}
		return &providers.LLMResponse{ToolCalls: calls}, nil
	}
	m.secondCallMessages = messages
	m.mu.Unlock()
	return &providers.LLMResponse{Content: "done"}, nil
}

func (m *toolCallingMockProvider) GetDefaultModel() string { return "mock-model" }

// probeSet records the maximum number of probe tools that ran simultaneously.
type probeSet struct {
	mu        sync.Mutex
	active    int
	maxActive int
}

func (s *probeSet) enter() {
	s.mu.Lock()
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
}

func (s *probeSet) leave() {
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
}

func (s *probeSet) peak() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxActive
}

// concurrencyProbe is a concurrency-safe tool that blocks briefly and records
// how many probes overlapped.
type concurrencyProbe struct {
	set *probeSet
	id  string
}

func (p *concurrencyProbe) Name() string        { return p.id }
func (p *concurrencyProbe) Description() string { return "concurrency probe" }
func (p *concurrencyProbe) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (p *concurrencyProbe) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	p.set.enter()
	defer p.set.leave()

	select {
	case <-time.After(120 * time.Millisecond):
	case <-ctx.Done():
	}
	return tools.SilentResult(p.id + "-done")
}

// sequentialProbe is a concurrencyProbe that opts out of parallel execution.
type sequentialProbe struct{ *concurrencyProbe }

func (p *sequentialProbe) IsSequential() bool { return true }

func newProbeAgent(t *testing.T, provider *toolCallingMockProvider, probeTools ...tools.Tool) *AgentLoop {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "agent-parallel-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

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
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	for _, tl := range probeTools {
		al.RegisterTool(tl)
	}
	return al
}

func toolResultContents(t *testing.T, provider *toolCallingMockProvider) []string {
	t.Helper()
	provider.mu.Lock()
	msgs := provider.secondCallMessages
	provider.mu.Unlock()

	var got []string
	for _, m := range msgs {
		if m.Role == "tool" {
			got = append(got, m.Content)
		}
	}
	return got
}

// TestToolCallsRunInParallelAndPreserveOrder verifies that when every requested
// tool is concurrency-safe they overlap in time, yet their results are appended
// to the conversation in the original request order.
func TestToolCallsRunInParallelAndPreserveOrder(t *testing.T) {
	set := &probeSet{}
	names := []string{"probe_a", "probe_b", "probe_c"}
	provider := &toolCallingMockProvider{toolNames: names}

	probeTools := make([]tools.Tool, 0, len(names))
	for _, n := range names {
		probeTools = append(probeTools, &concurrencyProbe{set: set, id: n})
	}

	al := newProbeAgent(t, provider, probeTools...)
	helper := testHelper{al: al}

	resp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:    "test",
		SenderID:   "user1",
		ChatID:     "chat1",
		Content:    "run probes",
		SessionKey: "probe-session",
	})
	if resp != "done" {
		t.Fatalf("response = %q, want %q", resp, "done")
	}

	if peak := set.peak(); peak != len(names) {
		t.Errorf("peak concurrent probes = %d, want %d (tool calls did not run in parallel)", peak, len(names))
	}

	want := []string{"probe_a-done", "probe_b-done", "probe_c-done"}
	if got := toolResultContents(t, provider); !reflect.DeepEqual(got, want) {
		t.Errorf("tool result order = %v, want %v", got, want)
	}
}

// TestSequentialToolForcesSequentialExecution verifies that a single tool which
// opts out of parallelism makes the whole batch run one call at a time.
func TestSequentialToolForcesSequentialExecution(t *testing.T) {
	set := &probeSet{}
	provider := &toolCallingMockProvider{toolNames: []string{"probe_seq", "probe_par"}}

	seq := &sequentialProbe{&concurrencyProbe{set: set, id: "probe_seq"}}
	par := &concurrencyProbe{set: set, id: "probe_par"}

	al := newProbeAgent(t, provider, seq, par)
	helper := testHelper{al: al}

	resp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:    "test",
		SenderID:   "user1",
		ChatID:     "chat1",
		Content:    "run mixed probes",
		SessionKey: "probe-mixed-session",
	})
	if resp != "done" {
		t.Fatalf("response = %q, want %q", resp, "done")
	}

	if peak := set.peak(); peak != 1 {
		t.Errorf("peak concurrent probes = %d, want 1 (batch should fall back to sequential)", peak)
	}

	want := []string{"probe_seq-done", "probe_par-done"}
	if got := toolResultContents(t, provider); !reflect.DeepEqual(got, want) {
		t.Errorf("tool result order = %v, want %v", got, want)
	}
}
