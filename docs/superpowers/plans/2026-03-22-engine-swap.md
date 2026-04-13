# Engine Swap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make tg_listener.py dispatch Telegram messages to either PicoClaw or Claude Code CLI, switchable live via `/engine` command.

**Architecture:** Add an engine abstraction layer to tg_listener.py. A dispatcher function reads `~/.picoclaw/engine.json` per-message and routes to either the existing PicoClaw subprocess logic or a new Claude Code CLI adapter. Both adapters yield identical `(event, content)` tuples consumed by the unchanged `handle_message()`.

**Tech Stack:** Python 3 (stdlib only — subprocess, json, threading, queue, os, tempfile)

**Spec:** `docs/superpowers/specs/2026-03-22-engine-swap-design.md`

---

### Task 1: Engine Config — load/save/defaults

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py:13-20` (add constants after existing constants)

- [ ] **Step 1: Add engine config constants and functions**

After line 20 (`PICOCLAW_CONFIG = ...`), add:

```python
ENGINE_CONFIG_PATH = "/home/prodrifterdk/.picoclaw/engine.json"
AGENTS_MD_PATH = "/home/prodrifterdk/.picoclaw/workspace/AGENTS.md"

VALID_ENGINES = ("picoclaw", "claude")
VALID_MODELS = ("sonnet", "opus", "haiku")
VALID_EFFORTS = ("low", "medium", "high", "max")

DEFAULT_ENGINE_CONFIG = {
    "active": "picoclaw",
    "claude": {
        "model": "sonnet",
        "effort": "high",
    }
}

def load_engine_config():
    try:
        with open(ENGINE_CONFIG_PATH, 'r') as f:
            cfg = json.load(f)
        # Validate and fill defaults
        if cfg.get("active") not in VALID_ENGINES:
            cfg["active"] = "picoclaw"
        claude = cfg.get("claude", {})
        if claude.get("model") not in VALID_MODELS:
            claude["model"] = "sonnet"
        if claude.get("effort") not in VALID_EFFORTS:
            claude["effort"] = "high"
        cfg["claude"] = claude
        return cfg
    except Exception as e:
        logger.warning(f"Could not read engine config: {e}. Using defaults.")
        return json.loads(json.dumps(DEFAULT_ENGINE_CONFIG))

def save_engine_config(config):
    import tempfile
    try:
        dir_name = os.path.dirname(ENGINE_CONFIG_PATH)
        fd, tmp_path = tempfile.mkstemp(dir=dir_name, suffix='.tmp')
        with os.fdopen(fd, 'w') as f:
            json.dump(config, f, indent=2)
        os.rename(tmp_path, ENGINE_CONFIG_PATH)
        return True
    except Exception as e:
        logger.error(f"Failed to save engine config: {e}")
        if 'tmp_path' in locals() and os.path.exists(tmp_path):
            os.remove(tmp_path)
        return False
```

- [ ] **Step 2: Verify syntax**

Run: `python3 -c "exec(open('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py').read().split('def process_updates')[0])"`
Expected: No errors

- [ ] **Step 3: Test load defaults when no file exists**

Run: `python3 -c "import sys; sys.path.insert(0, '/home/prodrifterdk/.picoclaw/workspace'); from tg_listener import load_engine_config; c = load_engine_config(); print(c); assert c['active'] == 'picoclaw'; assert c['claude']['model'] == 'sonnet'; print('OK')"`
Expected: Prints config dict and "OK"

- [ ] **Step 4: Test save and reload**

Run: `python3 -c "import sys; sys.path.insert(0, '/home/prodrifterdk/.picoclaw/workspace'); from tg_listener import save_engine_config, load_engine_config; save_engine_config({'active': 'claude', 'claude': {'model': 'opus', 'effort': 'max'}}); c = load_engine_config(); assert c['active'] == 'claude'; assert c['claude']['model'] == 'opus'; assert c['claude']['effort'] == 'max'; print('OK')"`
Expected: Prints "OK"

- [ ] **Step 5: Clean up test file and commit**

```bash
rm -f /home/prodrifterdk/.picoclaw/engine.json
git -C /home/prodrifterdk/.picoclaw/workspace add tg_listener.py
git -C /home/prodrifterdk/.picoclaw/workspace commit -m "feat: add engine config load/save for engine swap"
```

---

### Task 2: `/engine` Telegram command

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py:543-817` (inside `handle_message`, add new elif before `/help`)

- [ ] **Step 1: Add `/engine` command handler**

