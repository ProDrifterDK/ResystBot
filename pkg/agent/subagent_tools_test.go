package agent

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// toolRecordingProvider records the tool definitions offered on each Chat call
// so tests can assert exactly which tools a subagent run was given.
type toolRecordingProvider struct {
	offered [][]string
}

func (p *toolRecordingProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	toolDefs []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	names := make([]string, 0, len(toolDefs))
	for _, def := range toolDefs {
		names = append(names, def.Function.Name)
	}
	p.offered = append(p.offered, names)
	return &providers.LLMResponse{Content: "done"}, nil
}

func (p *toolRecordingProvider) GetDefaultModel() string { return "mock-model" }

// offeredNames returns the tool names from the first Chat call, or nil.
func (p *toolRecordingProvider) offeredNames() []string {
	if len(p.offered) == 0 {
		return nil
	}
	return p.offered[0]
}

func namesToSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// newSubagentToolsLoop builds an AgentLoop with a "main" agent allowed to
// spawn/delegate to "helper". helperSubagents configures the helper's own
// subagents block (nil for the default).
func newSubagentToolsLoop(t *testing.T, helperSubagents *config.SubagentsConfig) (*AgentLoop, *toolRecordingProvider) {
	t.Helper()
	cfg := &config.Config{
		// claude_code is opt-in globally; enable it so the privileged-tool
		// stripping has something to strip. Otherwise the exclusion assertions
		// would pass because the tool was never registered at all.
		Tools: config.ToolsConfig{
			ClaudeCode: config.ClaudeCodeConfig{Enabled: true},
		},
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 5,
			},
			List: []config.AgentConfig{
				{
					ID:      "main",
					Default: true,
					Subagents: &config.SubagentsConfig{
						AllowAgents: []string{"helper"},
					},
				},
				{
					ID:        "helper",
					Subagents: helperSubagents,
				},
			},
		},
	}
	provider := &toolRecordingProvider{}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	return al, provider
}

// assertNoRecursiveTools checks the offered tool set excludes the tools that
// must never reach a subagent run by default.
func assertNoRecursiveTools(t *testing.T, offered []string) {
	t.Helper()
	set := namesToSet(offered)
	if len(set) == 0 {
		t.Fatal("subagent run was offered no tools at all")
	}
	for _, banned := range []string{"spawn", "delegate", "message"} {
		if set[banned] {
			t.Errorf("subagent run was offered %q; recursive/outbound tools must be stripped (offered=%v)", banned, offered)
		}
	}
	// Sanity: the run must still have ordinary tools, otherwise the assertion
	// above would pass for the wrong reason.
	if !set["web_fetch"] {
		t.Errorf("subagent run lost its ordinary tools; offered=%v", offered)
	}
}

// TestSubagentTargetProfileExcludesRecursiveTools is the regression test for the
// second-pass hole: a run targeting agent_id="helper" inherited the full tool
// registry of "helper", which by then already contained spawn and delegate.
func TestSubagentTargetProfileExcludesRecursiveTools(t *testing.T) {
	al, provider := newSubagentToolsLoop(t, nil)

	manager, ok := al.subagentManagers["main"]
	if !ok {
		t.Fatal("no subagent manager for main")
	}
	if _, err := manager.Delegate(context.Background(), "do the thing", "helper", "", "", ""); err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}

	assertNoRecursiveTools(t, provider.offeredNames())
}

// TestSubagentDefaultProfileExcludesRecursiveTools covers the no-agent_id path,
// which uses the manager's own default tool registry.
func TestSubagentDefaultProfileExcludesRecursiveTools(t *testing.T) {
	al, provider := newSubagentToolsLoop(t, nil)

	manager, ok := al.subagentManagers["main"]
	if !ok {
		t.Fatal("no subagent manager for main")
	}
	if _, err := manager.Delegate(context.Background(), "do the thing", "", "", "", ""); err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}

	assertNoRecursiveTools(t, provider.offeredNames())
}

