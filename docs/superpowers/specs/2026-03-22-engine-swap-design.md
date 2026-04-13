# Engine Swap: Configurable AI Backend for tg_listener.py

## Summary

Make `tg_listener.py` engine-agnostic so it can dispatch Telegram messages to either PicoClaw or Claude Code CLI as the AI backend. Switchable live from Telegram via `/engine` command. Both engines share the same Resyst persona and memory system.

## Motivation

PicoClaw and Claude Code CLI are both invoked as subprocesses by `tg_listener.py`. Claude Code offers superior tool calling, larger context, and access to Anthropic's latest models via Alan's Max subscription. PicoClaw offers local LLM support and custom agent orchestration. Being able to switch between them per-message without restarting the service gives maximum flexibility.

## Architecture

### Engine Config File

**Path:** `~/.picoclaw/engine.json`

```json
{
  "active": "picoclaw",
  "claude": {
    "model": "sonnet",
    "effort": "high",
    "permission_mode": "dangerously-skip-permissions",
    "workspace": "/home/prodrifterdk/.picoclaw/workspace"
  }
}
```

- Read per-message (no restart needed to switch)
- Default: `picoclaw` if file missing or corrupted
- `claude.model`: one of `sonnet`, `opus`, `haiku`
- `claude.effort`: one of `low`, `medium`, `high`, `max`

### Telegram Commands

| Command | Action |
|---------|--------|
| `/engine` | Show current engine, model, and effort |
| `/engine claude` | Switch to Claude Code (keeps current model/effort) |
| `/engine picoclaw` | Switch to PicoClaw |
| `/engine claude opus max` | Switch to Claude with Opus at max effort |
| `/engine claude sonnet low` | Switch to Claude with Sonnet at low effort |
| `/engine claude haiku` | Switch to Claude with Haiku, keeps current effort |

Parsing: `/engine [engine] [model] [effort]` — positional, all optional after engine name.

Validation:
- Engine must be `claude` or `picoclaw` — reject others with valid options list
- Model must be `sonnet`, `opus`, or `haiku` — reject others
- Effort must be `low`, `medium`, `high`, or `max` — reject others

Status message format:
```
🔧 Engine: claude
   Model: opus | Effort: max
```

### Engine Abstraction

Current call in `handle_message()`:
```python
for event, content in ask_picoclaw_streaming(ai_input, chat_id):
```

Becomes:
```python
for event, content in ask_engine_streaming(ai_input, chat_id):
```

`ask_engine_streaming()` reads `engine.json`, then dispatches to:
- `_picoclaw_adapter(text, chat_id)` — existing `ask_picoclaw_streaming()` logic, unchanged
- `_claude_adapter(text, chat_id, model, effort)` — new, parses `stream-json` output

Both yield identical `(event, content)` tuples:
- `block_start` — new message block started
- `block_update` — more content arrived for current block
- `block_done` — block complete
- `error` — unrecoverable error

`handle_message()` stays completely untouched beyond the one-line swap. The `/condense` handler also uses `ask_engine_streaming()`.

### Claude Adapter — Subprocess

Command:
```bash
claude -p \
  --output-format stream-json \
  --include-partial-messages \
  --model {model} \
  --effort {effort} \
  --dangerously-skip-permissions \
  --system-prompt "{agents_md_content + override_prompt}" \
  --add-dir /home/prodrifterdk/.picoclaw/workspace \
  --no-session-persistence \
  --max-budget-usd 5 \
  "{message_text}"
```

