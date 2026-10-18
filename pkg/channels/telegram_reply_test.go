package channels

import (
	"testing"

	"github.com/mymmrac/telego"
)

func TestTelegramQuotedContent_TextOnly(t *testing.T) {
	msg := &telego.Message{Text: "  hola mundo  "}
	if got := telegramQuotedContent(msg); got != "hola mundo" {
		t.Fatalf("got %q, want %q", got, "hola mundo")
	}
}

func TestTelegramQuotedContent_CaptionFallback(t *testing.T) {
	msg := &telego.Message{Caption: "caption context"}
	if got := telegramQuotedContent(msg); got != "caption context" {
		t.Fatalf("got %q, want %q", got, "caption context")
	}
}

func TestTelegramQuotedContent_VoiceMarkerAlongsideCaption(t *testing.T) {
	msg := &telego.Message{
		Caption: "listen to this",
		Voice:   &telego.Voice{FileID: "v1"},
	}
	want := "listen to this\n[voice]"
	if got := telegramQuotedContent(msg); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTelegramQuotedContent_AudioAndDocumentMarkers(t *testing.T) {
	msg := &telego.Message{
		Audio:    &telego.Audio{FileID: "a1"},
		Document: &telego.Document{FileID: "d1"},
	}
	want := "[audio]\n[file]"
	if got := telegramQuotedContent(msg); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTelegramQuotedContent_PhotoMarker(t *testing.T) {
	msg := &telego.Message{Photo: []telego.PhotoSize{{FileID: "p1"}}}
	if got := telegramQuotedContent(msg); got != "[image: photo]" {
		t.Fatalf("got %q, want %q", got, "[image: photo]")
	}
}

func TestTelegramQuotedContent_Nil(t *testing.T) {
	if got := telegramQuotedContent(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestPrependTelegramQuotedReply_UserReply(t *testing.T) {
	var c *TelegramChannel // nil receiver is supported by role detection
	reply := &telego.Message{
		Text: "old context",
		From: &telego.User{ID: 12, FirstName: "Bob"},
	}
	want := "[quoted user message from Bob]: old context\n\nfollow up"
	if got := c.prependTelegramQuotedReply("follow up", reply); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPrependTelegramQuotedReply_PrefersUsername(t *testing.T) {
	var c *TelegramChannel
	reply := &telego.Message{
		Text: "ctx",
		From: &telego.User{ID: 12, FirstName: "Bob", Username: "bobby"},
	}
	want := "[quoted user message from bobby]: ctx\n\nq"
	if got := c.prependTelegramQuotedReply("q", reply); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPrependTelegramQuotedReply_EmptyCurrentContent(t *testing.T) {
	var c *TelegramChannel
	reply := &telego.Message{
		Caption: "caption context",
		From:    &telego.User{ID: 14, FirstName: "Dave"},
	}
	want := "[quoted user message from Dave]: caption context"
	if got := c.prependTelegramQuotedReply("", reply); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPrependTelegramQuotedReply_NoQuotableContent(t *testing.T) {
	var c *TelegramChannel
	reply := &telego.Message{From: &telego.User{ID: 1, FirstName: "X"}}
	if got := c.prependTelegramQuotedReply("keep me", reply); got != "keep me" {
		t.Fatalf("got %q, want %q", got, "keep me")
	}
}

func TestTelegramQuotedRole_Classification(t *testing.T) {
	var c *TelegramChannel

	if got := c.telegramQuotedRole(nil); got != "unknown" {
		t.Fatalf("nil message: got %q", got)
	}
	if got := c.telegramQuotedRole(&telego.Message{From: &telego.User{ID: 1}}); got != "user" {
		t.Fatalf("user: got %q", got)
	}
	if got := c.telegramQuotedRole(&telego.Message{From: &telego.User{ID: 2, IsBot: true}}); got != "bot" {
		t.Fatalf("other bot: got %q", got)
	}
	if got := c.telegramQuotedRole(&telego.Message{SenderChat: &telego.Chat{ID: 5}}); got != "chat" {
		t.Fatalf("sender chat: got %q", got)
	}
	if got := c.telegramQuotedRole(&telego.Message{}); got != "unknown" {
		t.Fatalf("no sender: got %q", got)
	}
}

func TestQuotedTelegramMediaRefs_ResolvesVoiceAndAudio(t *testing.T) {
	msg := &telego.Message{
		Voice: &telego.Voice{FileID: "v1"},
		Audio: &telego.Audio{FileID: "a1"},
	}

	var seen []string
	resolve := func(fileID, ext string) string {
		seen = append(seen, fileID+ext)
		return "/tmp/" + fileID + ext
	}

	refs := quotedTelegramMediaRefs(msg, resolve)
	want := []string{"/tmp/v1.ogg", "/tmp/a1.mp3"}
	if len(refs) != len(want) {
		t.Fatalf("got %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("refs[%d]: got %q, want %q", i, refs[i], want[i])
		}
	}
	if len(seen) != 2 {
		t.Fatalf("resolver called %d times, want 2", len(seen))
	}
}

func TestQuotedTelegramMediaRefs_SkipsFailedResolution(t *testing.T) {
	msg := &telego.Message{Voice: &telego.Voice{FileID: "v1"}}
	resolve := func(fileID, ext string) string { return "" }

	if refs := quotedTelegramMediaRefs(msg, resolve); len(refs) != 0 {
		t.Fatalf("got %v, want no refs", refs)
	}
}

func TestQuotedTelegramMediaRefs_NilInputs(t *testing.T) {
	if refs := quotedTelegramMediaRefs(nil, func(string, string) string { return "x" }); refs != nil {
		t.Fatalf("got %v, want nil", refs)
	}
	if refs := quotedTelegramMediaRefs(&telego.Message{}, nil); refs != nil {
		t.Fatalf("got %v, want nil", refs)
	}
}
