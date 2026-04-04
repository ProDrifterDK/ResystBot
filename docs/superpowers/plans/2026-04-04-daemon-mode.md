# PicoClaw Daemon Mode — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-message subprocess spawning with a long-running PicoClaw daemon that communicates with tg_listener via JSON-lines over stdin/stdout.

**Architecture:** A new `--daemon` flag in `cmd_agent.go` enters a loop reading JSON-line messages from stdin. Each message spawns a goroutine for processing. Responses are emitted as JSON-line events on stdout. A cancel map enables per-chat interruption. The tg_listener replaces `_picoclaw_adapter` with a `DaemonManager` class that manages the persistent subprocess.

**Tech Stack:** Go (daemon), Python (tg_listener), JSON-lines protocol, `bufio.Scanner` for stdin, `encoding/json` for marshaling.

---

### Task 1: Create daemon event types and emitter

**Files:**
- Create: `cmd/picoclaw/daemon.go`
- Create: `cmd/picoclaw/daemon_test.go`

- [ ] **Step 1: Write the failing test for event marshaling**

Create `cmd/picoclaw/daemon_test.go`:

```go
package main

import (
	"encoding/json"
	"testing"
)

func TestMarshalDaemonEvent(t *testing.T) {
	event := daemonEvent{
		Type:   "response",
		ChatID: "221899910",
		Text:   "Hello world",
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if parsed["type"] != "response" {
		t.Errorf("expected type=response, got %s", parsed["type"])
	}
	if parsed["chat_id"] != "221899910" {
		t.Errorf("expected chat_id=221899910, got %s", parsed["chat_id"])
	}
	if parsed["text"] != "Hello world" {
		t.Errorf("expected text=Hello world, got %s", parsed["text"])
	}
}

func TestMarshalDaemonEvent_OmitEmpty(t *testing.T) {
	event := daemonEvent{
		Type: "ready",
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if _, ok := parsed["chat_id"]; ok {
		t.Error("ready event should not include chat_id")
	}
	if _, ok := parsed["text"]; ok {
		t.Error("ready event should not include text")
	}
}

func TestParseDaemonInput_Message(t *testing.T) {
	line := `{"type":"message","chat_id":"221899910","user":"Alan","username":"ProDrifterDK","text":"hello"}`

	var input daemonInput
	if err := json.Unmarshal([]byte(line), &input); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if input.Type != "message" {
		t.Errorf("expected type=message, got %s", input.Type)
	}
	if input.ChatID != "221899910" {
		t.Errorf("expected chat_id=221899910, got %s", input.ChatID)
	}
	if input.User != "Alan" {
		t.Errorf("expected user=Alan, got %s", input.User)
	}
	if input.Text != "hello" {
		t.Errorf("expected text=hello, got %s", input.Text)
	}
}

func TestParseDaemonInput_Cancel(t *testing.T) {
	line := `{"type":"cancel","chat_id":"221899910"}`

	var input daemonInput
	if err := json.Unmarshal([]byte(line), &input); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if input.Type != "cancel" {
		t.Errorf("expected type=cancel, got %s", input.Type)
	}
	if input.ChatID != "221899910" {
		t.Errorf("expected chat_id=221899910, got %s", input.ChatID)
	}
}

func TestParseDaemonInput_Shutdown(t *testing.T) {
	line := `{"type":"shutdown"}`

	var input daemonInput
	if err := json.Unmarshal([]byte(line), &input); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if input.Type != "shutdown" {
		t.Errorf("expected type=shutdown, got %s", input.Type)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./cmd/picoclaw/ -run TestMarshalDaemonEvent -v`
Expected: FAIL — `daemonEvent` not defined

- [ ] **Step 3: Implement the event types and emitter**

