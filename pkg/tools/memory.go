package tools

import (
	"context"
	"fmt"
	"strings"
)

// MarkdownMemoryStore backs the memory tool. Implemented by agent.MemoryStore;
// defined here to avoid an import cycle (agent imports tools).
type MarkdownMemoryStore interface {
	Update(target, channel, chatID string, fn func(string) (string, error)) (string, error)
	LimitChars(target string) int
}

// MemoryTool writes durable facts to persistent markdown memory.
// target "memory" is the shared MEMORY.md; target "user" is the per-chat
// profile file (memory/users/<channel>-<chatID>.md).
type MemoryTool struct {
	store   MarkdownMemoryStore
	channel string
	chatID  string
}

// NewMemoryTool creates a MemoryTool backed by the given store.
func NewMemoryTool(store MarkdownMemoryStore) *MemoryTool {
	return &MemoryTool{store: store}
}

// SetContext implements ContextualTool; the agent loop calls it per message.
func (t *MemoryTool) SetContext(channel, chatID string) {
	t.channel = channel
	t.chatID = chatID
}

func (t *MemoryTool) Name() string { return "memory" }

func (t *MemoryTool) Description() string {
	return "Save durable facts to persistent memory that survives across sessions. " +
		"target 'memory' (default) is the shared MEMORY.md: general facts, preferences, corrections, lessons. " +
		"target 'user' is the profile of the person in the current chat (their own file, per chat ID): who they are, how to work with them. " +
		"Actions: 'add' appends an entry, 'replace' substitutes old_text with content, 'remove' deletes old_text. " +
		"Use 'operations' to apply several changes in ONE call; the batch applies atomically and the char limit is checked only on the final result. " +
		"Memory is injected into future turns, so keep entries compact and high-signal. " +
		"Save proactively when the user states a preference, correction, or personal detail."
}

func (t *MemoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "replace", "remove"},
				"description": "Single operation to apply. Omit when using 'operations'.",
			},
			"target": map[string]any{
				"type":        "string",
				"enum":        []string{"memory", "user"},
				"description": "'memory' (default) for shared facts, 'user' for the current chat's user profile.",
				"default":     "memory",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Text to add, or the replacement text for 'replace'. Required for add/replace.",
			},
			"new_text": map[string]any{
				"type":        "string",
				"description": "Alias for content.",
			},
			"old_text": map[string]any{
				"type":        "string",
				"description": "Exact existing text to replace or remove. Required for replace/remove.",
			},
			"operations": map[string]any{
				"type":        "array",
				"description": "Batch shape: list of {action, content?, old_text?} applied atomically in one call.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action":   map[string]any{"type": "string", "enum": []string{"add", "replace", "remove"}},
						"content":  map[string]any{"type": "string"},
						"old_text": map[string]any{"type": "string"},
					},
					"required": []string{"action"},
				},
			},
		},
	}
}

type memoryOp struct {
	action  string
	content string
	oldText string
}

func (t *MemoryTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t.store == nil {
		return ErrorResult("memory is not available")
	}

	target, _ := args["target"].(string)
	if target == "" {
		target = "memory"
	}
	if target != "memory" && target != "user" {
		return ErrorResult(fmt.Sprintf("invalid target %q: use 'memory' or 'user'", target))
	}
	if target == "user" && t.chatID == "" {
		return ErrorResult("target 'user' requires an active chat context (no chat ID available)")
	}

	ops, err := parseMemoryOps(args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	final, err := t.store.Update(target, t.channel, t.chatID, func(current string) (string, error) {
		return applyMemoryOps(current, ops)
	})
	if err != nil {
		return ErrorResult(err.Error())
	}

	return SilentResult(fmt.Sprintf("Memory updated (target=%s, %d ops applied, %d/%d chars).",
		target, len(ops), len(final), t.store.LimitChars(target)))
}

func parseMemoryOps(args map[string]any) ([]memoryOp, error) {
	if rawOps, ok := args["operations"].([]any); ok && len(rawOps) > 0 {
		ops := make([]memoryOp, 0, len(rawOps))
		for i, raw := range rawOps {
			m, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("operations[%d] must be an object", i)
			}
			op, err := parseMemoryOp(m)
			if err != nil {
				return nil, fmt.Errorf("operations[%d]: %w", i, err)
			}
			ops = append(ops, op)
		}
		return ops, nil
	}

	op, err := parseMemoryOp(args)
	if err != nil {
		return nil, err
	}
	return []memoryOp{op}, nil
}

func parseMemoryOp(m map[string]any) (memoryOp, error) {
	action, _ := m["action"].(string)
	content, _ := m["content"].(string)
	if content == "" {
		content, _ = m["new_text"].(string)
	}
	oldText, _ := m["old_text"].(string)

	switch action {
	case "add":
		if content == "" {
			return memoryOp{}, fmt.Errorf("content is required for 'add'")
		}
	case "replace":
		if oldText == "" {
			return memoryOp{}, fmt.Errorf("old_text is required for 'replace'; read the file first (recall_memory) to copy the exact text")
		}
		if content == "" {
			return memoryOp{}, fmt.Errorf("content is required for 'replace'")
		}
	case "remove":
		if oldText == "" {
			return memoryOp{}, fmt.Errorf("old_text is required for 'remove'; read the file first (recall_memory) to copy the exact text")
		}
	default:
		return memoryOp{}, fmt.Errorf("unknown action %q: use add, replace, remove (or 'operations' for a batch)", action)
	}
	return memoryOp{action: action, content: content, oldText: oldText}, nil
}

func applyMemoryOps(content string, ops []memoryOp) (string, error) {
	for _, op := range ops {
		switch op.action {
		case "add":
			if content != "" && !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			content += op.content
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
		case "replace":
			if !strings.Contains(content, op.oldText) {
				return "", fmt.Errorf("old_text not found in memory; read the file first (recall_memory) to copy the exact text")
			}
			content = strings.Replace(content, op.oldText, op.content, 1)
		case "remove":
			if !strings.Contains(content, op.oldText) {
				return "", fmt.Errorf("old_text not found in memory; read the file first (recall_memory) to copy the exact text")
			}
			content = strings.Replace(content, op.oldText, "", 1)
		}
	}
	return content, nil
}
