package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/skills"
)

func TestBuildMessagesInjectsUserMemory(t *testing.T) {
	workspace := t.TempDir()
	cb := &ContextBuilder{
		workspace:    workspace,
		skillsLoader: skills.NewSkillsLoader(workspace, "", ""),
		memory:       NewMemoryStore(workspace),
	}
	require.NoError(t, cb.memory.WriteUser("telegram", "12345", "- Samuel: amigo de Alan\n"))
	require.NoError(t, cb.memory.WriteUser("telegram", "678", "- Alan\n"))

	messages := cb.BuildMessages(context.Background(), nil, "", "hola", nil, "telegram", "12345")
	prompt := messages[0].Content

	assert.Contains(t, prompt, "# User Memory")
	assert.Contains(t, prompt, "- Samuel: amigo de Alan")
	assert.NotContains(t, prompt, "- Alan\n")
}

func TestBuildMessagesWithoutChatHasNoUserMemory(t *testing.T) {
	workspace := t.TempDir()
	cb := &ContextBuilder{
		workspace:    workspace,
		skillsLoader: skills.NewSkillsLoader(workspace, "", ""),
		memory:       NewMemoryStore(workspace),
	}
	require.NoError(t, cb.memory.WriteUser("telegram", "12345", "- Samuel\n"))

	messages := cb.BuildMessages(context.Background(), nil, "", "hola", nil, "", "")
	prompt := messages[0].Content

	assert.NotContains(t, prompt, "# User Memory")
}