Create `cmd/picoclaw/daemon.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// daemonInput represents a JSON-line message received from tg_listener on stdin.
type daemonInput struct {
	Type     string `json:"type"`               // "message", "cancel", "shutdown"
	ChatID   string `json:"chat_id,omitempty"`
	User     string `json:"user,omitempty"`
	Username string `json:"username,omitempty"`
	Text     string `json:"text,omitempty"`
}

// daemonEvent represents a JSON-line event emitted to tg_listener on stdout.
type daemonEvent struct {
	Type   string `json:"type"`             // "ready", "status", "response", "error"
	ChatID string `json:"chat_id,omitempty"`
	Text   string `json:"text,omitempty"`
}

// daemonState tracks per-chat cancellation contexts.
type daemonState struct {
	mu        sync.Mutex
	cancelMap map[string]context.CancelFunc
}

func newDaemonState() *daemonState {
	return &daemonState{
		cancelMap: make(map[string]context.CancelFunc),
	}
}

// cancelChat cancels the in-flight request for a chat, if any.
// Returns true if there was a request to cancel.
func (ds *daemonState) cancelChat(chatID string) bool {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if cancel, ok := ds.cancelMap[chatID]; ok {
		cancel()
		delete(ds.cancelMap, chatID)
		return true
	}
	return false
}

// registerChat stores a cancel func for an in-flight chat request.
// If the chat already has an in-flight request, it is cancelled first (implicit cancel).
func (ds *daemonState) registerChat(chatID string, cancel context.CancelFunc) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if existing, ok := ds.cancelMap[chatID]; ok {
		existing() // implicit cancel
	}
	ds.cancelMap[chatID] = cancel
}

// removeChat removes the cancel func for a completed chat request.
func (ds *daemonState) removeChat(chatID string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	delete(ds.cancelMap, chatID)
}

// cancelAll cancels all in-flight requests (used during shutdown).
func (ds *daemonState) cancelAll() {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	for chatID, cancel := range ds.cancelMap {
		cancel()
		delete(ds.cancelMap, chatID)
	}
}

// emitMu serializes writes to stdout so concurrent goroutines don't interleave JSON lines.
var emitMu sync.Mutex

// emitEvent writes a JSON-line event to stdout.
func emitEvent(eventType, chatID, text string) {
	event := daemonEvent{
		Type:   eventType,
		ChatID: chatID,
		Text:   text,
	}
	data, err := json.Marshal(event)
	if err != nil {
		fmt.Fprintln(os.Stderr, "emitEvent marshal error:", err)
		return
	}
	emitMu.Lock()
	fmt.Println(string(data))
	os.Stdout.Sync() //nolint:errcheck
	emitMu.Unlock()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go test ./cmd/picoclaw/ -run "TestMarshalDaemonEvent|TestParseDaemonInput" -v`
Expected: PASS (all 5 tests)

- [ ] **Step 5: Commit**

```bash
git add cmd/picoclaw/daemon.go cmd/picoclaw/daemon_test.go
git commit -m "feat: add daemon event types, state manager, and emitter"
```

---

### Task 2: Implement the daemon loop

**Files:**
- Modify: `cmd/picoclaw/daemon.go`
- Modify: `cmd/picoclaw/cmd_agent.go`

- [ ] **Step 1: Add the daemonMode function to daemon.go**

Add this function to `cmd/picoclaw/daemon.go`. Add `"bufio"`, `"github.com/sipeed/picoclaw/pkg/agent"`, `"github.com/sipeed/picoclaw/pkg/bus"`, and `"github.com/sipeed/picoclaw/pkg/logger"` to the imports:

