package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func makeSession(key string, contents ...string) *Session {
	msgs := make([]providers.Message, len(contents))
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs[i] = providers.Message{Role: role, Content: c}
	}
	return &Session{Key: key, Messages: msgs, Created: time.Now(), Updated: time.Now()}
}

func TestIndexSearchAndRead(t *testing.T) {
	ix, err := OpenIndex(t.TempDir())
	require.NoError(t, err)
	defer ix.Close()

	require.NoError(t, ix.IndexSession(makeSession("telegram_111",
		"¿cómo configuro el webhook de stripe?",
		"usa stripe listen --forward-to localhost:8080",
		"gracias, funcionó")))
	require.NoError(t, ix.IndexSession(makeSession("telegram_222",
		"hoy cocinamos risotto de champiñones",
		"suena bien")))

	t.Run("search finds the right session", func(t *testing.T) {
		hits, err := ix.Search("stripe webhook", 5)
		require.NoError(t, err)
		require.NotEmpty(t, hits)
		assert.Equal(t, "telegram_111", hits[0].SessionKey)
	})

	t.Run("search with no matches returns empty", func(t *testing.T) {
		hits, err := ix.Search("kubernetes cluster", 5)
		require.NoError(t, err)
		assert.Empty(t, hits)
	})

	t.Run("search survives FTS5 special characters", func(t *testing.T) {
		_, err := ix.Search(`"quoted" OR (unbalanced`, 5)
		assert.NoError(t, err)
	})

	t.Run("reindex replaces old content", func(t *testing.T) {
		s := makeSession("telegram_111", "tema completamente distinto sobre jardinería")
		s.Updated = time.Now()
		require.NoError(t, ix.IndexSession(s))

		hits, err := ix.Search("stripe", 5)
		require.NoError(t, err)
		for _, h := range hits {
			assert.NotEqual(t, "telegram_111", h.SessionKey)
		}
	})

	t.Run("recent lists newest first with preview", func(t *testing.T) {
		metas, err := ix.Recent(10)
		require.NoError(t, err)
		require.Len(t, metas, 2)
		assert.Equal(t, "telegram_111", metas[0].Key) // reindexed most recently
		assert.Contains(t, metas[1].Preview, "risotto")
	})

	t.Run("read returns head and tail for long sessions", func(t *testing.T) {
		var contents []string
		for i := 0; i < 50; i++ {
			contents = append(contents, "mensaje número largo para rellenar la sesión")
		}
		require.NoError(t, ix.IndexSession(makeSession("long", contents...)))

		msgs, total, err := ix.ReadSession("long", 5, 3)
		require.NoError(t, err)
		assert.Equal(t, 50, total)
		require.Len(t, msgs, 8)
		assert.Equal(t, 0, msgs[0].Idx)
		assert.Equal(t, 49, msgs[7].Idx)
	})

	t.Run("read unknown session errors", func(t *testing.T) {
		_, _, err := ix.ReadSession("nope", 5, 5)
		assert.Error(t, err)
	})

	t.Run("window returns neighbors around center", func(t *testing.T) {
		msgs, err := ix.Window("long", 25, 2)
		require.NoError(t, err)
		require.Len(t, msgs, 5)
		assert.Equal(t, 23, msgs[0].Idx)
		assert.Equal(t, 27, msgs[4].Idx)
	})

	t.Run("empty content messages are skipped", func(t *testing.T) {
		s := makeSession("gaps", "primero", "", "tercero")
		require.NoError(t, ix.IndexSession(s))
		msgs, total, err := ix.ReadSession("gaps", 10, 10)
		require.NoError(t, err)
		assert.Equal(t, 2, total)
		assert.Equal(t, []int{0, 2}, []int{msgs[0].Idx, msgs[1].Idx})
	})
}

func TestManagerBackfillsIndex(t *testing.T) {
	dir := t.TempDir()

	// First manager: write a session to disk.
	sm1 := NewSessionManager(dir)
	s := sm1.GetOrCreate("telegram_999")
	s.Messages = append(s.Messages, providers.Message{Role: "user", Content: "receta de humita"})
	require.NoError(t, sm1.Save("telegram_999"))

	// The index should already be in sync from Save.
	hits, err := sm1.Index().Search("humita", 5)
	require.NoError(t, err)
	require.NotEmpty(t, hits)

	// Simulate a lost index: remove the DB and recreate the manager,
	// which must backfill from the JSON files on disk.
	require.NoError(t, sm1.Index().Close())
	for _, suffix := range []string{"index.db", "index.db-wal", "index.db-shm"} {
		_ = os.Remove(filepath.Join(dir, suffix))
	}
	sm2 := NewSessionManager(dir)
	hits, err = sm2.Index().Search("humita", 5)
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	assert.Equal(t, "telegram_999", hits[0].SessionKey)
}
