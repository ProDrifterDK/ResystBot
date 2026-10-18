package tools

import (
	"context"
	"testing"
)

// TestMutatingToolsAreSequential is a regression guard: every tool that mutates
// shared state (files, sessions, schedules, workspace) must implement
// SequentialTool so the agent loop falls back to sequential execution whenever
// such a tool is part of a multi-call batch. Running them concurrently would
// introduce data races (e.g. read-modify-write on the same file).
func TestMutatingToolsAreSequential(t *testing.T) {
	mutating := []SequentialTool{
		(*EditFileTool)(nil),
		(*AppendFileTool)(nil),
		(*WriteFileTool)(nil),
		(*ExecSessionTool)(nil),
		(*SpawnTool)(nil),
		(*SubagentTool)(nil),
		(*MemoryTool)(nil),
		(*CronTool)(nil),
		(*ClaudeCodeTool)(nil),
		(*InstallSkillTool)(nil),
		(*MessageTool)(nil),
	}
	for _, tool := range mutating {
		if !tool.IsSequential() {
			t.Errorf("%s must report IsSequential()==true", tool.Name())
		}
	}
}

// TestReadOnlyToolsAreParallelisable is the counterpart guard: pure/read-only
// tools must NOT opt out of parallel execution, otherwise the latency win is
// silently lost.
func TestReadOnlyToolsAreParallelisable(t *testing.T) {
	readOnly := []Tool{
		(*ReadFileTool)(nil),
		(*ListDirTool)(nil),
		(*WebSearchTool)(nil),
		(*WebFetchTool)(nil),
		(*SearchMemoryTool)(nil),
		(*SessionSearchTool)(nil),
		(*RecallMemoryTool)(nil),
		(*FindSkillsTool)(nil),
		(*SkillLoadTool)(nil),
	}
	for _, tool := range readOnly {
		if IsSequential(tool) {
			t.Errorf("%s should be concurrency-safe (parallelisable)", tool.Name())
		}
	}
}

type plainStubTool struct{}

func (plainStubTool) Name() string               { return "plain" }
func (plainStubTool) Description() string        { return "plain stub" }
func (plainStubTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (plainStubTool) Execute(_ context.Context, _ map[string]any) *ToolResult {
	return SilentResult("ok")
}

type seqStubTool struct{ plainStubTool }

func (seqStubTool) Name() string       { return "seq" }
func (seqStubTool) IsSequential() bool { return true }

// TestIsSequentialHelper verifies the interface helper's default behaviour:
// tools that don't implement SequentialTool are treated as parallel-safe.
func TestIsSequentialHelper(t *testing.T) {
	if IsSequential(plainStubTool{}) {
		t.Error("plain tool without SequentialTool must be treated as parallel-safe")
	}
	if !IsSequential(seqStubTool{}) {
		t.Error("tool implementing SequentialTool(true) must be reported as sequential")
	}
}

// TestRegistryIsToolSequential verifies the registry lookup used by the agent
// loop, including the unknown-tool default.
func TestRegistryIsToolSequential(t *testing.T) {
	r := NewToolRegistry()
	r.Register(plainStubTool{})
	r.Register(seqStubTool{})

	if r.IsToolSequential("plain") {
		t.Error("IsToolSequential(plain) = true, want false")
	}
	if !r.IsToolSequential("seq") {
		t.Error("IsToolSequential(seq) = false, want true")
	}
	if r.IsToolSequential("does-not-exist") {
		t.Error("IsToolSequential(unknown) = true, want false (default parallel-safe)")
	}
}