```go
// daemonMode runs PicoClaw as a long-running daemon, reading JSON-line
// messages from stdin and emitting JSON-line events on stdout.
func daemonMode(agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, channel string) {
	state := newDaemonState()

	// Outbound message listener: convert message tool output to status events.
	go func() {
		ctx := context.Background()
		for {
			msg, ok := msgBus.SubscribeOutbound(ctx)
			if !ok {
				break
			}
			emitEvent("status", msg.ChatID, msg.Content)
		}
	}()

	emitEvent("ready", "", "")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var input daemonInput
		if err := json.Unmarshal([]byte(line), &input); err != nil {
			logger.WarnCF("daemon", "Invalid JSON input", map[string]any{"error": err.Error(), "line": line})
			continue
		}

		switch input.Type {
		case "message":
			if input.ChatID == "" || input.Text == "" {
				emitEvent("error", input.ChatID, "message requires chat_id and text")
				continue
			}
			// Implicit cancel: if this chat has an in-flight request, cancel it
			state.cancelChat(input.ChatID)

			ctx, cancel := context.WithCancel(context.Background())
			state.registerChat(input.ChatID, cancel)

			go processChat(ctx, agentLoop, state, input, channel)

		case "cancel":
			if input.ChatID == "" {
				continue
			}
			state.cancelChat(input.ChatID)

		case "shutdown":
			logger.InfoCF("daemon", "Shutdown requested", nil)
			state.cancelAll()
			return // agentCmd's defer handles Shutdown + session save

		default:
			logger.WarnCF("daemon", "Unknown input type", map[string]any{"type": input.Type})
		}
	}

	if err := scanner.Err(); err != nil {
		logger.WarnCF("daemon", "Stdin read error", map[string]any{"error": err.Error()})
	}
	// EOF on stdin — parent process died
	state.cancelAll()
}

// processChat handles a single chat message in its own goroutine.
func processChat(ctx context.Context, agentLoop *agent.AgentLoop, state *daemonState, input daemonInput, channel string) {
	defer state.removeChat(input.ChatID)

	msg := bus.InboundMessage{
		Channel:  channel,
		SenderID: input.Username,
		ChatID:   input.ChatID,
		Content:  input.Text,
		Metadata: map[string]string{
			"user":     input.User,
			"username": input.Username,
		},
	}

	response, err := agentLoop.ProcessMessage(ctx, msg)
	if err != nil {
		if ctx.Err() == context.Canceled {
			// Cancelled by new message or explicit cancel — discard silently
			logger.DebugCF("daemon", "Chat cancelled", map[string]any{"chat_id": input.ChatID})
			return
		}
		emitEvent("error", input.ChatID, fmt.Sprintf("processing error: %v", err))
		return
	}

	if response != "" && response != "SILENT" {
		emitEvent("response", input.ChatID, response)
	}

	// Wait for subagents if any
	agentLoop.WaitForSubagents()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer drainCancel()
	agentLoop.DrainInbound(drainCtx, channel, input.ChatID)
}
```

Add `"time"` to the imports.

- [ ] **Step 2: Expose processMessage in agent loop**

The `processMessage` method in `pkg/agent/loop.go` is currently unexported (lowercase). Add a public wrapper. Add this to `pkg/agent/loop.go` after the existing `ProcessDirectWithChannel` function (around line 418):

```go
// ProcessMessage routes and processes an inbound message through the agent system.
// This is the main entry point for daemon mode, where the caller constructs
// the full InboundMessage with metadata for proper routing.
func (al *AgentLoop) ProcessMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	return al.processMessage(ctx, msg)
}
```

- [ ] **Step 3: Add --daemon flag to cmd_agent.go**

In `cmd_agent.go`, add the `daemonMode` flag parsing. After the existing flag declarations (line 56), add:

```go
	daemon := false
```

In the switch-case block (around line 60), add:

```go
		case "--daemon":
			daemon = true
```

Then modify the mode selection at the bottom of `agentCmd()`. Replace the existing block (around line 186-286):

```go
	if daemon {
		daemonMode(agentLoop, msgBus, channel)
	} else if message != "" {
		// ... existing one-shot mode code stays unchanged ...
```

The `else` clause for interactive mode also stays unchanged. The key change is adding `if daemon {` as the first branch.

- [ ] **Step 4: Verify it compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add cmd/picoclaw/daemon.go cmd/picoclaw/cmd_agent.go pkg/agent/loop.go
git commit -m "feat: add daemon loop with stdin reader and processChat goroutines"
```

---

### Task 3: Guard stdout contamination in daemon mode

**Files:**
- Modify: `cmd/picoclaw/cmd_agent.go`

All existing `fmt.Printf` / `fmt.Println` calls that write to stdout in `cmd_agent.go` must be gated so they don't corrupt the JSON-lines protocol in daemon mode. In daemon mode, only `emitEvent()` may write to stdout.

- [ ] **Step 1: Pass daemon flag to control stdout behavior**

The outbound goroutine (lines 153-175) prints raw 🦞 lines to stdout. In daemon mode, this goroutine is replaced by the one in `daemonMode()`. Gate it:

Replace the outbound goroutine block (lines 153-175 in `cmd_agent.go`):

```go
	// Start outbound listener only in non-daemon modes.
	// In daemon mode, daemonMode() has its own outbound listener that emits JSON events.
	if !daemon {
		go func() {
			ctx := context.Background()
			for {
				msg, ok := msgBus.SubscribeOutbound(ctx)
				if !ok {
					break
				}
				if isInternalMessage(msg.Content) {
					logger.DebugCF("agent", "Suppressing internal status message from stdout",
						map[string]any{"content": msg.Content})
					continue
				}
				fmt.Printf("\n%s %s\n\n", logo, msg.Content)
				os.Stdout.Sync() //nolint:errcheck
				atomic.AddInt64(&outboundPrinted, 1)
			}
		}()
	}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build ./...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add cmd/picoclaw/cmd_agent.go