Before the `elif text.startswith("/help"):` block (line 802), insert:

```python
        elif text.startswith("/engine"):
            if user_id != ADMIN_ID:
                send_message(chat_id, "Only the admin can change the engine.")
                return
            parts = text.strip().split()
            config = load_engine_config()

            if len(parts) == 1:
                # Show current engine status
                active = config["active"]
                if active == "claude":
                    claude = config.get("claude", {})
                    model = claude.get("model", "sonnet")
                    effort = claude.get("effort", "high")
                    status = f"🔧 Engine: claude\n   Model: {model} | Effort: {effort}"
                else:
                    status = "🔧 Engine: picoclaw"
                send_message(chat_id, status)
                return

            engine = parts[1].lower()
            if engine not in VALID_ENGINES:
                send_message(chat_id, f"❌ Unknown engine '{parts[1]}'. Valid: {', '.join(VALID_ENGINES)}")
                return

            config["active"] = engine

            if engine == "claude":
                claude = config.get("claude", {})
                if len(parts) >= 3:
                    model = parts[2].lower()
                    if model not in VALID_MODELS:
                        send_message(chat_id, f"❌ Unknown model '{parts[2]}'. Valid: {', '.join(VALID_MODELS)}")
                        return
                    claude["model"] = model
                if len(parts) >= 4:
                    effort = parts[3].lower()
                    if effort not in VALID_EFFORTS:
                        send_message(chat_id, f"❌ Unknown effort '{parts[3]}'. Valid: {', '.join(VALID_EFFORTS)}")
                        return
                    claude["effort"] = effort
                config["claude"] = claude

            if save_engine_config(config):
                if engine == "claude":
                    claude = config["claude"]
                    send_message(chat_id, f"✅ Switched to claude\n   Model: {claude['model']} | Effort: {claude['effort']}")
                else:
                    send_message(chat_id, "✅ Switched to picoclaw")
            else:
                send_message(chat_id, "❌ Failed to save engine config.")
            return
```

- [ ] **Step 2: Add `/engine` to the `/help` text**

In the `/help` handler, add this line to the help_text string:

```python
                "• /engine \\[claude|picoclaw\\] \\[model\\] \\[effort\\] — Switch AI engine\n"
```

- [ ] **Step 3: Verify syntax**

Run: `python3 -c "exec(open('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py').read().split('def process_updates')[0])"`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git -C /home/prodrifterdk/.picoclaw/workspace add tg_listener.py
git -C /home/prodrifterdk/.picoclaw/workspace commit -m "feat: add /engine Telegram command for live engine switching"
```

---

### Task 3: Rename `ask_picoclaw_streaming` to `_picoclaw_adapter`

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py` (rename function + all call sites)

- [ ] **Step 1: Rename the function definition**

At line 237, change:
```python
def ask_picoclaw_streaming(text, chat_id):
```
to:
```python
def _picoclaw_adapter(text, chat_id):
```

- [ ] **Step 2: Update all call sites**

Replace all remaining references to `ask_picoclaw_streaming` with `_picoclaw_adapter`. There are 2 call sites:
- Line 650 (condense handler): `ask_picoclaw_streaming(condense_prompt, chat_id)` → `_picoclaw_adapter(condense_prompt, chat_id)`
- Line 831 (main message handler): `ask_picoclaw_streaming(ai_input, chat_id)` → `_picoclaw_adapter(ai_input, chat_id)`

`night_cron_runner.py` at `~/.picoclaw/workspace/cron/night_cron_runner.py` imports `ask_picoclaw_streaming` (line 7) and calls it (line 28). Add a mandatory backward compatibility alias after the function definition:
```python
ask_picoclaw_streaming = _picoclaw_adapter  # backward compat for cron scripts
```

- [ ] **Step 3: Verify no broken references**

Run: `grep -n "ask_picoclaw_streaming" /home/prodrifterdk/.picoclaw/workspace/tg_listener.py`
Expected: Only the compatibility alias line (if added), no other references

Run: `grep -rn "ask_picoclaw_streaming" /home/prodrifterdk/.picoclaw/workspace/cron/`
Expected: Check if any cron scripts import it — if yes, the alias covers them

- [ ] **Step 4: Verify syntax**

Run: `python3 -c "exec(open('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py').read().split('def process_updates')[0])"`
Expected: No errors

- [ ] **Step 5: Commit**

