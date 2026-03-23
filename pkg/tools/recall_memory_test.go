package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeMemoryWorkspace(t *testing.T) (workspace string, memoryDir string) {
	t.Helper()
	workspace = t.TempDir()
	memoryDir = filepath.Join(workspace, "memory")
	require.NoError(t, os.MkdirAll(memoryDir, 0o755))
	return workspace, memoryDir
}

// TestRecallMemoryTool_Name verifies the tool name.
func TestRecallMemoryTool_Name(t *testing.T) {
	tool := NewRecallMemoryTool("/some/workspace")
	assert.Equal(t, "recall_memory", tool.Name())
}

// TestRecallMemoryTool_Description verifies the description is non-empty.
func TestRecallMemoryTool_Description(t *testing.T) {
	tool := NewRecallMemoryTool("/some/workspace")
	assert.NotEmpty(t, tool.Description())
}

// TestRecallMemoryTool_Parameters verifies the parameter schema is correct.
func TestRecallMemoryTool_Parameters(t *testing.T) {
	tool := NewRecallMemoryTool("/some/workspace")
	params := tool.Parameters()

	assert.Equal(t, "object", params["type"])

	props, ok := params["properties"].(map[string]any)
	require.True(t, ok, "properties should be map[string]any")

	pathProp, ok := props["path"].(map[string]any)
	require.True(t, ok, "path property should exist")
	assert.Equal(t, "string", pathProp["type"])

	required, ok := params["required"].([]string)
	require.True(t, ok, "required should be []string")
	assert.Contains(t, required, "path")
}

// TestRecallMemoryTool_ReadExistingFile verifies reading an existing memory file.
func TestRecallMemoryTool_ReadExistingFile(t *testing.T) {
	workspace, memoryDir := makeMemoryWorkspace(t)

	content := "# Alan Profile\nName: Alan\n"
	subDir := filepath.Join(memoryDir, "personal")
	require.NoError(t, os.MkdirAll(subDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "alan_profile.md"), []byte(content), 0o644))

	tool := NewRecallMemoryTool(workspace)
	result := tool.Execute(context.Background(), map[string]any{
		"path": "personal/alan_profile.md",
	})

	assert.False(t, result.IsError, "expected success, got: %s", result.ForLLM)
	assert.True(t, result.Silent, "expected SilentResult")
	assert.Equal(t, content, result.ForLLM)
}

// TestRecallMemoryTool_MissingFile verifies that a missing file returns an error.
func TestRecallMemoryTool_MissingFile(t *testing.T) {
	workspace, _ := makeMemoryWorkspace(t)

	tool := NewRecallMemoryTool(workspace)
	result := tool.Execute(context.Background(), map[string]any{
		"path": "nonexistent/file.md",
	})

	assert.True(t, result.IsError, "expected error for missing file")
	assert.Contains(t, result.ForLLM, "not found")
}

// TestRecallMemoryTool_PathTraversalBlocked verifies that path traversal is blocked.
func TestRecallMemoryTool_PathTraversalBlocked(t *testing.T) {
	workspace, _ := makeMemoryWorkspace(t)

	// Place a secret file outside the memory directory
	secret := filepath.Join(workspace, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("top secret"), 0o644))

	tool := NewRecallMemoryTool(workspace)
	result := tool.Execute(context.Background(), map[string]any{
		"path": "../secret.txt",
	})

	assert.True(t, result.IsError, "expected path traversal to be blocked")
	assert.Contains(t, result.ForLLM, "access denied")
}

// TestRecallMemoryTool_EmptyPath verifies that an empty path returns an error.
func TestRecallMemoryTool_EmptyPath(t *testing.T) {
	workspace, _ := makeMemoryWorkspace(t)

	tool := NewRecallMemoryTool(workspace)

	// Empty string value
	result := tool.Execute(context.Background(), map[string]any{
		"path": "",
	})
	assert.True(t, result.IsError, "expected error for empty path string")
	assert.Contains(t, result.ForLLM, "path is required")

	// Missing key entirely
	result = tool.Execute(context.Background(), map[string]any{})
	assert.True(t, result.IsError, "expected error when path key is absent")
	assert.Contains(t, result.ForLLM, "path is required")
}

// TestRecallMemoryTool_WhitespaceOnlyPath verifies that a whitespace-only path returns an error.
func TestRecallMemoryTool_WhitespaceOnlyPath(t *testing.T) {
	workspace, _ := makeMemoryWorkspace(t)

	tool := NewRecallMemoryTool(workspace)
	result := tool.Execute(context.Background(), map[string]any{
		"path": "   ",
	})

	assert.True(t, result.IsError, "expected error for whitespace-only path")
	assert.Contains(t, result.ForLLM, "path is required")
}
