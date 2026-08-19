package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/session"
)

const (
	sessionSearchDefaultLimit = 5
	sessionSearchMaxLimit     = 20
	sessionSearchWindowRadius = 5
	sessionSearchHead         = 20
	sessionSearchTail         = 10
	sessionSearchMaxMsgChars  = 800
	sessionSearchMaxOutChars  = 12000
)

// SessionSearchIndex is the read side of the session FTS5 index.
// Implemented by *session.Index; defined here so the tool stays testable.
type SessionSearchIndex interface {
	Search(query string, limit int) ([]session.SearchHit, error)
	Recent(limit int) ([]session.SessionMeta, error)
	ReadSession(key string, head, tail int) ([]session.IndexedMessage, int, error)
	Window(key string, center, radius int) ([]session.IndexedMessage, error)
}

// SessionSearchTool searches past conversation sessions. Four modes, inferred
// from args (mirrors Hermes' session_search):
//
//  1. query                    → full-text search, best hit hydrated with context
//  2. session_id + around      → window of messages around a message index
//  3. session_id               → bounded head/tail read of one session
//  4. (no args)                → browse most recent sessions
type SessionSearchTool struct {
	index SessionSearchIndex
}

// NewSessionSearchTool creates the tool backed by the given index.
func NewSessionSearchTool(index SessionSearchIndex) *SessionSearchTool {
	return &SessionSearchTool{index: index}
}

func (t *SessionSearchTool) Name() string { return "session_search" }

func (t *SessionSearchTool) Description() string {
	return "Search past conversation sessions to recover context. " +
		"Pass 'query' for full-text search over all sessions (results include surrounding messages). " +
		"Pass 'session_id' to read one session, optionally with 'around' (a message index from a previous result) " +
		"to see a window of messages around it. " +
		"Call with no arguments to list recent sessions."
}

func (t *SessionSearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Natural language or keywords to search for in past sessions.",
			},
			"session_id": map[string]any{
				"type":        "string",
				"description": "Session key to read (from search results or browse).",
			},
			"around": map[string]any{
				"type":        "integer",
				"description": "Message index to center a context window on. Requires session_id.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max results for search/browse (default 5, max 20).",
				"default":     sessionSearchDefaultLimit,
				"maximum":     sessionSearchMaxLimit,
			},
		},
	}
}

func (t *SessionSearchTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t.index == nil {
		return ErrorResult("session search is not available (index disabled or failed to open)")
	}

	query, _ := args["query"].(string)
	sessionID, _ := args["session_id"].(string)
	limit := intArg(args, "limit", sessionSearchDefaultLimit, sessionSearchMaxLimit)

	switch {
	case strings.TrimSpace(query) != "":
		return t.discover(query, limit)
	case sessionID != "":
		if around, ok := intArgOK(args, "around"); ok {
			return t.scroll(sessionID, around)
		}
		return t.read(sessionID)
	default:
		return t.browse(limit)
	}
}