git commit -m "feat: gate stdout writes behind daemon flag to prevent protocol corruption"
```

---

### Task 4: Implement DaemonManager in tg_listener.py

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py`

- [ ] **Step 1: Add the DaemonManager class**

Add this class after the existing `write_session` function (around line 85) and before `load_engine_config`:

```python
class DaemonManager:
    """Manages a persistent PicoClaw daemon process communicating via JSON-lines."""

    def __init__(self):
        self.process = None
        self.ready = False
        self.lock = threading.Lock()         # Serializes stdin writes
        self.callbacks = {}                  # chat_id -> {'on_status': fn, 'on_response': fn, 'on_error': fn}
        self.pending_messages = {}           # chat_id -> full input dict (for crash replay)
        self.last_crash = 0
        self.backoff = 2
        self._reader_thread = None

    def start(self):
        """Spawn PicoClaw daemon and wait for ready signal."""
        self.ready = False
        try:
            self.process = subprocess.Popen(
                ['picoclaw', 'agent', '--daemon', '--channel', 'telegram'],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                bufsize=1,
            )
            logger.info("[DaemonManager] Started PicoClaw daemon (PID %d)", self.process.pid)

            # Drain stderr to log
            self._stderr_thread = threading.Thread(target=self._drain_stderr, daemon=True)
            self._stderr_thread.start()

            # Start reader thread
            self._reader_thread = threading.Thread(target=self._reader_loop, daemon=True)
            self._reader_thread.start()

            # Wait for ready signal (max 60s)
            deadline = time.time() + 60
            while not self.ready and time.time() < deadline:
                if self.process.poll() is not None:
                    logger.error("[DaemonManager] Daemon exited during startup (code %d)", self.process.returncode)
                    return False
                time.sleep(0.1)

            if not self.ready:
                logger.error("[DaemonManager] Daemon did not emit ready within 60s")
                self.process.kill()
                return False

            logger.info("[DaemonManager] Daemon ready")
            return True

        except Exception as e:
            logger.exception("[DaemonManager] Failed to start daemon: %s", e)
            return False

    def is_alive(self):
        return self.process is not None and self.process.poll() is None

    def send_message(self, chat_id, user, username, text, on_status=None, on_response=None, on_error=None):
        """Send a message to the daemon. Callbacks are invoked from the reader thread."""
        if not self.is_alive():
            if on_error:
                on_error(chat_id, "Daemon not running")
            return

        self.callbacks[chat_id] = {
            'on_status': on_status,
            'on_response': on_response,
            'on_error': on_error,
        }

        msg = {
            'type': 'message',
            'chat_id': str(chat_id),
            'user': user,
            'username': username,
            'text': text,
        }
        self.pending_messages[str(chat_id)] = msg
        self._write(msg)

    def send_cancel(self, chat_id):
        """Cancel the in-flight request for a chat."""
        self._write({'type': 'cancel', 'chat_id': str(chat_id)})

    def shutdown(self):
        """Graceful shutdown."""
        self._write({'type': 'shutdown'})
        if self.process:
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.process.kill()

    def _write(self, obj):
        """Write a JSON line to the daemon's stdin."""
        with self.lock:
            try:
                line = json.dumps(obj) + '\n'
                self.process.stdin.write(line)
                self.process.stdin.flush()
            except (BrokenPipeError, OSError) as e:
                logger.error("[DaemonManager] Write failed: %s", e)
                self._handle_crash()

    def _reader_loop(self):
        """Read JSON-line events from daemon stdout and dispatch to callbacks."""
        try:
            for line in self.process.stdout:
                line = line.strip()
                if not line:
                    continue
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    logger.warning("[DaemonManager] Non-JSON stdout: %s", line[:100])
                    continue

                event_type = event.get('type', '')
                chat_id = event.get('chat_id', '')
                text = event.get('text', '')

                if event_type == 'ready':
                    self.ready = True
                    continue

                cbs = self.callbacks.get(chat_id, {})

                if event_type == 'status':
                    if cbs.get('on_status'):
                        cbs['on_status'](chat_id, text)

                elif event_type == 'response':
                    self.pending_messages.pop(chat_id, None)
                    if cbs.get('on_response'):
                        cbs['on_response'](chat_id, text)
                    self.callbacks.pop(chat_id, None)

                elif event_type == 'error':
                    self.pending_messages.pop(chat_id, None)
                    if cbs.get('on_error'):
                        cbs['on_error'](chat_id, text)
                    self.callbacks.pop(chat_id, None)

        except Exception as e:
            logger.exception("[DaemonManager] Reader loop error: %s", e)

        # If we get here, stdout closed — daemon died
        logger.error("[DaemonManager] Daemon stdout closed (process died)")
        self._handle_crash()

    def _drain_stderr(self):
        """Read daemon stderr and log it."""
        try:
            for line in self.process.stderr:
                logger.debug("[daemon stderr] %s", line.rstrip())
        except Exception:
            pass

    def _handle_crash(self):
        """Auto-restart the daemon with backoff."""
        now = time.time()

        # Notify users with pending messages
        for chat_id in list(self.pending_messages.keys()):
            try:
                send_message(int(chat_id), "🦞 I had a brief hiccup, restarting...")
            except Exception:
                pass

        # Backoff
        if now - self.last_crash < 60:
            self.backoff = min(self.backoff * 2, 60)
        else:
            self.backoff = 2
        self.last_crash = now

        logger.info("[DaemonManager] Restarting in %ds...", self.backoff)
        time.sleep(self.backoff)

        if self.start():
            # Replay pending messages
            for chat_id, msg in list(self.pending_messages.items()):
                logger.info("[DaemonManager] Replaying message for chat %s", chat_id)
                self._write(msg)


# Global daemon manager instance
_daemon_mgr = None

def get_daemon_manager():
    """Get or create the global DaemonManager."""
    global _daemon_mgr
    if _daemon_mgr is None or not _daemon_mgr.is_alive():
        _daemon_mgr = DaemonManager()
        if not _daemon_mgr.start():
            logger.error("[DaemonManager] Failed to start daemon")
            return None
    return _daemon_mgr
```