// TestSubagentTargetProfileKeepsRecursiveToolsWhenEnabled asserts the opt-in:
// with allow_recursive_delegation on the target agent, spawn/delegate survive
// but the message tool is still stripped.
func TestSubagentTargetProfileKeepsRecursiveToolsWhenEnabled(t *testing.T) {
	al, provider := newSubagentToolsLoop(t, &config.SubagentsConfig{AllowRecursiveDelegation: true})

	manager, ok := al.subagentManagers["main"]
	if !ok {
		t.Fatal("no subagent manager for main")
	}
	if _, err := manager.Delegate(context.Background(), "do the thing", "helper", "", "", ""); err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}

	set := namesToSet(provider.offeredNames())
	if !set["spawn"] {
		t.Errorf("spawn should be kept when recursive delegation is enabled; offered=%v", provider.offeredNames())
	}
	if !set["delegate"] {
		t.Errorf("delegate should be kept when recursive delegation is enabled; offered=%v", provider.offeredNames())
	}
	if set["message"] {
		t.Error("message must be stripped even when recursive delegation is enabled")
	}
}

// privilegedTools are registered on every agent, but they expand the run's own
// capabilities (install new skills, hand off to an autonomous external agent),
// so they must not reach a subagent run unless the target agent opts in.
var privilegedTools = []string{"find_skills", "install_skill", "claude_code"}

// TestSubagentTargetProfileExcludesPrivilegedTools is the regression test for
// the escalation surface: a subagent run inherited find_skills, install_skill
// and claude_code from the target agent's registry.
func TestSubagentTargetProfileExcludesPrivilegedTools(t *testing.T) {
	al, provider := newSubagentToolsLoop(t, nil)

	// The parent registry must actually hold the tools, otherwise the
	// exclusion assertions below would pass for the wrong reason (e.g. if the
	// registration were moved after the snapshot).
	parent, ok := al.registry.GetAgent("helper")
	if !ok {
		t.Fatal("no agent helper")
	}
	for _, name := range privilegedTools {
		if _, ok := parent.Tools.Get(name); !ok {
			t.Fatalf("test setup: %q is not registered on the parent agent", name)
		}
	}

	manager, ok := al.subagentManagers["main"]
	if !ok {
		t.Fatal("no subagent manager for main")
	}
	if _, err := manager.Delegate(context.Background(), "do the thing", "helper", "", "", ""); err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}

	offered := provider.offeredNames()
	set := namesToSet(offered)
	for _, name := range privilegedTools {
		if set[name] {
			t.Errorf("subagent run was offered privileged tool %q; offered=%v", name, offered)
		}
	}
	// Boundary pin: skill_load ("skill") only reads a skill already in the
	// workspace. It is not an escalation surface and must survive.
	if !set["skill"] {
		t.Errorf("skill_load must remain available to subagent runs; offered=%v", offered)
	}
}

// TestSubagentDefaultProfileExcludesPrivilegedTools covers the no-agent_id
// path, which uses the manager's own default tool registry.
func TestSubagentDefaultProfileExcludesPrivilegedTools(t *testing.T) {
	al, provider := newSubagentToolsLoop(t, nil)

	manager, ok := al.subagentManagers["main"]
	if !ok {
		t.Fatal("no subagent manager for main")
	}
	if _, err := manager.Delegate(context.Background(), "do the thing", "", "", "", ""); err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}

	offered := provider.offeredNames()
	set := namesToSet(offered)
	for _, name := range privilegedTools {
		if set[name] {
			t.Errorf("subagent run was offered privileged tool %q; offered=%v", name, offered)
		}
	}
}

// TestSubagentTargetProfileKeepsPrivilegedToolsWhenEnabled asserts the opt-in
// and, just as importantly, that the two subagent flags are independent axes:
// enabling privileged tools must not resurrect spawn/delegate.
func TestSubagentTargetProfileKeepsPrivilegedToolsWhenEnabled(t *testing.T) {
	al, provider := newSubagentToolsLoop(t, &config.SubagentsConfig{AllowPrivilegedTools: true})

	manager, ok := al.subagentManagers["main"]
	if !ok {
		t.Fatal("no subagent manager for main")
	}
	if _, err := manager.Delegate(context.Background(), "do the thing", "helper", "", "", ""); err != nil {
		t.Fatalf("Delegate() error = %v", err)
	}

	offered := provider.offeredNames()
	set := namesToSet(offered)
	for _, name := range privilegedTools {
		if !set[name] {
			t.Errorf("privileged tool %q should be kept when enabled; offered=%v", name, offered)
		}
	}
	if set["spawn"] || set["delegate"] {
		t.Errorf("allow_privileged_tools must not re-enable recursive tools; offered=%v", offered)
	}
	if set["message"] {
		t.Error("message must be stripped even when privileged tools are enabled")
	}
}