Flags rationale:
- `-p`: Print mode (non-interactive, exits after response).
- `--output-format stream-json`: Structured JSON lines on stdout.
- `--include-partial-messages`: Enables real-time streaming of partial text deltas. Without this, response arrives as one dump at the end — no live Telegram editing.
- `--system-prompt`: Injects AGENTS.md content + engine overrides as one combined prompt. Read from Python at invocation time. (Note: `--bare` is NOT used because it blocks OAuth authentication, which Alan's Max subscription requires.)
- `--add-dir`: Gives Claude access to workspace (memory, mind, logs).
- `--dangerously-skip-permissions`: No interactive prompts (headless). Required since the process runs as a daemon with no TTY.
- `--no-session-persistence`: Uses PicoClaw's session/condense system, not Claude's.
- `--max-budget-usd 5`: Safety net to cap per-message cost (prevents runaway tool loops).

### Claude Adapter — Output Parsing

With `--include-partial-messages`, each stdout line is a JSON object. The key event types:

| JSON `type` | `event.type` | Action |
|-------------|-------------|--------|
| `stream_event` | `content_block_start` (text type) | Begin accumulating text. Yield `block_start`. |
| `stream_event` | `content_block_delta` | Extract `event.delta.text`, append to buffer. Yield `block_update`. |
| `stream_event` | `content_block_stop` | Yield `block_done` with accumulated text. Reset buffer. |
| `stream_event` | `content_block_start` (tool_use type) | Tool invocation starting — yield `block_update` with "[Using tools...]" status. |
| `stream_event` | `content_block_delta` (tool input) | Ignore (internal tool arguments). |
| `stream_event` | `content_block_stop` (tool) | Ignore. |
| `assistant` | N/A | Ignore (redundant summary of content blocks). |
| `result` | N/A | If `is_error`, yield `error`. Otherwise final confirmation — ignore (text already yielded via stream events). |
| `system` | N/A | Ignore (init, hooks). |
| `rate_limit_event` | N/A | Ignore. |

**Multi-turn tool use:** Claude may produce multiple text content blocks across tool-use turns (text → tool_use → tool_result → text → ...). Each text block gets its own `block_start`/`block_done` cycle, which maps to separate Telegram messages — same behavior as PicoClaw's `🦞`-delimited multi-block output.

No `🦞` marker parsing needed — response text comes cleanly structured in JSON.

### Claude Adapter — Threading & Watchdog

Same pattern as picoclaw adapter:
- **Reader thread**: Reads stdout line-by-line into a queue
- **Stderr drain thread**: Prevents buffer deadlock
- **Watchdog thread**: Kills process if silent >1 hour
- **Deadline extension**: 4 hours on first yielded content (for long tool-use sessions)
- **Block boundaries**: Driven by `content_block_start`/`content_block_stop` events (not silence-based like picoclaw adapter)

### Persona Injection

AGENTS.md content is read from Python at invocation time and combined with the override text into a single `--system-prompt` string. This avoids the undocumented `--system-prompt-file` flag.

The override appended after AGENTS.md content:
```
You do NOT need to start responses with 🦞 — the delivery system handles this automatically.

Your persistent memory is at ~/.picoclaw/workspace/memory/MEMORY.md — read it when you need context about the user, projects, or past decisions.

When asked to remember something, save it to ~/.picoclaw/workspace/memory/ following the existing structure. Update ~/.picoclaw/workspace/memory/MEMORY.md as the index. Use the same format as existing memory files there.
```

This way:
- AGENTS.md stays untouched for PicoClaw compatibility
- Claude doesn't waste tokens on `🦞` prefix (adapter prepends it to `block_start` for Telegram branding)
- Memory reads/writes go to the shared location

### Memory & Context

Both engines read and write to `~/.picoclaw/workspace/memory/`. No split brain.

- Claude gets access via `--add-dir`
- PicoClaw reads it natively
- The `/condense` summary in `sessions/agent_main_main.json` is written by `tg_listener.py` (not the engine), so it persists across engine switches
- Session history is NOT shared between engines (per design decision — condense summary is the bridge)

## File Changes

**Single file modified:** `~/.picoclaw/workspace/tg_listener.py`

**New file created:** `~/.picoclaw/engine.json` (created on first `/engine` command or with defaults)

### New Code

| Function | Purpose |
|----------|---------|
| `ENGINE_CONFIG_PATH` | Constant: `~/.picoclaw/engine.json` |
| `DEFAULT_ENGINE_CONFIG` | Dict with default values |
| `load_engine_config()` | Read engine.json, return defaults if missing/corrupt |
| `save_engine_config(config)` | Write engine.json atomically (write-to-temp + `os.rename`) |
| `_claude_adapter(text, chat_id, model, effort)` | Subprocess + stream-json parser, yields `(event, content)` |
| `ask_engine_streaming(text, chat_id)` | Read config, dispatch to appropriate adapter |

### Renamed

| Old | New | Notes |
|-----|-----|-------|
| `ask_picoclaw_streaming()` | `_picoclaw_adapter()` | Internal function, same logic |

### Modified

| Function | Change |
|----------|--------|
| `handle_message()` | Add `/engine` command handler; replace `ask_picoclaw_streaming` call → `ask_engine_streaming` |
| `/condense` handler (inside `handle_message`) | Replace `ask_picoclaw_streaming` call → `ask_engine_streaming` |

### Not Touched

- All Telegram API functions (send, edit, escape, markup)
- Watchdog/threading pattern (reused in claude adapter)
- Session management (`/new`, `/trim`, `/session`)
- `/status`, `/model`, `/help`, `/log`, `/md`
- AGENTS.md, config.json, systemd service
- PicoClaw Go source code

## Edge Cases

- **engine.json missing**: Use defaults (picoclaw, sonnet, high)
- **engine.json corrupted**: Log warning, use defaults
- **Claude Code not installed**: `_claude_adapter` catches `FileNotFoundError`, yields `('error', "Claude Code CLI not found")`
- **Claude auth expired**: Catch in stderr, yield error message
- **Rate limit hit**: Claude's stream-json includes rate limit events — log but don't surface unless the request fails
- **Empty response from Claude**: Same fallback as picoclaw — "He procesado tu mensaje..."
- **Guest users with Claude engine**: Pass `--disallowedTools "Bash,Edit,Write,NotebookEdit"` to enforce tool restrictions at the CLI level (not just natural language prompt)

## Testing Plan

1. Verify `/engine` command shows status, switches engines, persists config
2. Send a message with picoclaw engine — verify normal flow unchanged
3. Switch to claude — send a message — verify response arrives in Telegram
4. Test `/engine claude opus max` — verify model and effort are passed correctly
5. Test `/condense` with claude engine — verify summary is saved to session file
6. Test engine.json missing/corrupt — verify graceful fallback to picoclaw
7. Test with claude not installed — verify error message in Telegram
8. Kill-switch: `/engine picoclaw` must always work to revert
