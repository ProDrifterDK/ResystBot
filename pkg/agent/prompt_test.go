package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/stretchr/testify/require"
)

func TestBuildSystemPrompt_PromptLayering_ProducesIdenticalOutput(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# Agent bootstrap\n"), 0o644))

	skillDir := filepath.Join(workspace, "skills", "test-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: test-skill
description: Test skill for prompt layering regression
triggers:
  keywords: ["trigger me"]
  agents: ["default"]
inject:
  priority: normal
---

# Test Skill

Loaded on demand.
`), 0o644))

	cb := &ContextBuilder{
		workspace:    workspace,
		skillsLoader: skills.NewSkillsLoader(workspace, "", ""),
		memory:       NewMemoryStore(workspace),
	}
	require.NoError(t, cb.memory.WriteLongTerm("- remembers prompt regression fixtures\n"))

	cb.triggerEngine = skills.NewTriggerEngine(cb.skillsLoader, skills.TriggerConfig{
		Enabled:       true,
		AutoInject:    true,
		MaxAutoInject: 5,
	})
	cb.UpdateTriggerContext(skills.TriggerContext{UserMessage: "please trigger me now", AgentType: "default"})

	legacy := cb.buildSystemPromptLegacy()
	current := cb.BuildSystemPrompt()

	require.Equal(t, legacy, current)
	// Sanity-check that the comparison exercised all layers we care about.
	require.Contains(t, current, "# Agent bootstrap")
	require.Contains(t, current, "# Skills")
	require.Contains(t, current, `<skill_content name="test-skill" reason="keyword:trigger me">`)
	require.Contains(t, current, "# Memory")
}
