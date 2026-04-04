# PicoClaw Daemon Mode — Design Spec

**Date:** 2026-04-03
**Status:** Draft
**Scope:** Replace per-message subprocess spawning with a long-running daemon process

## Problem

PicoClaw currently spawns a new process for every Telegram message. Each process:

- Takes ~8 seconds to load config, connect MCP servers, and initialize agents
- Loads session history from disk (cold state, no conversational momentum)
- Dies after responding (all in-memory state lost)
- Makes the bot feel like a new conversation every time ("Hola" on every message)

## Solution

A `--daemon` mode where PicoClaw runs as a single long-running process. The tg_listener communicates with it via JSON-lines over stdin/stdout. Conversation state stays in memory. Responses are instant (no startup overhead).

## Architecture

```
tg_listener.py (long-running)
    |
    | stdin:  JSON-line messages  (message, cancel, shutdown)
    | stdout: JSON-line responses (ready, status, response, error)
    |
PicoClaw daemon (long-running)
    |
    +-- Message routing (by chat_id -> agent/session)
    +-- Session manager (in-memory, flushed to disk after each response)
    +-- LLM provider (Gemma via LM Studio)
    +-- Tools (web_search, exec, spawn, claude_code, etc.)
    +-- Cancel map (per-chat context cancellation)
```

## Component 1: JSON-Lines Protocol

All messages are single JSON objects terminated by a newline. No framing, no length prefixes.

### Input (tg_listener -> PicoClaw stdin)

**message** — User sent a message:
```json
{"type":"message","chat_id":"221899910","user":"Alan","username":"ProDrifterDK","text":"Busca informacion sobre la guerra"}
```

**cancel** — Interrupt the current LLM call for a chat:
```json
{"type":"cancel","chat_id":"221899910"}
```

**shutdown** — Graceful shutdown:
```json
{"type":"shutdown"}
```

### Output (PicoClaw stdout -> tg_listener)

**ready** — Daemon finished initialization:
```json
{"type":"ready"}
```

**status** — Interim update (from message tool or progress):
```json
{"type":"status","chat_id":"221899910","text":"Searching the web..."}
```

**response** — Final answer for a chat:
```json
{"type":"response","chat_id":"221899910","text":"Here are the results..."}
```

**error** — Something went wrong:
```json
{"type":"error","chat_id":"221899910","text":"LLM provider unreachable"}
```

### Field Reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | Event type (message, cancel, shutdown, ready, status, response, error) |
| `chat_id` | string | for message/cancel/status/response; optional for error | Telegram chat ID for routing. Omit for global errors (e.g., startup failures). |
| `user` | string | for message | User's display name |
| `username` | string | for message | User's Telegram username |
| `text` | string | for message/status/response/error | Message content |

## Component 2: PicoClaw --daemon Mode

**File:** `cmd/picoclaw/cmd_agent.go`

A new mode alongside the existing `-m` (one-shot) and interactive (readline) modes.

### Startup

1. Parse `--daemon` flag
2. Load config, create providers, initialize agents, connect MCP servers (same as current)
3. Emit `{"type":"ready"}` on stdout
4. Enter the daemon loop

### Daemon Loop

The stdin reader runs in its own goroutine. Each `message` event spawns a goroutine for processing, so multiple chats can be served concurrently. The stdin reader is never blocked by LLM inference.

```
goroutine: stdin reader
  loop:
    read JSON line from stdin
    switch type:
      "message":
        if chat_id has an in-flight request:
          implicit cancel — call cancelMap[chat_id]()
        create cancellable context, store in cancelMap[chat_id]
        go processChat(ctx, chatID, message)  // goroutine per chat

      "cancel":
        if chat_id in cancelMap:
          call cancel func, remove from cancelMap

      "shutdown":
        cancel all in-flight contexts
        save all sessions to disk
        close MCP servers
        exit 0

goroutine: processChat(ctx, chatID, message)
  construct InboundMessage with channel, chatID, user metadata
  call agentLoop.processMessage(ctx, InboundMessage)
  if ctx was cancelled (context.Canceled):
    discard partial result, do NOT save to session
  else:
    emit {"type":"response"} on stdout
    save session to disk
  remove from cancelMap
```

