package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/learning"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/stretchr/testify/require"
)

type fakeLearningRetriever struct {
	lessons []learning.LessonRecord
	err     error
	calls   int
	query   string
	topK    int
}

func (f *fakeLearningRetriever) Search(_ context.Context, query string, topK int) ([]learning.LessonRecord, error) {
	f.calls++
	f.query = query
	f.topK = topK
	if f.err != nil {
		return nil, f.err
	}
	out := make([]learning.LessonRecord, len(f.lessons))
	copy(out, f.lessons)
	return out, nil
}

func newLearningTestContextBuilder(t *testing.T) *ContextBuilder {
	t.Helper()
	workspace := t.TempDir()
	cb := &ContextBuilder{
		workspace:    workspace,
		skillsLoader: skills.NewSkillsLoader(workspace, "", ""),
		memory:       NewMemoryStore(workspace),
	}
	require.NoError(t, cb.memory.WriteLongTerm("- remembers the long-term memory fixture\n"))
	return cb
}

func normalizePromptCurrentTime(prompt string) string {
	lines := strings.Split(prompt, "\n")
	for i := 0; i < len(lines)-1; i++ {
		if lines[i] == "## Current Time" {
			lines[i+1] = "<normalized-time>"
			break
		}
	}
	return strings.Join(lines, "\n")
}

func TestContextBuilderBuildMessagesInjectsLearningBeforeMemory(t *testing.T) {
	cb := newLearningTestContextBuilder(t)
	retriever := &fakeLearningRetriever{lessons: []learning.LessonRecord{{
		ID:             "lesson_123",
		Situation:      "Qdrant timeouts happened during retrieval",
		Correction:     "Keep learning retrieval non-fatal",
		BetterApproach: "Swallow retrieval errors and continue with the existing prompt",
		Outcome:        "success",
		Tags:           []string{"learning", "retrieval"},
	}}}
	cb.SetLearningRetriever(retriever, &config.LearningConfig{Enabled: true})

	messages := cb.BuildMessages(context.Background(), nil, "", "fix qdrant timeout bug", nil, "", "")
	require.Len(t, messages, 2)
	prompt := messages[0].Content

	require.Equal(t, 1, retriever.calls)
	require.Equal(t, "fix qdrant timeout bug", retriever.query)
	require.Equal(t, 3, retriever.topK)
	require.Contains(t, prompt, "## Past Learnings (use these to avoid repeating mistakes)")
	require.Contains(t, prompt, "Lesson ID: lesson_123")
	require.Contains(t, prompt, "Swallow retrieval errors and continue with the existing prompt")
	require.Less(t, strings.Index(prompt, "## Past Learnings (use these to avoid repeating mistakes)"), strings.Index(prompt, "# Memory\n\n"))

	injected := cb.GetInjectedLessons()
	require.Len(t, injected, 1)
	require.Equal(t, "lesson_123", injected[0].ID)

	injected[0].ID = "mutated"
	require.Equal(t, "lesson_123", cb.GetInjectedLessons()[0].ID)
}

func TestContextBuilderBuildMessagesLearningFailureIsSilent(t *testing.T) {
	base := newLearningTestContextBuilder(t)
	baselinePrompt := base.BuildMessages(context.Background(), nil, "", "fix qdrant timeout bug", nil, "", "")[0].Content

	cb := &ContextBuilder{
		workspace:    base.workspace,
		skillsLoader: skills.NewSkillsLoader(base.workspace, "", ""),
		memory:       NewMemoryStore(base.workspace),
	}
	cb.SetLearningRetriever(&fakeLearningRetriever{err: errors.New("embed failed")}, &config.LearningConfig{Enabled: true})

	messages := cb.BuildMessages(context.Background(), nil, "", "fix qdrant timeout bug", nil, "", "")
	require.Len(t, messages, 2)
	require.Equal(t, normalizePromptCurrentTime(baselinePrompt), normalizePromptCurrentTime(messages[0].Content))
	require.NotContains(t, messages[0].Content, "## Past Learnings (use these to avoid repeating mistakes)")
	require.Nil(t, cb.GetInjectedLessons())
}

func TestContextBuilderBuildMessagesSkipsLearningWhenDisabledOrConfigNil(t *testing.T) {
	t.Run("disabled config", func(t *testing.T) {
		cb := newLearningTestContextBuilder(t)
		cb.SetLearningRetriever(&fakeLearningRetriever{lessons: []learning.LessonRecord{{ID: "lesson_disabled"}}}, &config.LearningConfig{Enabled: false})

		messages := cb.BuildMessages(context.Background(), nil, "", "fix qdrant timeout bug", nil, "", "")
		require.NotContains(t, messages[0].Content, "## Past Learnings (use these to avoid repeating mistakes)")
		require.Nil(t, cb.GetInjectedLessons())
	})

	t.Run("nil config", func(t *testing.T) {
		cb := newLearningTestContextBuilder(t)
		cb.SetLearningRetriever(&fakeLearningRetriever{lessons: []learning.LessonRecord{{ID: "lesson_nil_cfg"}}}, nil)

		messages := cb.BuildMessages(context.Background(), nil, "", "fix qdrant timeout bug", nil, "", "")
		require.NotContains(t, messages[0].Content, "## Past Learnings (use these to avoid repeating mistakes)")
		require.Nil(t, cb.GetInjectedLessons())
	})
}

func TestContextBuilderInjectedLessonsClearedBetweenBuilds(t *testing.T) {
	cb := newLearningTestContextBuilder(t)
	retriever := &fakeLearningRetriever{lessons: []learning.LessonRecord{{
		ID:             "lesson_stale",
		Situation:      "Previous failure",
		BetterApproach: "Use the corrected path",
	}}}
	cb.SetLearningRetriever(retriever, &config.LearningConfig{Enabled: true})

	first := cb.BuildMessages(context.Background(), nil, "", "first query", nil, "", "")
	require.Contains(t, first[0].Content, "lesson_stale")
	require.Len(t, cb.GetInjectedLessons(), 1)

	cb.SetLearningRetriever(retriever, &config.LearningConfig{Enabled: false})
	second := cb.BuildMessages(context.Background(), nil, "", "second query", nil, "", "")
	require.NotContains(t, second[0].Content, "## Past Learnings (use these to avoid repeating mistakes)")
	require.Nil(t, cb.GetInjectedLessons())
}