```bash
git -C /home/prodrifterdk/.picoclaw/workspace add tg_listener.py
git -C /home/prodrifterdk/.picoclaw/workspace commit -m "refactor: rename ask_picoclaw_streaming to _picoclaw_adapter"
```

---

### Task 4: Claude adapter — `_claude_adapter()`

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py` (add new function after `_picoclaw_adapter`)

- [ ] **Step 1: Add the `_claude_adapter` function**

Insert after `_picoclaw_adapter` (after its closing except block, around line 402). This is the core new code:

```python
def _claude_adapter(text, chat_id, model="sonnet", effort="high", is_guest=False):
    """Streams Claude Code CLI output as (event, content) tuples.

    Events: same as _picoclaw_adapter:
      'block_start'  — new text block started
      'block_update' — more text arrived for current block
      'block_done'   — block complete
      'error'        — unrecoverable error
    """
    import queue as queue_mod

    logger.info(f"[Claude] Asking claude ({model}/{effort}) for chat {chat_id}")
    logger.debug(f"[Claude] Input payload:\n{text}")

    # Build system prompt from AGENTS.md + overrides
    system_prompt = ""
    try:
        with open(AGENTS_MD_PATH, 'r') as f:
            system_prompt = f.read()
    except Exception as e:
        logger.warning(f"[Claude] Could not read AGENTS.md: {e}")

    override = (
        "\n\n---\n"
        "You do NOT need to start responses with 🦞 — the delivery system handles this automatically.\n\n"
        "Your persistent memory is at ~/.picoclaw/workspace/memory/MEMORY.md — read it when you need "
        "context about the user, projects, or past decisions.\n\n"
        "When asked to remember something, save it to ~/.picoclaw/workspace/memory/ following the "
        "existing structure. Update ~/.picoclaw/workspace/memory/MEMORY.md as the index. "
        "Use the same format as existing memory files there."
    )
    system_prompt += override

    cmd = [
        'claude', '-p',
        '--output-format', 'stream-json',
        '--include-partial-messages',
        '--model', model,
        '--effort', effort,
        '--dangerously-skip-permissions',
        '--system-prompt', system_prompt,
        '--add-dir', '/home/prodrifterdk/.picoclaw/workspace',
        '--no-session-persistence',
        '--max-budget-usd', '5',
        text,
    ]

    if is_guest:
        cmd.extend(['--disallowedTools', 'Bash,Edit,Write,NotebookEdit'])

    try:
        process = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
        )

        assert process.stdout is not None
        assert process.stderr is not None

        last_activity = [time.time()]
        process_start_time = time.time()
        deadline = process_start_time + 3600
        deadline_extended = False
        has_yielded = False

        # Watchdog
        def watchdog():
            while process.poll() is None:
                time.sleep(10)
                silence = time.time() - last_activity[0]
                if silence > 3600:
                    logger.error(f"[Claude][{chat_id}] Watchdog: silent for {silence:.0f}s, killing")
                    process.kill()
                    return

        watchdog_t = threading.Thread(target=watchdog, daemon=True)
        watchdog_t.start()

        # Stderr drain
        stderr_lines = []
        def drain_stderr():
            for line in process.stderr:
                stderr_lines.append(line)
        stderr_thread = threading.Thread(target=drain_stderr, daemon=True)
        stderr_thread.start()

        # Reader thread
        line_queue = queue_mod.Queue()
        def reader_thread():
            assert process.stdout is not None
            while True:
                line = process.stdout.readline()
                if not line:
                    line_queue.put(None)
                    break
                line_queue.put(line)
                last_activity[0] = time.time()

        reader_t = threading.Thread(target=reader_thread, daemon=True)
        reader_t.start()

        # State for accumulating text blocks
        text_buffer = []
        in_text_block = False
        tool_use_active = False

        while True:
            if time.time() > deadline:
                process.kill()
                process.wait()
                if not has_yielded:
                    yield ('error', "Error: Claude took too long to respond (timed out).")
                break

            try:
                line = line_queue.get(timeout=2.0)
            except queue_mod.Empty:
                if process.poll() is not None:
                    break
                continue

            if line is None:
                break

            line = line.strip()
            if not line:
                continue

            try:
                data = json.loads(line)
            except json.JSONDecodeError:
                logger.debug(f"[Claude] Non-JSON line: {line[:100]}")
                continue

            msg_type = data.get("type", "")

            if msg_type == "stream_event":
                event_data = data.get("event", {})
                event_type = event_data.get("type", "")

                if event_type == "content_block_start":
                    content_block = event_data.get("content_block", {})
                    block_type = content_block.get("type", "")

                    if block_type == "text":
                        in_text_block = True
                        tool_use_active = False
                        text_buffer = []
                        yield ('block_start', "")
                        has_yielded = True
                        if not deadline_extended:
                            deadline_extended = True
                            deadline = time.time() + 14400
                            logger.info(f"[Claude][{chat_id}] Extended deadline to 4h")

                    elif block_type == "tool_use":
                        tool_use_active = True
                        in_text_block = False
                        tool_name = content_block.get("name", "tool")
                        logger.info(f"[Claude][{chat_id}] Tool use: {tool_name}")

                elif event_type == "content_block_delta":
                    delta = event_data.get("delta", {})
                    delta_type = delta.get("type", "")

                    if delta_type == "text_delta" and in_text_block:
                        text_fragment = delta.get("text", "")
                        if text_fragment:
                            text_buffer.append(text_fragment)
                            yield ('block_update', ''.join(text_buffer).strip())

                elif event_type == "content_block_stop":
                    if in_text_block and text_buffer:
                        final_text = ''.join(text_buffer).strip()
                        if final_text:
                            yield ('block_done', final_text)
                        text_buffer = []
                        in_text_block = False

            elif msg_type == "result":
                if data.get("is_error"):
                    error_msg = data.get("result", "Unknown Claude error")
                    if not has_yielded:
                        yield ('error', f"Claude error: {error_msg}")
                # If not error, text was already yielded via stream events

            # Ignore: system, assistant, rate_limit_event

        # Flush any remaining text
        if in_text_block and text_buffer:
            final_text = ''.join(text_buffer).strip()
            if final_text:
                if not has_yielded:
                    yield ('block_start', final_text)
                yield ('block_done', final_text)
                has_yielded = True

        if not has_yielded:
            logger.warning(f"[Claude][{chat_id}] No output produced")

        elapsed = time.time() - process_start_time
        logger.info(f"[Claude][{chat_id}] Process completed after {elapsed:.0f}s")

        process.wait()
        stderr_thread.join(timeout=2)
        reader_t.join(timeout=2)
        if stderr_lines:
            logger.warning(f"[Claude] Raw stderr:\n{''.join(stderr_lines)}")

    except FileNotFoundError:
        logger.error("[Claude] claude CLI not found in PATH")
        yield ('error', "❌ Claude Code CLI not found. Install it or switch to picoclaw with /engine picoclaw")
    except Exception as e:
        logger.exception(f"[Claude] Error: {e}")
        yield ('error', f"Error communicating with Claude: {e}")