- [ ] **Step 2: Create the daemon-based adapter function**

Add this function after the `DaemonManager` class (before `ask_engine_streaming`):

```python
def _picoclaw_daemon_adapter(text, chat_id, user="Unknown", username="unknown"):
    """Streams PicoClaw output via the daemon as (event, content) tuples.

    Yields the same event types as _picoclaw_adapter for compatibility:
      'block_start'  — new response started
      'block_done'   — response complete
      'error'        — error occurred
    """
    mgr = get_daemon_manager()
    if mgr is None:
        yield ('error', "Failed to start PicoClaw daemon")
        return

    result_queue = queue.Queue()

    def on_status(cid, text):
        result_queue.put(('block_start', text))
        result_queue.put(('block_done', text))

    def on_response(cid, text):
        result_queue.put(('block_start', text))
        result_queue.put(('block_done', text))
        result_queue.put(None)  # Signal completion

    def on_error(cid, text):
        result_queue.put(('error', text))
        result_queue.put(None)

    mgr.send_message(
        chat_id=str(chat_id),
        user=user,
        username=username,
        text=text,
        on_status=on_status,
        on_response=on_response,
        on_error=on_error,
    )

    # Yield events until completion signal
    timeout = 3600  # 1 hour max
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            item = result_queue.get(timeout=30)
            if item is None:
                return  # Completion signal
            yield item
        except queue.Empty:
            if not mgr.is_alive():
                yield ('error', "PicoClaw daemon died")
                return
            continue
```

Add `import queue` at the top of the file if not already present.

- [ ] **Step 3: Switch ask_engine_streaming to use the daemon adapter**

Update `ask_engine_streaming` signature to accept user/username, and update the picoclaw dispatch (around line 739):

```python
def ask_engine_streaming(text, chat_id, is_guest=False, user="Unknown", username="unknown"):
```

Replace the picoclaw dispatch (around line 755):

```python
    else:
        logger.info("[Engine] Dispatching to picoclaw (daemon)")
        yield from _picoclaw_daemon_adapter(text, chat_id, user=user, username=username)
```

Update the call site in `handle_message` (around line 1240) to pass user/username:

