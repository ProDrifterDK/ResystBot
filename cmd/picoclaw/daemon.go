package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
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