### Message Routing

The daemon constructs `bus.InboundMessage` directly (not `ProcessDirectWithChannel`) so that:
- User metadata (name, username) is preserved for tools and context
- `ResolveRoute` handles per-chat session key derivation (e.g., `agent:main:telegram:221899910`)
- Multi-chat sessions are properly isolated

### Cancel Map

```go
type daemonState struct {
    mu        sync.Mutex
    cancelMap map[string]context.CancelFunc // chat_id -> cancel
}
```

When a `message` arrives for a chat_id that already has an in-flight request, the daemon cancels the old context before starting the new one.

**Canonical cancellation pattern:** tg_listener simply sends the new `message`. The daemon handles implicit cancel internally. The explicit `cancel` event exists as a safety valve (e.g., `/stop` command) but the normal interrupt flow is just "send new message, daemon cancels the old one."

**Cancellation semantics:**
- Cancelled LLM calls return `context.Canceled` — the daemon discards the partial result
- No partial assistant message is saved to the session
- The cancelMap entry is removed, and the new message is processed immediately

### Output Encoding

All stdout output goes through a single `emitEvent()` function that JSON-marshals and writes with a trailing newline. This replaces all `fmt.Printf` calls that currently write 🦞 lines.

```go
func emitEvent(eventType, chatID, text string) {
    event := map[string]string{
        "type":    eventType,
        "chat_id": chatID,
        "text":    text,
    }
    data, err := json.Marshal(event)
    if err != nil {
        fmt.Fprintln(os.Stderr, "emitEvent marshal error:", err)
        return
    }
    fmt.Println(string(data))
    os.Stdout.Sync()
}
```

### Stdout Contamination Guard

In daemon mode, **only `emitEvent()` may write to stdout.** Any raw `fmt.Printf` or `fmt.Println` to stdout would corrupt the JSON-lines protocol. All existing stdout writes in `cmd_agent.go` (outbound goroutine, heartbeat keepalives, subagent completion, error messages, fallback output) must be either removed or redirected to stderr in daemon mode. The implementation should audit every `os.Stdout` / `fmt.Print` call in `cmd_agent.go` and gate them behind `if !daemonMode`.

### Message Tool Integration

The `message` tool's outbound callback must emit `{"type":"status"}` instead of printing raw text to stdout. In the daemon's setup:

```go
messageTool.SetSendCallback(func(channel, chatID, content string) error {
    emitEvent("status", chatID, content)
    return nil
})
```

This replaces the current outbound goroutine that prints 🦞 lines.

### Subagent Handling

The current one-shot mode has a blocking poll loop that waits for subagents (`HasPendingSubagents`, `WaitForSubagents`, `DrainInbound`). In daemon mode this blocking approach is incompatible with serving multiple chats.

**Approach:** The `processChat` goroutine handles its own subagent lifecycle:
- After the main LLM loop returns, if subagents are pending, the goroutine continues waiting (non-blocking for other chats since each chat has its own goroutine)
- Subagent completion results are drained and emitted as additional `status`/`response` events
- Cancelling a chat also cancels its spawned subagents (the cancel context propagates)
- Progress updates for long-running subagents are emitted as `{"type":"status"}` events

### Stderr

All logging continues to go to stderr (unchanged). The tg_listener already captures stderr for diagnostics.

## Component 3: tg_listener Daemon Manager

**File:** `~/.picoclaw/workspace/tg_listener.py`

Replaces `_picoclaw_adapter()` (subprocess-per-message) with a daemon manager.

### DaemonManager Class

```python
class DaemonManager:
    def __init__(self):
        self.process = None
        self.ready = False
        self.pending = {}         # chat_id -> callback for response
        self.last_crash = 0
        self.backoff = 2          # seconds, doubles on repeated crashes, max 60

    def start(self):
        """Spawn PicoClaw daemon and wait for ready."""

    def send_message(self, chat_id, user, username, text):
        """Write a message JSON line to stdin."""

    def send_cancel(self, chat_id):
        """Write a cancel JSON line to stdin."""

    def _reader_loop(self):
        """Background thread reading stdout JSON lines and dispatching."""

    def _handle_crash(self):
        """Detect broken pipe, notify user, restart with backoff."""
```