```python
        for event, content in ask_engine_streaming(ai_input, chat_id, is_guest=is_guest, user=username, username=username):
```

Note: `username` is already available in `handle_message`'s parameters.

- [ ] **Step 4: Verify the file is valid Python**

Run: `python3 -c "import py_compile; py_compile.compile('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py', doraise=True)"`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
cd /home/prodrifterdk/.picoclaw/workspace && git add tg_listener.py
git commit -m "feat: add DaemonManager and daemon adapter to tg_listener"
```

Note: tg_listener.py is in the workspace directory, not the main repo. If it's not tracked by git, just save it — it's a runtime file.

---

### Task 5: Handle interruption in tg_listener

**Files:**
- Modify: `~/.picoclaw/workspace/tg_listener.py`

- [ ] **Step 1: Add implicit cancel on new message for busy chat**

In `handle_message`, before calling `ask_engine_streaming`, check if the daemon has a pending request for this chat. The daemon handles implicit cancel internally (new message cancels old), so no explicit cancel is needed — just send the new message. The daemon's `registerChat` will cancel the old context.

However, the `_picoclaw_daemon_adapter` needs to handle the case where a previous call's `result_queue` is still waiting. When a new message arrives for the same chat, the old adapter call should stop yielding. This is already handled by the daemon: when it cancels the old context, the old `processChat` goroutine exits without emitting a response, so the old `result_queue` will time out. But we should be explicit.

Add a chat-level tracking dict. Before the `handle_message` function, add:

```python
_active_adapters = {}  # chat_id -> queue.Queue (completion signal)
```

In `_picoclaw_daemon_adapter`, before sending the message:

```python
    # If there's an active adapter for this chat, signal it to stop
    old_queue = _active_adapters.get(str(chat_id))
    if old_queue is not None:
        old_queue.put(None)  # Signal old adapter to stop yielding
    _active_adapters[str(chat_id)] = result_queue
```

And at the end of `_picoclaw_daemon_adapter` (after the while loop), clean up:

```python
    _active_adapters.pop(str(chat_id), None)
```

- [ ] **Step 2: Verify the file is valid Python**

Run: `python3 -c "import py_compile; py_compile.compile('/home/prodrifterdk/.picoclaw/workspace/tg_listener.py', doraise=True)"`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
cd /home/prodrifterdk/.picoclaw/workspace && git add tg_listener.py
git commit -m "feat: handle implicit cancel for busy chats in daemon adapter"
```

---

### Task 6: Integration test — end-to-end daemon communication

**Files:**
- None (manual testing)

- [ ] **Step 1: Build the binary**

Run: `cd /home/prodrifterdk/Documentos/projects/ResystBot && go build -o /home/prodrifterdk/.local/bin/picoclaw ./cmd/picoclaw/`
Expected: Binary builds successfully

- [ ] **Step 2: Test daemon startup and ready signal**

Run:
```bash
echo '{"type":"shutdown"}' | timeout 30 picoclaw agent --daemon --channel telegram 2>/dev/null
```
Expected: First line of stdout is `{"type":"ready"}`, then process exits cleanly.

- [ ] **Step 3: Test message processing**

Run:
```bash
(echo '{"type":"message","chat_id":"test","user":"Test","username":"test","text":"say hi"}'; sleep 15; echo '{"type":"shutdown"}') | timeout 30 picoclaw agent --daemon --channel cli 2>/dev/null
```
Expected: See `{"type":"ready"}` followed by `{"type":"response","chat_id":"test","text":"..."}`.

- [ ] **Step 4: Restart tg_listener and test via Telegram**

```bash
# Kill old tg_listener
pkill -f tg_listener.py
sleep 2
# Start fresh
python3 /home/prodrifterdk/.picoclaw/workspace/tg_listener.py >> /home/prodrifterdk/.picoclaw/workspace/logs/tg_listener.log 2>&1 &
disown
```

Send a message on Telegram. Verify:
- One response (not two)
- Send a follow-up message — bot should have conversation context (no "Hola" greeting)
- Send a message while bot is busy — should interrupt and process new message

- [ ] **Step 5: Test crash recovery**

Kill the daemon process while it's running:
```bash
pkill -f "picoclaw agent --daemon"
```

Verify:
- tg_listener logs "I had a brief hiccup, restarting..."
- Daemon restarts automatically
- Next message works normally
