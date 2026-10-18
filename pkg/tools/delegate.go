package tools

import (
	"context"
	"fmt"
	"strings"
)

// DelegateTool hands a task off to a named agent and blocks until the result is
// ready. It sits between the two existing subagent entry points:
//
//   - spawn    — asynchronous, fire-and-forget, may target a named agent
//   - subagent — synchronous, but always runs with the *current* agent profile
//   - delegate — synchronous *and* targets a named agent, running the task with
//     that agent's own provider, model, tools and iteration budget
//
// Ported from upstream commit 484ef399 (issue #2148), adapted to this fork's
// SubagentManager (the upstream SubTurnSpawner/SubTurnConfig plumbing does not
// exist here).
type DelegateTool struct {
	manager        *SubagentManager
	originChannel  string
	originChatID   string
	allowlistCheck func(targetAgentID string) bool
	selfAgentID    string
}

func NewDelegateTool(manager *SubagentManager) *DelegateTool {
	return &DelegateTool{
		manager:       manager,
		originChannel: "cli",
		originChatID:  "direct",
	}
}

func (t *DelegateTool) Name() string {
	return "delegate"
}

// IsSequential implements SequentialTool: delegate blocks the caller and stores
// its origin context on the shared receiver, so two concurrent calls would race
// on that state (and would serialize anyway while waiting for the LLM).
func (t *DelegateTool) IsSequential() bool { return true }

func (t *DelegateTool) Description() string {
	return "Delegate a task to another agent and wait for the result. " +
		"Use this when another agent is better suited to handle a specific task " +
		"based on their capabilities. The target agent runs with its own workspace, " +
		"model, and tools."
}

func (t *DelegateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"description": "The ID of the target agent to delegate the task to",
			},
			"task": map[string]any{
				"type":        "string",
				"description": "Clear description of the task to delegate",
			},
		},
		"required": []string{"agent_id", "task"},
	}
}

// SetContext stores the origin channel/chat so the delegated run is attributed
// and routed correctly. Mirrors SpawnTool/SubagentTool.
func (t *DelegateTool) SetContext(channel, chatID string) {
	t.originChannel = channel
	t.originChatID = chatID
}

// SetAllowlistChecker installs the permission gate (subagents.allow_agents).
func (t *DelegateTool) SetAllowlistChecker(check func(targetAgentID string) bool) {
	t.allowlistCheck = check
}

// SetSelfAgentID sets the agent ID that owns this tool, so self-delegation can
// be rejected explicitly instead of relying on the allowlist.
func (t *DelegateTool) SetSelfAgentID(id string) {
	t.selfAgentID = id
}

func (t *DelegateTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	agentID, _ := args["agent_id"].(string)
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return ErrorResult("agent_id is required and must be a non-empty string")
	}

	task, _ := args["task"].(string)
	if strings.TrimSpace(task) == "" {
		return ErrorResult("task is required and must be a non-empty string")
	}

	if t.selfAgentID != "" && agentID == t.selfAgentID {
		return ErrorResult("cannot delegate to self")
	}

	if t.allowlistCheck != nil && !t.allowlistCheck(agentID) {
		return ErrorResult(fmt.Sprintf("not allowed to delegate to agent %q", agentID))
	}

	if t.manager == nil {
		return ErrorResult("delegate tool not configured")
	}

	result, err := t.manager.Delegate(ctx, task, agentID, "", t.originChannel, t.originChatID)
	if err != nil {
		return ErrorResult(fmt.Sprintf("delegation to agent %q failed: %v", agentID, err)).WithError(err)
	}
	if result == nil {
		return ErrorResult(fmt.Sprintf("delegation to agent %q returned no result", agentID))
	}

	// Attribute the answer to the agent that produced it, for both the model
	// and the user. Upstream only rewrites ForLLM; we also tag ForUser so a
	// delegated answer is not presented to the user as if the main agent said it.
	result.ForLLM = fmt.Sprintf("[Response from agent %q]\n%s", agentID, result.ForLLM)
	if result.ForUser != "" {
		result.ForUser = fmt.Sprintf("[%s] %s", agentID, result.ForUser)
	}

	return result
}