func (t *SessionSearchTool) discover(query string, limit int) *ToolResult {
	hits, err := t.index.Search(query, limit)
	if err != nil {
		return ErrorResult(fmt.Sprintf("session search failed: %v", err))
	}
	if len(hits) == 0 {
		return SilentResult(fmt.Sprintf("No past sessions match %q.", query))
	}

	// Deduplicate by session, keeping the best-ranked hit per session.
	seen := map[string]bool{}
	var unique []session.SearchHit
	for _, h := range hits {
		if seen[h.SessionKey] {
			continue
		}
		seen[h.SessionKey] = true
		unique = append(unique, h)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found matches in %d session(s) for %q:\n", len(unique), query)
	for i, h := range unique {
		fmt.Fprintf(&sb, "\n### %d. %s (updated %s)\n", i+1, h.SessionKey, h.Updated.Format("2006-01-02 15:04"))
		if i == 0 {
			// Hydrate the best hit with its surrounding context.
			msgs, err := t.index.Window(h.SessionKey, h.MsgIdx, sessionSearchWindowRadius)
			if err == nil && len(msgs) > 0 {
				for _, m := range msgs {
					marker := "  "
					if m.Idx == h.MsgIdx {
						marker = "> "
					}
					fmt.Fprintf(&sb, "%s[%s #%d] %s\n", marker, m.Role, m.Idx, truncateSessionMsg(m.Content))
				}
			} else {
				fmt.Fprintf(&sb, "> [%s #%d] %s\n", h.Role, h.MsgIdx, h.Snippet)
			}
		} else {
			fmt.Fprintf(&sb, "  match [%s #%d]: %s\n", h.Role, h.MsgIdx, h.Snippet)
		}
	}
	sb.WriteString("\nUse session_search with session_id to read more, or session_id + around to scroll around a message.")
	return SilentResult(capSessionOutput(sb.String()))
}

func (t *SessionSearchTool) scroll(sessionID string, around int) *ToolResult {
	msgs, err := t.index.Window(sessionID, around, sessionSearchWindowRadius)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read window: %v", err))
	}
	if len(msgs) == 0 {
		return SilentResult(fmt.Sprintf("No messages found around #%d in session %s.", around, sessionID))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Session %s, messages around #%d:\n\n", sessionID, around)
	for _, m := range msgs {
		fmt.Fprintf(&sb, "[%s #%d] %s\n\n", m.Role, m.Idx, truncateSessionMsg(m.Content))
	}
	fmt.Fprintf(&sb, "To scroll, call again with around=%d (older) or around=%d (newer).",
		msgs[0].Idx-sessionSearchWindowRadius, msgs[len(msgs)-1].Idx+sessionSearchWindowRadius)
	return SilentResult(capSessionOutput(sb.String()))
}

func (t *SessionSearchTool) read(sessionID string) *ToolResult {
	msgs, total, err := t.index.ReadSession(sessionID, sessionSearchHead, sessionSearchTail)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read session: %v", err))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Session %s (%d indexed messages", sessionID, total)
	if total > len(msgs) {
		fmt.Fprintf(&sb, ", showing first %d + last %d", sessionSearchHead, sessionSearchTail)
	}
	sb.WriteString("):\n\n")
	prevIdx := -1
	for _, m := range msgs {
		if prevIdx >= 0 && m.Idx > prevIdx+1 {
			fmt.Fprintf(&sb, "... [%d messages omitted, use around=%d to read them] ...\n\n", m.Idx-prevIdx-1, prevIdx+1+(m.Idx-prevIdx-1)/2)
		}
		fmt.Fprintf(&sb, "[%s #%d] %s\n\n", m.Role, m.Idx, truncateSessionMsg(m.Content))
		prevIdx = m.Idx
	}
	return SilentResult(capSessionOutput(sb.String()))
}

func (t *SessionSearchTool) browse(limit int) *ToolResult {
	metas, err := t.index.Recent(limit)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to list sessions: %v", err))
	}
	if len(metas) == 0 {
		return SilentResult("No past sessions found.")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d most recent sessions:\n", len(metas))
	for _, m := range metas {
		fmt.Fprintf(&sb, "\n- %s (%s, %d msgs)\n  %s\n",
			m.Key, m.Updated.Format("2006-01-02 15:04"), m.MessageCount, m.Preview)
	}
	sb.WriteString("\nUse session_search with session_id to read one.")
	return SilentResult(capSessionOutput(sb.String()))
}

func intArg(args map[string]any, name string, def, max int) int {
	v, ok := intArgOK(args, name)
	if !ok {
		return def
	}
	if v < 1 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func intArgOK(args map[string]any, name string) (int, bool) {
	f, ok := args[name].(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func truncateSessionMsg(s string) string {
	if len(s) <= sessionSearchMaxMsgChars {
		return s
	}
	return s[:sessionSearchMaxMsgChars] + fmt.Sprintf("… [%d more chars]", len(s)-sessionSearchMaxMsgChars)
}

func capSessionOutput(s string) string {
	if len(s) <= sessionSearchMaxOutChars {
		return s
	}
	return s[:sessionSearchMaxOutChars] + "\n… [output truncated; narrow the query or use around= to scroll]"
}
