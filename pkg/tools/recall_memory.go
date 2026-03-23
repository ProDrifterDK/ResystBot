package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RecallMemoryTool reads memory files on demand from the agent's memory directory.
// It provides safe, restricted access to {workspace}/memory/ contents.
type RecallMemoryTool struct {
	memoryDir string
}

// NewRecallMemoryTool creates a new RecallMemoryTool rooted at {workspace}/memory/.
func NewRecallMemoryTool(workspace string) *RecallMemoryTool {
	return &RecallMemoryTool{
		memoryDir: filepath.Join(workspace, "memory"),
	}
}

func (t *RecallMemoryTool) Name() string {
	return "recall_memory"
}

func (t *RecallMemoryTool) Description() string {
	return "Read a memory file by its relative path (e.g., 'personal/alan_profile.md'). Use this when you need context from your persistent memory."
}

func (t *RecallMemoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the memory file, relative to the memory directory (e.g. personal/alan_profile.md)",
			},
		},
		"required": []string{"path"},
	}
}

func (t *RecallMemoryTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return ErrorResult("path is required")
	}

	absPath, err := t.safePath(path)
	if err != nil {
		return ErrorResult(err.Error())
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrorResult(fmt.Sprintf("memory file not found: %s", path))
		}
		return ErrorResult(fmt.Sprintf("failed to read memory file: %v", err))
	}

	return SilentResult(string(content))
}

// safePath resolves the given relative path inside memoryDir and validates that
// the resulting absolute path is still within memoryDir (path traversal guard).
func (t *RecallMemoryTool) safePath(rel string) (string, error) {
	// filepath.Clean removes any ".." components; joining under memoryDir then
	// checking the prefix prevents traversal attacks.
	joined := filepath.Join(t.memoryDir, rel)
	clean := filepath.Clean(joined)

	// Ensure the resolved path is inside memoryDir (use trailing separator to
	// prevent a prefix like /foo/memory matching /foo/memory_other).
	prefix := t.memoryDir
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}

	if clean != filepath.Clean(t.memoryDir) && !strings.HasPrefix(clean, prefix) {
		return "", fmt.Errorf("access denied: path is outside the memory directory")
	}

	return clean, nil
}