```

- [ ] **Step 2: Verify syntax**

Run: `python3 -c "exec(open('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py').read().split('def process_updates')[0])"`
Expected: No errors

- [ ] **Step 3: Quick smoke test the adapter in isolation**

Run: `python3 -c "
import sys; sys.path.insert(0, '/home/prodrifterdk/.picoclaw/workspace')
from tg_listener import _claude_adapter
for event, content in _claude_adapter('Say exactly: hello world', 0, 'haiku', 'low'):
    print(f'{event}: {content[:80]}')
"`
Expected: Should print block_start, block_update(s), block_done with "hello world" in the text

- [ ] **Step 4: Commit**

```bash
git -C /home/prodrifterdk/.picoclaw/workspace add tg_listener.py
git -C /home/prodrifterdk/.picoclaw/workspace commit -m "feat: add _claude_adapter for Claude Code CLI streaming"
```

---

### Task 5: Engine dispatcher — `ask_engine_streaming()`

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py` (add dispatcher function after `_claude_adapter`)

- [ ] **Step 1: Add the dispatcher function**

Insert after `_claude_adapter`:

```python
def ask_engine_streaming(text, chat_id, is_guest=False):
    """Dispatch to the active engine's streaming adapter.

    Reads engine.json per-call so changes take effect immediately.
    Falls back to picoclaw if anything goes wrong with config loading.
    """
    config = load_engine_config()
    engine = config.get("active", "picoclaw")

    if engine == "claude":
        claude_cfg = config.get("claude", {})
        model = claude_cfg.get("model", "sonnet")
        effort = claude_cfg.get("effort", "high")
        logger.info(f"[Engine] Dispatching to claude (model={model}, effort={effort})")
        yield from _claude_adapter(text, chat_id, model=model, effort=effort, is_guest=is_guest)
    else:
        logger.info("[Engine] Dispatching to picoclaw")
        yield from _picoclaw_adapter(text, chat_id)
