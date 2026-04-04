package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// daemonInput is a JSON-line message received from tg_listener on stdin.
type daemonInput struct {
	Type     string `json:"type"`               // "message", "cancel", "shutdown"
	ChatID   string `json:"chat_id,omitempty"`
	User     string `json:"user,omitempty"`
	Username string `json:"username,omitempty"`
	Text     string `json:"text,omitempty"`
}

// daemonEvent is a JSON-line event emitted to tg_listener on stdout.
type daemonEvent struct {
	Type   string `json:"type"`              // "ready", "status", "response", "error"
	ChatID string `json:"chat_id,omitempty"`
	Text   string `json:"text,omitempty"`
}

// emitMu serializes stdout writes from concurrent goroutines.
var emitMu sync.Mutex

// emitEvent marshals a daemonEvent to JSON and writes it to stdout with a
// trailing newline, then flushes. Marshal errors are logged to stderr.
func emitEvent(eventType, chatID, text string) {
	e := daemonEvent{Type: eventType, ChatID: chatID, Text: text}
	b, err := json.Marshal(e)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: emitEvent marshal error: %v\n", err)
		return
	}
	emitMu.Lock()
	defer emitMu.Unlock()
	fmt.Fprintf(os.Stdout, "%s\n", b)
	os.Stdout.Sync() //nolint:errcheck
}

// daemonState tracks per-chat cancellation functions.
type daemonState struct {
	mu        sync.Mutex
	cancelMap map[string]context.CancelFunc
}

// newDaemonState returns an initialised daemonState.
func newDaemonState() *daemonState {
	return &daemonState{
		cancelMap: make(map[string]context.CancelFunc),
	}
}

// registerChat stores the cancel function for chatID. If there is already an
// in-flight request for that chat, the previous cancel is called first.
func (ds *daemonState) registerChat(chatID string, cancel context.CancelFunc) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if prev, ok := ds.cancelMap[chatID]; ok {
		prev()
	}
	ds.cancelMap[chatID] = cancel
}

// cancelChat calls and removes the cancel function for chatID. Returns true if
// there was an in-flight request to cancel, false otherwise.
func (ds *daemonState) cancelChat(chatID string) bool {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	cancel, ok := ds.cancelMap[chatID]
	if !ok {
		return false
	}
	cancel()
	delete(ds.cancelMap, chatID)
	return true
}

// removeChat removes the cancel entry for chatID without calling the cancel
// function. Call this after a request completes normally.
func (ds *daemonState) removeChat(chatID string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	delete(ds.cancelMap, chatID)
}

// cancelAll cancels every in-flight request and clears the map.
func (ds *daemonState) cancelAll() {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	for id, cancel := range ds.cancelMap {
		cancel()
		delete(ds.cancelMap, id)
	}
}

// daemonMode is the main daemon entry point. It reads JSON-line commands from
// stdin and dispatches chat processing in goroutines, supporting concurrent
// chats with per-chat cancellation.
func daemonMode(agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, channel string) {
	state := newDaemonState()

	// Forward outbound bus messages as {"type":"status"} events so
	// tg_listener can relay intermediate tool output to the user.
	go func() {
		ctx := context.Background()
		for {
			msg, ok := msgBus.SubscribeOutbound(ctx)
			if !ok {
				return
			}
			emitEvent("status", msg.ChatID, msg.Content)
		}
	}()

	// Signal readiness to the parent process.
	emitEvent("ready", "", "")

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		var input daemonInput
		if err := json.Unmarshal(line, &input); err != nil {
			logger.WarnCF("daemon", "Failed to parse stdin line", map[string]any{
				"error": err.Error(),
				"raw":   string(line),
			})
			continue
		}

		switch input.Type {
		case "message":
			// Implicit-cancel: if there is an in-flight request for this
			// chat, cancel it before starting a new one.
			state.cancelChat(input.ChatID)

			ctx, cancel := context.WithCancel(context.Background())
			state.registerChat(input.ChatID, cancel)
			go processChat(ctx, state, agentLoop, channel, input)

		case "cancel":
			if state.cancelChat(input.ChatID) {
				logger.InfoCF("daemon", "Cancelled chat", map[string]any{"chat_id": input.ChatID})
			}

		case "shutdown":
			state.cancelAll()
			return
		}
	}

	// stdin EOF — parent went away.
	state.cancelAll()
}

// processChat handles a single chat request in its own goroutine. It builds
// an InboundMessage, calls the agent loop, emits the response, then drains
// any pending subagent work.
func processChat(ctx context.Context, state *daemonState, agentLoop *agent.AgentLoop, channel string, input daemonInput) {
	defer state.removeChat(input.ChatID)

	sessionKey := channel + ":" + input.ChatID
	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   input.Username,
		ChatID:     input.ChatID,
		Content:    input.Text,
		SessionKey: sessionKey,
		Metadata: map[string]string{
			"user":     input.User,
			"username": input.Username,
		},
	}

	response, err := agentLoop.ProcessMessage(ctx, msg)
	if ctx.Err() == context.Canceled {
		// Request was superseded or explicitly cancelled — exit silently.
		return
	}
	if err != nil {
		emitEvent("error", input.ChatID, err.Error())
		return
	}
	if response != "" {
		emitEvent("response", input.ChatID, response)
	}

	// Wait for any spawned subagents, then drain pending inbound messages
	// that they may have produced.
	agentLoop.WaitForSubagents()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer drainCancel()
	agentLoop.DrainInbound(drainCtx, channel, input.ChatID)
}
