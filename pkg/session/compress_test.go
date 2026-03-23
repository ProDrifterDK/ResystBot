package session

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func makeMsg(role, content string) protocoltypes.Message {
	return protocoltypes.Message{Role: role, Content: content}
}

func makeAssistantWithReasoning(content, reasoning string) protocoltypes.Message {
	return protocoltypes.Message{
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoning,
		ReasoningDetails: "some-details",
	}
}

func makeAssistantWithToolCall(toolName, args string) protocoltypes.Message {
	return protocoltypes.Message{
		Role: "assistant",
		ToolCalls: []protocoltypes.ToolCall{
			{
				ID:   "tc1",
				Type: "function",
				Function: &protocoltypes.FunctionCall{
					Name:      toolName,
					Arguments: args,
				},
			},
		},
	}
}

func makeAssistantWithNilFunction() protocoltypes.Message {
	return protocoltypes.Message{
		Role: "assistant",
		ToolCalls: []protocoltypes.ToolCall{
			{
				ID:   "tc2",
				Type: "function",
				// Function is nil intentionally
			},
		},
	}
}

func makeToolResult(content string) protocoltypes.Message {
	return protocoltypes.Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: "tc1",
	}
}

// buildMessages creates a message slice of the given length, filling old
// messages with the provided list and padding with simple user/assistant pairs.
func buildMessages(old []protocoltypes.Message, recentCount int) []protocoltypes.Message {
	msgs := make([]protocoltypes.Message, 0, len(old)+recentCount)
	msgs = append(msgs, old...)
	for i := 0; i < recentCount; i++ {
		if i%2 == 0 {
			msgs = append(msgs, makeMsg("user", "recent user message"))
		} else {
			msgs = append(msgs, makeMsg("assistant", "recent assistant message"))
		}
	}
	return msgs
}

// TestCompressPreservesRecent checks that <= 10 messages are returned unchanged.
func TestCompressPreservesRecent(t *testing.T) {
	msgs := []protocoltypes.Message{
		makeMsg("user", "hello"),
		makeMsg("assistant", "world"),
	}
	result := CompressForLLM(msgs)
	if len(result) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(result))
	}
	for i := range msgs {
		if result[i].Content != msgs[i].Content {
			t.Errorf("message %d content changed: want %q got %q", i, msgs[i].Content, result[i].Content)
		}
	}

	// Exactly 10 messages should also be unchanged
	ten := make([]protocoltypes.Message, 10)
	for i := range ten {
		ten[i] = makeMsg("user", "msg")
	}
	result10 := CompressForLLM(ten)
	if len(result10) != 10 {
		t.Fatalf("expected 10 messages, got %d", len(result10))
	}
}

// TestCompressTruncatesOldToolArguments checks that arguments >200 chars are truncated.
func TestCompressTruncatesOldToolArguments(t *testing.T) {
	longArgs := strings.Repeat("x", 250)
	old := []protocoltypes.Message{
		makeAssistantWithToolCall("myTool", longArgs),
	}
	msgs := buildMessages(old, 10)
	result := CompressForLLM(msgs)

	// The first message should have truncated args
	tc := result[0].ToolCalls[0]
	if tc.Function == nil {
		t.Fatal("Function should not be nil after compression")
	}
	if tc.Function.Arguments == longArgs {
		t.Error("Expected arguments to be truncated but they were not")
	}
	if !strings.HasPrefix(tc.Function.Arguments, "[args:") {
		t.Errorf("Expected truncated args placeholder, got: %q", tc.Function.Arguments)
	}
	// Original message in msgs should be unmodified (we work on a copy)
	if msgs[0].ToolCalls[0].Function.Arguments != longArgs {
		t.Error("Original message should not be modified")
	}
}