```

- [ ] **Step 2: Verify syntax**

Run: `python3 -c "exec(open('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py').read().split('def process_updates')[0])"`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git -C /home/prodrifterdk/.picoclaw/workspace add tg_listener.py
git -C /home/prodrifterdk/.picoclaw/workspace commit -m "feat: add ask_engine_streaming dispatcher"
```

---

### Task 6: Wire up `handle_message()` to use the dispatcher

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py:650,831` (two call site swaps)

- [ ] **Step 1: Update the `/condense` handler**

At line 650, change:
```python
            for event, content in ask_picoclaw_streaming(condense_prompt, chat_id):
```
to:
```python
            for event, content in ask_engine_streaming(condense_prompt, chat_id):
```

- [ ] **Step 2: Update the main message handler**

At line 831, change:
```python
        for event, content in ask_picoclaw_streaming(ai_input, chat_id):
```
to:
```python
        is_guest = (user_id != ADMIN_ID)
        for event, content in ask_engine_streaming(ai_input, chat_id, is_guest=is_guest):
```

- [ ] **Step 3: Verify syntax**

Run: `python3 -c "exec(open('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py').read().split('def process_updates')[0])"`
Expected: No errors

- [ ] **Step 4: Verify no remaining direct adapter calls except the compat alias**

Run: `grep -n "_picoclaw_adapter\|_claude_adapter" /home/prodrifterdk/.picoclaw/workspace/tg_listener.py | grep -v "^.*def \|^.*#\|ask_engine_streaming\|ask_picoclaw_streaming"`
Expected: No direct calls outside of `ask_engine_streaming` and the backward compat alias

- [ ] **Step 5: Commit**

```bash
git -C /home/prodrifterdk/.picoclaw/workspace add tg_listener.py
git -C /home/prodrifterdk/.picoclaw/workspace commit -m "feat: wire handle_message to use ask_engine_streaming dispatcher"
```

---

### Task 7: End-to-end validation

- [ ] **Step 1: Test picoclaw engine (default) still works**

Ensure `engine.json` does not exist or has `"active": "picoclaw"`. Run:

```bash
rm -f /home/prodrifterdk/.picoclaw/engine.json
python3 -c "
import sys; sys.path.insert(0, '/home/prodrifterdk/.picoclaw/workspace')
from tg_listener import ask_engine_streaming
for event, content in ask_engine_streaming('Say hello', 0):
    print(f'{event}: {content[:80]}')
"
```

Expected: PicoClaw outputs with `🦞` prefix, parsed into block events

- [ ] **Step 2: Test claude engine**

```bash
python3 -c "
import sys, json; sys.path.insert(0, '/home/prodrifterdk/.picoclaw/workspace')
from tg_listener import save_engine_config, ask_engine_streaming
save_engine_config({'active': 'claude', 'claude': {'model': 'haiku', 'effort': 'low'}})
for event, content in ask_engine_streaming('Say exactly: engine swap works', 0):
    print(f'{event}: {content[:80]}')
"
```

Expected: Claude responds with block events, content includes "engine swap works"

- [ ] **Step 3: Test switching back to picoclaw**

```bash
python3 -c "
import sys; sys.path.insert(0, '/home/prodrifterdk/.picoclaw/workspace')
from tg_listener import save_engine_config
save_engine_config({'active': 'picoclaw', 'claude': {'model': 'haiku', 'effort': 'low'}})
print('Switched back to picoclaw — OK')
"
```

- [ ] **Step 4: Clean up and final commit**

```bash
rm -f /home/prodrifterdk/.picoclaw/engine.json
git -C /home/prodrifterdk/.picoclaw/workspace add tg_listener.py
git -C /home/prodrifterdk/.picoclaw/workspace commit -m "feat: engine swap complete — tg_listener supports picoclaw and claude backends"
```

- [ ] **Step 5: Restart tg_listener service to pick up changes**

```bash
systemctl --user restart tg_listener
systemctl --user status tg_listener
```

Expected: Service running, no errors in status output

- [ ] **Step 6: Live test from Telegram**

Send from Telegram:
1. `/engine` — should show "Engine: picoclaw"
2. `/engine claude haiku low` — should confirm switch
3. Send "Hello, who are you?" — should get a Resyst-style response from Claude
4. `/engine picoclaw` — should confirm switch back
5. Send "Hello again" — should get response from PicoClaw
