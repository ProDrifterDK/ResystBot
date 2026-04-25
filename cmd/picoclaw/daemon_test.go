package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMarshalDaemonEvent(t *testing.T) {
	e := daemonEvent{
		Type:   "response",
		ChatID: "123456",
		Text:   "hello",
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got["type"] != "response" {
		t.Errorf("type: want %q, got %q", "response", got["type"])
	}
	if got["chat_id"] != "123456" {
		t.Errorf("chat_id: want %q, got %q", "123456", got["chat_id"])
	}
	if got["text"] != "hello" {
		t.Errorf("text: want %q, got %q", "hello", got["text"])
	}
}

func TestMarshalDaemonEvent_OmitEmpty(t *testing.T) {
	e := daemonEvent{Type: "ready"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got["type"] != "ready" {
		t.Errorf("type: want %q, got %q", "ready", got["type"])
	}
	if _, ok := got["chat_id"]; ok {
		t.Error("chat_id should be omitted when empty")
	}
	if _, ok := got["text"]; ok {
		t.Error("text should be omitted when empty")
	}
	if _, ok := got["file_path"]; ok {
		t.Error("file_path should be omitted when empty")
	}
}

func TestParseDaemonInput_Message(t *testing.T) {
	raw := `{"type":"message","chat_id":"42","user":"Alan","username":"prodrifterdk","text":"hello bot"}`
	var in daemonInput
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if in.Type != "message" {
		t.Errorf("Type: want %q, got %q", "message", in.Type)
	}
	if in.ChatID != "42" {
		t.Errorf("ChatID: want %q, got %q", "42", in.ChatID)
	}
	if in.User != "Alan" {
		t.Errorf("User: want %q, got %q", "Alan", in.User)
	}
	if in.Username != "prodrifterdk" {
		t.Errorf("Username: want %q, got %q", "prodrifterdk", in.Username)
	}
	if in.Text != "hello bot" {
		t.Errorf("Text: want %q, got %q", "hello bot", in.Text)
	}
}

func TestParseDaemonInput_Cancel(t *testing.T) {
	raw := `{"type":"cancel","chat_id":"42"}`
	var in daemonInput
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if in.Type != "cancel" {
		t.Errorf("Type: want %q, got %q", "cancel", in.Type)
	}
	if in.ChatID != "42" {
		t.Errorf("ChatID: want %q, got %q", "42", in.ChatID)
	}
}

func TestParseDaemonInput_Shutdown(t *testing.T) {
	raw := `{"type":"shutdown"}`
	var in daemonInput
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if in.Type != "shutdown" {
		t.Errorf("Type: want %q, got %q", "shutdown", in.Type)
	}
}

// TestDaemonState_CancelChat verifies cancelChat returns false when no chat is registered.
func TestDaemonState_CancelChat(t *testing.T) {
	ds := newDaemonState()
	if ds.cancelChat("nonexistent") {
		t.Error("cancelChat on missing chat should return false")
	}
}

// TestDaemonState_RegisterAndCancel verifies registerChat + cancelChat round-trip.
func TestDaemonState_RegisterAndCancel(t *testing.T) {
	ds := newDaemonState()
	_, cancel := context.WithCancel(context.Background())
	ds.registerChat("99", cancel)
	if !ds.cancelChat("99") {
		t.Error("cancelChat should return true after registerChat")
	}
	// Second cancel on same chat should return false (already removed).
	if ds.cancelChat("99") {
		t.Error("second cancelChat should return false")
	}
}

// TestDaemonState_RegisterImplicitCancel verifies that registering a second
// handler for the same chat cancels the first one.
func TestDaemonState_RegisterImplicitCancel(t *testing.T) {
	ds := newDaemonState()
	ctx1, cancel1 := context.WithCancel(context.Background())
	ds.registerChat("7", cancel1)

	_, cancel2 := context.WithCancel(context.Background())
	ds.registerChat("7", cancel2) // should implicitly cancel ctx1

	if ctx1.Err() == nil {
		t.Error("first context should have been cancelled by implicit cancel")
	}
	// The second cancel should now be the active one.
	if !ds.cancelChat("7") {
		t.Error("cancelChat should return true for the second registration")
	}
}

// TestDaemonState_CancelAll verifies cancelAll fires all pending cancels.
func TestDaemonState_CancelAll(t *testing.T) {
	ds := newDaemonState()
	ctx1, c1 := context.WithCancel(context.Background())
	ctx2, c2 := context.WithCancel(context.Background())
	ds.registerChat("a", c1)
	ds.registerChat("b", c2)
	ds.cancelAll()
	if ctx1.Err() == nil {
		t.Error("ctx1 should be cancelled after cancelAll")
	}
	if ctx2.Err() == nil {
		t.Error("ctx2 should be cancelled after cancelAll")
	}
}