// TestCompressTruncatesOldToolResults checks that tool results >500 chars are truncated.
func TestCompressTruncatesOldToolResults(t *testing.T) {
	longContent := strings.Repeat("a", 100) + strings.Repeat("b", 400) + strings.Repeat("c", 100)
	old := []protocoltypes.Message{
		makeToolResult(longContent),
	}
	msgs := buildMessages(old, 10)
	result := CompressForLLM(msgs)

	content := result[0].Content
	if content == longContent {
		t.Error("Expected content to be truncated but it was not")
	}
	if !strings.Contains(content, "truncated") {
		t.Errorf("Expected truncation marker in content, got: %q", content)
	}
	// Should keep first 200 and last 200 chars
	if !strings.HasPrefix(content, strings.Repeat("a", 100)) {
		t.Error("Expected first 200 chars to be preserved")
	}
	if !strings.HasSuffix(content, strings.Repeat("c", 100)) {
		t.Error("Expected last 200 chars to be preserved")
	}
}

// TestCompressStripsReasoningFromOldButPreservesMostRecent checks reasoning
// handling.
func TestCompressStripsReasoningFromOldButPreservesMostRecent(t *testing.T) {
	old := []protocoltypes.Message{
		makeAssistantWithReasoning("old response", "old reasoning"),
	}
	// The most recent assistant is in the "recent 10" block
	recent := []protocoltypes.Message{
		makeMsg("user", "r1"),
		makeAssistantWithReasoning("new response", "new reasoning"),
		makeMsg("user", "r2"),
		makeMsg("user", "r3"),
		makeMsg("user", "r4"),
		makeMsg("user", "r5"),
		makeMsg("user", "r6"),
		makeMsg("user", "r7"),
		makeMsg("user", "r8"),
		makeMsg("user", "r9"),
	}
	msgs := append(old, recent...)
	result := CompressForLLM(msgs)

	// Old assistant message should have reasoning stripped
	if result[0].ReasoningContent != "" {
		t.Errorf("Expected reasoning stripped from old message, got: %q", result[0].ReasoningContent)
	}
	if result[0].ReasoningDetails != nil {
		t.Error("Expected ReasoningDetails nil for old message")
	}

	// Recent assistant message should keep reasoning
	recentAssistantIdx := 2 // index in result (1 old + index 1 in recent)
	if result[recentAssistantIdx].ReasoningContent != "new reasoning" {
		t.Errorf("Expected most recent assistant to keep reasoning, got: %q", result[recentAssistantIdx].ReasoningContent)
	}
}

// TestCompressHandlesNilFunction ensures no panic when ToolCall.Function is nil.
func TestCompressHandlesNilFunction(t *testing.T) {
	old := []protocoltypes.Message{
		makeAssistantWithNilFunction(),
	}
	msgs := buildMessages(old, 10)

	// Should not panic
	result := CompressForLLM(msgs)
	if len(result) == 0 {
		t.Error("Expected non-empty result")
	}
	// Function remains nil
	if result[0].ToolCalls[0].Function != nil {
		t.Error("Expected Function to remain nil")
	}
}

// TestCompressAggressiveCollapsesOldToolPairs checks that tool call+result
// pairs older than 20 turns are collapsed into a summary message.
func TestCompressAggressiveCollapsesOldToolPairs(t *testing.T) {
	resultContent := strings.Repeat("r", 120) // 120 chars, preview truncates at 80
	old := []protocoltypes.Message{
		makeAssistantWithToolCall("search", `{"query":"foo"}`),
		makeToolResult(resultContent),
	}
	// Pad to push old messages beyond the 20-turn boundary
	msgs := buildMessages(old, 20)

	result := CompressForLLM(msgs)

	// The old tool call + result pair (2 messages) should be collapsed to 1
	// So total = len(msgs) - 1
	if len(result) != len(msgs)-1 {
		t.Errorf("Expected %d messages after collapse, got %d", len(msgs)-1, len(result))
	}

	// Collapsed message should be an assistant message with summary
	first := result[0]
	if first.Role != "assistant" {
		t.Errorf("Expected collapsed message role 'assistant', got %q", first.Role)
	}
	if !strings.HasPrefix(first.Content, "[Used search →") {
		t.Errorf("Expected collapsed content to start with '[Used search →', got: %q", first.Content)
	}
	// Preview should be max 80 chars
	if strings.Contains(first.Content, resultContent) {
		t.Error("Full result should not appear in collapsed message")
	}
}
