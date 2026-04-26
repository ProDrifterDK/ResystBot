package skills

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestWatcher(t *testing.T, onChange func(name, event string)) (*SkillWatcher, string) {
	t.Helper()

	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "skills"), 0o755))

	loader := NewSkillsLoader(workspace, "", "")
	watcher, err := NewSkillWatcher(loader, onChange)
	require.NoError(t, err)

	return watcher, workspace
}

func writeSkillFile(t *testing.T, workspace, skillName, body string) string {
	t.Helper()

	skillDir := filepath.Join(workspace, "skills", skillName)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	skillFile := filepath.Join(skillDir, "SKILL.md")
	require.NoError(t, os.WriteFile(skillFile, []byte(body), 0o644))

	return skillFile
}

func TestWatcherCreateSkill(t *testing.T) {
	var mu sync.Mutex
	var events []string

	watcher, workspace := newTestWatcher(t, func(name, event string) {
		mu.Lock()
		events = append(events, name+":"+event)
		mu.Unlock()
	})
	watcher.SetDebounce(100 * time.Millisecond)
	require.NoError(t, watcher.Start())
	defer watcher.Stop()

	// Create directory first, then write file after a brief pause to ensure
	// the watcher picks up the directory creation before the file write.
	skillDir := filepath.Join(workspace, "skills", "new-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: new-skill\ndescription: test\n---\n\nBody"), 0o644))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 1
	}, 3*time.Second, 25*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 1)
	assert.Equal(t, "new-skill", strings.Split(events[0], ":")[0])
	assert.Contains(t, []string{"new-skill:created", "new-skill:modified"}, events[0])
}

func TestWatcherModifySkill(t *testing.T) {
	var mu sync.Mutex
	var events []string

	watcher, workspace := newTestWatcher(t, func(name, event string) {
		mu.Lock()
		events = append(events, name+":"+event)
		mu.Unlock()
	})
	watcher.SetDebounce(100 * time.Millisecond)

	skillFile := writeSkillFile(t, workspace, "existing-skill", "---\nname: existing-skill\ndescription: test\n---\n\nInitial")
	require.NoError(t, watcher.Start())
	defer watcher.Stop()

	require.NoError(t, os.WriteFile(skillFile, []byte("---\nname: existing-skill\ndescription: test\n---\n\nUpdated"), 0o644))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 1
	}, 1500*time.Millisecond, 25*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, events, "existing-skill:modified")
}

func TestWatcherDeleteSkill(t *testing.T) {
	var mu sync.Mutex
	var events []string

	watcher, workspace := newTestWatcher(t, func(name, event string) {
		mu.Lock()
		events = append(events, name+":"+event)
		mu.Unlock()
	})
	watcher.SetDebounce(100 * time.Millisecond)

	skillFile := writeSkillFile(t, workspace, "deleted-skill", "---\nname: deleted-skill\ndescription: test\n---\n\nBody")
	require.NoError(t, watcher.Start())
	defer watcher.Stop()

	require.NoError(t, os.Remove(skillFile))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 1
	}, 1500*time.Millisecond, 25*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, events, "deleted-skill:deleted")
}

func TestWatcherDebounce(t *testing.T) {
	var callCount atomic.Int32

	watcher, workspace := newTestWatcher(t, func(name, event string) {
		callCount.Add(1)
	})
	watcher.SetDebounce(200 * time.Millisecond)

	skillFile := writeSkillFile(t, workspace, "debounced-skill", "---\nname: debounced-skill\ndescription: test\n---\n\nInitial")
	require.NoError(t, watcher.Start())
	defer watcher.Stop()

	for i := 0; i < 5; i++ {
		require.NoError(t, os.WriteFile(skillFile, []byte("---\nname: debounced-skill\ndescription: test\n---\n\nBody"), 0o644))
		time.Sleep(10 * time.Millisecond)
	}

	require.Eventually(t, func() bool {
		return callCount.Load() == 1
	}, time.Second, 25*time.Millisecond)
	assert.EqualValues(t, 1, callCount.Load())
}
