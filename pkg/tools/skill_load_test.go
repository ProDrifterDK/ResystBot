package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/skills"
)

func TestSkillLoadToolFound(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: test-skill\ndescription: A test skill\n---\n\n# Test Skill\n\nDo the thing."), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := skills.NewSkillsLoader(tmpDir, "", "")
	tool := NewSkillLoadTool(loader)

	result := tool.Execute(context.Background(), map[string]any{"name": "test-skill"})

	if result.IsError {
		t.Errorf("expected no error, got IsError=true, ForLLM=%s", result.ForLLM)
	}
	if !result.Silent {
		t.Error("expected Silent=true")
	}
	if !contains(result.ForLLM, "<skill_content") {
		t.Errorf("expected ForLLM to contain '<skill_content', got: %s", result.ForLLM)
	}
	if !contains(result.ForLLM, "Do the thing") {
		t.Errorf("expected ForLLM to contain skill body, got: %s", result.ForLLM)
	}
}

func TestSkillLoadToolMiss(t *testing.T) {
	loader := skills.NewSkillsLoader(t.TempDir(), "", "")
	tool := NewSkillLoadTool(loader)

	result := tool.Execute(context.Background(), map[string]any{"name": "nonexistent"})

	if !result.IsError {
		t.Error("expected IsError=true for nonexistent skill")
	}
	if !contains(result.ForLLM, "not found") {
		t.Errorf("expected 'not found' in ForLLM, got: %s", result.ForLLM)
	}
}

func TestSkillLoadToolEmptyName(t *testing.T) {
	loader := skills.NewSkillsLoader(t.TempDir(), "", "")
	tool := NewSkillLoadTool(loader)

	result := tool.Execute(context.Background(), map[string]any{"name": ""})
	if !result.IsError {
		t.Error("expected IsError=true for empty name")
	}

	result = tool.Execute(context.Background(), map[string]any{})
	if !result.IsError {
		t.Error("expected IsError=true for missing name arg")
	}
}

func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
