package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestSkill(t *testing.T, baseDir, skillName, frontmatter, body string) {
	t.Helper()
	dir := filepath.Join(baseDir, "skills", skillName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := "---\n" + frontmatter + "\n---\n\n" + body
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}

func newTestTriggerEngine(t *testing.T, config TriggerConfig, skills map[string]string) *TriggerEngine {
	t.Helper()
	baseDir := t.TempDir()
	for name, frontmatter := range skills {
		createTestSkill(t, baseDir, name, frontmatter, "# "+name)
	}
	loader := NewSkillsLoader(baseDir, "", "")
	return NewTriggerEngine(loader, config)
}

func TestKeywordTrigger(t *testing.T) {
	engine := newTestTriggerEngine(t, TriggerConfig{Enabled: true, AutoInject: true}, map[string]string{
		"git-skill": strings.Join([]string{
			"name: git-skill",
			"description: git helper",
			"triggers:",
			"  keywords: [\"commit\", \"rebase\"]",
		}, "\n"),
	})

	matches := engine.MatchSkills(TriggerContext{UserMessage: "commit my changes", AgentType: "default"})
	require.Len(t, matches, 1)
	assert.Equal(t, "git-skill", matches[0].Skill.Name)
	assert.Equal(t, "keyword:commit", matches[0].Reason)
}

func TestToolTriggerQueuing(t *testing.T) {
	engine := newTestTriggerEngine(t, TriggerConfig{Enabled: true, AutoInject: true}, map[string]string{
		"shell-skill": strings.Join([]string{
			"name: shell-skill",
			"description: shell helper",
			"triggers:",
			"  tools: [\"shell\"]",
		}, "\n"),
	})

	engine.RecordToolCall("shell", "sess-1")
	matches := engine.MatchSkills(TriggerContext{SessionKey: "sess-1", AgentType: "default"})
	require.Len(t, matches, 1)
	assert.Equal(t, "tool:shell", matches[0].Reason)
}

func TestToolTriggerSessionIsolation(t *testing.T) {
	engine := newTestTriggerEngine(t, TriggerConfig{Enabled: true, AutoInject: true}, map[string]string{
		"shell-skill": strings.Join([]string{
			"name: shell-skill",
			"description: shell helper",
			"triggers:",
			"  tools: [\"shell\"]",
		}, "\n"),
	})

	engine.RecordToolCall("shell", "sess-1")
	matches := engine.MatchSkills(TriggerContext{SessionKey: "sess-2", AgentType: "default"})
	assert.Empty(t, matches)
}

func TestToolTriggerNotYetCalled(t *testing.T) {
	engine := newTestTriggerEngine(t, TriggerConfig{Enabled: true, AutoInject: true}, map[string]string{
		"shell-skill": strings.Join([]string{
			"name: shell-skill",
			"description: shell helper",
			"triggers:",
			"  tools: [\"shell\"]",
		}, "\n"),
	})

	matches := engine.MatchSkills(TriggerContext{SessionKey: "sess-1", AgentType: "default"})
	assert.Empty(t, matches)
}

func TestAgentFilter(t *testing.T) {
	engine := newTestTriggerEngine(t, TriggerConfig{Enabled: true, AutoInject: true}, map[string]string{
		"coder-skill": strings.Join([]string{
			"name: coder-skill",
			"description: coder helper",
			"triggers:",
			"  agents: [\"coder\"]",
		}, "\n"),
	})

	assert.Empty(t, engine.MatchSkills(TriggerContext{AgentType: "default"}))

	matches := engine.MatchSkills(TriggerContext{AgentType: "coder"})
	require.Len(t, matches, 1)
	assert.Equal(t, "agent:coder", matches[0].Reason)
}

func TestTokenBudgetTruncation(t *testing.T) {
	longString := strings.Repeat("abcdefghij", 100)
	truncated := truncateToTokenBudget(longString, 100)
	assert.LessOrEqual(t, len(truncated), 400)
	assert.True(t, strings.HasSuffix(truncated, "[... skill content truncated due to token budget]"))
}

func TestAllowBlockList(t *testing.T) {
	engine := newTestTriggerEngine(t, TriggerConfig{
		Enabled:    true,
		AutoInject: true,
		BlockList:  []string{"bad-skill"},
	}, map[string]string{
		"good-skill":  strings.Join([]string{"name: good-skill", "description: good"}, "\n"),
		"bad-skill":   strings.Join([]string{"name: bad-skill", "description: bad"}, "\n"),
		"other-skill": strings.Join([]string{"name: other-skill", "description: other"}, "\n"),
	})

	matches := engine.MatchSkills(TriggerContext{AgentType: "default"})
	require.Len(t, matches, 2)
	assert.Equal(t, []string{"good-skill", "other-skill"}, []string{matches[0].Skill.Name, matches[1].Skill.Name})
}

func TestMaxAutoInject(t *testing.T) {
	engine := newTestTriggerEngine(t, TriggerConfig{Enabled: true, AutoInject: true, MaxAutoInject: 3}, map[string]string{
		"skill-low-a": strings.Join([]string{
			"name: skill-low-a",
			"description: low",
			"inject:",
			"  priority: low",
		}, "\n"),
		"skill-high-a": strings.Join([]string{
			"name: skill-high-a",
			"description: high",
			"inject:",
			"  priority: high",
		}, "\n"),
		"skill-normal": strings.Join([]string{
			"name: skill-normal",
			"description: normal",
			"inject:",
			"  priority: normal",
		}, "\n"),
		"skill-high-b": strings.Join([]string{
			"name: skill-high-b",
			"description: high",
			"inject:",
			"  priority: high",
		}, "\n"),
		"skill-low-b": strings.Join([]string{
			"name: skill-low-b",
			"description: low",
			"inject:",
			"  priority: low",
		}, "\n"),
	})

	matches := engine.MatchSkills(TriggerContext{AgentType: "default"})
	require.Len(t, matches, 3)
	assert.Equal(t, []string{"skill-high-a", "skill-high-b", "skill-normal"}, []string{matches[0].Skill.Name, matches[1].Skill.Name, matches[2].Skill.Name})
	assert.Equal(t, []string{"high", "high", "normal"}, []string{matches[0].Priority, matches[1].Priority, matches[2].Priority})
}

func TestConfigDisabled(t *testing.T) {
	engine := newTestTriggerEngine(t, TriggerConfig{Enabled: false, AutoInject: true}, map[string]string{
		"git-skill": strings.Join([]string{
			"name: git-skill",
			"description: git helper",
			"triggers:",
			"  keywords: [\"commit\"]",
		}, "\n"),
	})

	assert.Empty(t, engine.MatchSkills(TriggerContext{UserMessage: "commit my changes", AgentType: "default"}))
}