### Startup

1. `DaemonManager.start()` spawns: `picoclaw agent --daemon --channel telegram`
2. A reader thread reads stdout line by line
3. On `{"type":"ready"}`: sets `self.ready = True`
4. tg_listener starts accepting Telegram messages

### Message Flow

1. Telegram message arrives
2. `DaemonManager.send_message(chat_id, user, username, text)` writes JSON line to stdin
3. Reader thread receives `status` events -> forwards as Telegram message edits
4. Reader thread receives `response` event -> forwards as final Telegram message

### Interruption

1. Alan sends a new message while PicoClaw is busy on that chat_id
2. tg_listener calls `send_cancel(chat_id)` then `send_message(...)` with the new text
3. PicoClaw cancels the in-flight LLM call, processes the new message

### Crash Recovery

1. Reader thread detects EOF on stdout (process died)
2. Sends all users with pending messages: "I had a brief hiccup, restarting..."
3. Waits `backoff` seconds (starts at 2, doubles on repeated crash, max 60)
4. Calls `start()` to respawn
5. Replays all pending messages (stored in `pending` dict with full payload) in arrival order
6. If next crash is >60s later, resets backoff to 2

### Session Slash Commands

The tg_listener slash commands (`/new`, `/trim`, `/condense`) currently modify session files directly. In daemon mode, these must be sent as protocol messages instead of direct file writes, since the daemon owns the in-memory session state.

The simplest approach: tg_listener sends these as regular `message` events with the command text. The daemon's agent processes them as user messages, and the agent can call tools to manage its own session (e.g., clear history). No new protocol events needed.

### Engine Switching

The `/engine` command continues to work at the tg_listener level. When engine is `picoclaw`, messages go through the `DaemonManager`. When engine is `claude`, messages go through `_claude_adapter` (subprocess-per-message, unchanged). Switching to `claude` does not stop the daemon — it stays warm for when the user switches back.

## Component 4: Session Persistence

Sessions stay in memory for the daemon's lifetime. This is the key improvement — no more cold-loading from disk on every message.

**Flush to disk:**
- After each completed response (existing `session.Save()` call in `runAgentLoop`)
- On graceful shutdown (`shutdown` event)

**On crash restart:**
- Sessions load from the last flush (existing `loadSessions()` in SessionManager)
- At most one in-flight response is lost (which never completed anyway)

## Component 5: Migration from Per-Message Mode

### What Changes

| Component | Before | After |
|-----------|--------|-------|
| tg_listener | Spawns `picoclaw agent -m "text"` per message | Manages one `picoclaw agent --daemon` process |
| PicoClaw stdout | Raw 🦞 text lines | JSON-line events |
| Session loading | From disk per message (~8s) | In memory (instant) |
| Conversation feel | Fresh each time | Continuous |
| Message tool output | Printed via outbound goroutine | Emitted as `{"type":"status"}` |
| Crash recovery | N/A (stateless) | Auto-restart with backoff + user notification |
| Interrupt | Not possible | `cancel` event |

### What Stays the Same

- Config file format and location
- AGENTS.md and workspace layout
- Session file format on disk
- All tool implementations (web_search, exec, spawn, claude_code, etc.)
- Agent routing and session key logic
- MCP server connections
- The `-m` one-shot mode (kept for backward compatibility and scripting)
- Interactive readline mode (kept for local development)

### Backward Compatibility

The `-m` and interactive modes remain unchanged. `--daemon` is purely additive. The tg_listener can be switched between modes via a config flag if needed.

## Files to Create/Modify

| File | Action | Description |
|------|--------|-------------|
| `cmd/picoclaw/cmd_agent.go` | Modify | Add `--daemon` flag, daemon loop, emitEvent, cancel map |
| `cmd/picoclaw/daemon.go` | Create | Daemon-specific logic (stdin reader, event emitter, cancel state) |
| `~/.picoclaw/workspace/tg_listener.py` | Modify | Replace _picoclaw_adapter with DaemonManager |
| `pkg/agent/loop.go` | Minor modify | Expose `processMessage` for daemon use (or add a wrapper). Ensure `runAgentLoop` returns `context.Canceled` cleanly without saving partial state. |
