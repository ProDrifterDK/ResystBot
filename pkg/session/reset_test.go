package session

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedResetSession(t *testing.T, sm *SessionManager, key, summary string) {
	t.Helper()
	sm.AddMessage(key, "user", "old searchable reset token")
	sm.AddMessage(key, "assistant", "old reply")
	sm.AddMessage(key, "tool", "old tool result")
	sm.SetSummary(key, summary)
	require.NoError(t, sm.Save(key))
}

func closeManagerIndex(t *testing.T, sm *SessionManager) {
	t.Helper()
	if sm.Index() != nil {
		require.NoError(t, sm.Index().Close())
	}
}

func TestSessionManagerResetSoftAndHard(t *testing.T) {
	for _, tc := range []struct {
		name         string
		clearSummary bool
		wantSummary  string
	}{
		{name: "soft", clearSummary: false, wantSummary: "  exact summary\nbytes  "},
		{name: "hard", clearSummary: true, wantSummary: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			key := "telegram:reset-" + tc.name
			sm := NewSessionManager(dir)
			seedResetSession(t, sm, key, "  exact summary\nbytes  ")

			hits, err := sm.Index().Search("searchable", 5)
			require.NoError(t, err)
			require.NotEmpty(t, hits)

			cleared, err := sm.Reset(key, tc.clearSummary)
			require.NoError(t, err)
			assert.Equal(t, 3, cleared)
			assert.Empty(t, sm.GetHistory(key))
			assert.Equal(t, tc.wantSummary, sm.GetSummary(key))

			hits, err = sm.Index().Search("searchable", 5)
			require.NoError(t, err)
			assert.Empty(t, hits)
			closeManagerIndex(t, sm)

			reloaded := NewSessionManager(dir)
			defer closeManagerIndex(t, reloaded)
			assert.Empty(t, reloaded.GetHistory(key))
			assert.Equal(t, tc.wantSummary, reloaded.GetSummary(key))

			cleared, err = reloaded.Reset(key, tc.clearSummary)
			require.NoError(t, err)
			assert.Zero(t, cleared)
		})
	}
}

func TestSessionManagerResetPersistenceFailureKeepsMemory(t *testing.T) {
	sm := NewSessionManager("")
	key := "telegram:persist-failure"
	seedResetSession(t, sm, key, "keep summary")

	sm.storage = filepath.Join(t.TempDir(), "missing", "sessions")
	cleared, err := sm.Reset(key, true)
	require.Error(t, err)
	assert.Zero(t, cleared)
	assert.Len(t, sm.GetHistory(key), 3)
	assert.Equal(t, "keep summary", sm.GetSummary(key))
}

func TestSessionManagerResetStrictIndexFailure(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManager(dir)
	key := "telegram:index-failure"
	seedResetSession(t, sm, key, "summary")
	require.NoError(t, sm.Index().Close())

	cleared, err := sm.Reset(key, false)
	require.Error(t, err)
	assert.Zero(t, cleared, "a failed strict index reconciliation must not report success")
	assert.Empty(t, sm.GetHistory(key), "JSON and memory commit before strict index reconciliation")
	assert.Equal(t, "summary", sm.GetSummary(key))
}

func TestSessionManagerResetConcurrentMutationHasTotalOrder(t *testing.T) {
	for i := 0; i < 100; i++ {
		sm := NewSessionManager("")
		key := "telegram:concurrent"
		sm.AddMessage(key, "user", "before")

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var cleared int
		var resetErr error
		go func() {
			defer wg.Done()
			<-start
			cleared, resetErr = sm.Reset(key, false)
		}()
		go func() {
			defer wg.Done()
			<-start
			sm.AddMessage(key, "assistant", "concurrent")
		}()
		close(start)
		wg.Wait()
		require.NoError(t, resetErr)

		history := sm.GetHistory(key)
		switch cleared {
		case 1:
			require.Len(t, history, 1)
			assert.Equal(t, "concurrent", history[0].Content)
		case 2:
			assert.Empty(t, history)
		default:
			t.Fatalf("cleared = %d, want a valid total order count of 1 or 2", cleared)
		}
	}
}
