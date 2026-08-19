package session

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Index is a SQLite FTS5 index over persisted session messages, enabling
// full-text search across past sessions. It is a derived store: the JSON
// session files remain the source of truth and the index is rebuilt from
// them whenever a session is saved or is found stale at startup.
type Index struct {
	db *sql.DB
	mu sync.Mutex
}

// SearchHit is one FTS5 match: the message that matched, ranked by bm25.
type SearchHit struct {
	SessionKey string
	MsgIdx     int
	Role       string
	Snippet    string
	Updated    time.Time
}

// SessionMeta is the browse-view summary of one indexed session.
type SessionMeta struct {
	Key          string
	Updated      time.Time
	MessageCount int
	Preview      string // first user message, truncated
}

// IndexedMessage is one message row returned by ReadSession/Window.
type IndexedMessage struct {
	Idx     int
	Role    string
	Content string
}

// OpenIndex opens (or creates) the index at <dir>/index.db.
// Returns (nil, nil) when dir is empty, meaning indexing is disabled.
func OpenIndex(dir string) (*Index, error) {
	if dir == "" {
		return nil, nil
	}
	dsn := fmt.Sprintf("file:%s/index.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", dir)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: all access is serialized through ix.mu anyway, and this
	// avoids SQLITE_BUSY between the writer (Save) and readers (search tool).
	db.SetMaxOpenConns(1)

	schema := `
CREATE TABLE IF NOT EXISTS sessions_meta (
	session_key TEXT PRIMARY KEY,
	updated INTEGER NOT NULL,
	message_count INTEGER NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
	session_key UNINDEXED,
	msg_idx UNINDEXED,
	role,
	content
);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Index{db: db}, nil
}

// Close closes the underlying database.
func (ix *Index) Close() error {
	if ix == nil {
		return nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.db.Close()
}

// IndexSession replaces the indexed content of one session. Safe to call
// after every Save; it is a delete + reinsert of that session's rows.
func (ix *Index) IndexSession(s *Session) error {
	if ix == nil {
		return nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()

	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM messages_fts WHERE session_key = ?`, s.Key); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO messages_fts (session_key, msg_idx, role, content) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, msg := range s.Messages {
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		if _, err := stmt.Exec(s.Key, i, msg.Role, msg.Content); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO sessions_meta (session_key, updated, message_count) VALUES (?, ?, ?)
		 ON CONFLICT(session_key) DO UPDATE SET updated = excluded.updated, message_count = excluded.message_count`,
		s.Key, s.Updated.Unix(), len(s.Messages)); err != nil {
		return err
	}
	return tx.Commit()
}

// NeedsReindex reports whether the stored metadata for key differs from the
// given session, i.e. the file on disk is newer than the index.
func (ix *Index) NeedsReindex(s *Session) bool {
	if ix == nil {
		return false
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()

	var updated int64
	var count int
	err := ix.db.QueryRow(`SELECT updated, message_count FROM sessions_meta WHERE session_key = ?`, s.Key).
		Scan(&updated, &count)
	if err == sql.ErrNoRows {
		return len(s.Messages) > 0
	}
	if err != nil {
		return true
	}
	return updated != s.Updated.Unix() || count != len(s.Messages)
}

// escapeFTSQuery turns free text into a safe FTS5 query: each term is quoted
// and terms are OR-joined for recall; bm25 handles the ranking.
func escapeFTSQuery(query string) string {
	terms := strings.Fields(query)
	for i, t := range terms {
		terms[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(terms, " OR ")
}

// Search runs FTS5 over all sessions and returns up to limit hits ranked by
// bm25 (best first).
func (ix *Index) Search(query string, limit int) ([]SearchHit, error) {
	if ix == nil {
		return nil, fmt.Errorf("session index is not available")
	}
	q := escapeFTSQuery(query)
	if q == "" {
		return nil, fmt.Errorf("query is empty")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()

	rows, err := ix.db.Query(`
		SELECT m.session_key, m.msg_idx, m.role,
		       snippet(messages_fts, 3, '[', ']', '…', 32) AS snip,
		       bm25(messages_fts) AS rank, s.updated
		FROM messages_fts m
		JOIN sessions_meta s ON s.session_key = m.session_key
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var h SearchHit
		var updated int64
		var rank float64
		if err := rows.Scan(&h.SessionKey, &h.MsgIdx, &h.Role, &h.Snippet, &rank, &updated); err != nil {
			return nil, err
		}
		h.Updated = time.Unix(updated, 0)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// Recent returns the most recently updated indexed sessions, with the first
// user message of each as a preview.
func (ix *Index) Recent(limit int) ([]SessionMeta, error) {
	if ix == nil {
		return nil, fmt.Errorf("session index is not available")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()

	rows, err := ix.db.Query(`
		SELECT session_key, updated, message_count
		FROM sessions_meta
		WHERE message_count > 0
		ORDER BY updated DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metas []SessionMeta
	for rows.Next() {
		var m SessionMeta
		var updated int64
		if err := rows.Scan(&m.Key, &updated, &m.MessageCount); err != nil {
			return nil, err
		}
		m.Updated = time.Unix(updated, 0)
		metas = append(metas, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range metas {
		var preview string
		err := ix.db.QueryRow(`
			SELECT content FROM messages_fts
			WHERE session_key = ? AND role = 'user'
			ORDER BY msg_idx LIMIT 1`, metas[i].Key).Scan(&preview)
		if err == nil {
			metas[i].Preview = truncate(preview, 120)
		}
	}
	return metas, nil
}

// ReadSession returns a bounded head+tail view of one session plus its total
// indexed message count.
func (ix *Index) ReadSession(key string, head, tail int) ([]IndexedMessage, int, error) {
	if ix == nil {
		return nil, 0, fmt.Errorf("session index is not available")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()

	var total int
	if err := ix.db.QueryRow(
		`SELECT COUNT(*) FROM messages_fts WHERE session_key = ?`, key).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, fmt.Errorf("session not found in index: %s", key)
	}

	// msg_idx has gaps (empty messages are not indexed), so head/tail are
	// taken by position, not by index arithmetic.
	if total <= head+tail {
		msgs, err := ix.queryMessages(key, `ORDER BY msg_idx LIMIT ?`, total)
		return msgs, total, err
	}
	headMsgs, err := ix.queryMessages(key, `ORDER BY msg_idx LIMIT ?`, head)
	tailMsgs, err := ix.queryMessages(key, `ORDER BY msg_idx DESC LIMIT ?`, tail)
	if err != nil {
		return nil, 0, err
	}
	for i, j := 0, len(tailMsgs)-1; i < j; i, j = i+1, j-1 {
		tailMsgs[i], tailMsgs[j] = tailMsgs[j], tailMsgs[i]
	}
	return append(headMsgs, tailMsgs...), total, nil
}

func (ix *Index) queryMessages(key, order string, limit int) ([]IndexedMessage, error) {
	rows, err := ix.db.Query(
		`SELECT msg_idx, role, content FROM messages_fts WHERE session_key = ? `+order,
		key, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []IndexedMessage
	for rows.Next() {
		var m IndexedMessage
		if err := rows.Scan(&m.Idx, &m.Role, &m.Content); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// Window returns the messages with msg_idx in [center-radius, center+radius].
func (ix *Index) Window(key string, center, radius int) ([]IndexedMessage, error) {
	if ix == nil {
		return nil, fmt.Errorf("session index is not available")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	lo := center - radius
	if lo < 0 {
		lo = 0
	}
	return ix.windowLocked(key, lo, center+radius)
}

func (ix *Index) windowLocked(key string, lo, hi int) ([]IndexedMessage, error) {
	rows, err := ix.db.Query(`
		SELECT msg_idx, role, content FROM messages_fts
		WHERE session_key = ? AND msg_idx BETWEEN ? AND ?
		ORDER BY msg_idx`, key, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []IndexedMessage
	for rows.Next() {
		var m IndexedMessage
		if err := rows.Scan(&m.Idx, &m.Role, &m.Content); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func truncate(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
