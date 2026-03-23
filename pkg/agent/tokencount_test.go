package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func TestCountTokens_NonEmpty(t *testing.T) {
	result := countTokens("Hello, world!")
	if result <= 0 {
		t.Errorf("expected countTokens(\"Hello, world!\") > 0, got %d", result)
	}
}

func TestCountTokens_Empty(t *testing.T) {
	result := countTokens("")
	if result != 0 {
		t.Errorf("expected countTokens(\"\") == 0, got %d", result)
	}
}

func TestCountMessageTokens_CountsAllFields(t *testing.T) {
	messages := []protocoltypes.Message{
		{
			Role:             "user",
			Content:          "Hello from content",
			ReasoningContent: "Hello from reasoning",
			ToolCalls: []protocoltypes.ToolCall{
				{
					Function: &protocoltypes.FunctionCall{
						Name:      "some_tool",
						Arguments: `{"key": "value"}`,
					},
				},
			},
		},
	}

	// Count tokens for each field individually (before safety margin).
	contentTokens := countTokens("Hello from content")
	reasoningTokens := countTokens("Hello from reasoning")
	argsTokens := countTokens(`{"key": "value"}`)
	baseTotal := contentTokens + reasoningTokens + argsTokens
	expected := baseTotal * 110 / 100

	result := countMessageTokens(messages)
	if result != expected {
		t.Errorf("expected countMessageTokens to return %d, got %d", expected, result)
	}
}

func TestCountMessageTokens_NilFunctionNoPanic(t *testing.T) {
	messages := []protocoltypes.Message{
		{
			Role:    "assistant",
			Content: "response",
			ToolCalls: []protocoltypes.ToolCall{
				{
					// Function is nil — must not panic
					Function: nil,
				},
			},
		},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("countMessageTokens panicked on nil Function: %v", r)
		}
	}()

	result := countMessageTokens(messages)
	if result <= 0 {
		t.Errorf("expected result > 0 for non-empty content, got %d", result)
	}
}

func TestCountMessageTokens_SafetyMarginApplied(t *testing.T) {
	// Use a message where we can verify the 10% margin is applied.
	messages := []protocoltypes.Message{
		{
			Role:    "user",
			Content: "test margin",
		},
	}

	base := countTokens("test margin")
	expected := base * 110 / 100

	result := countMessageTokens(messages)
	if result != expected {
		t.Errorf("safety margin not applied correctly: expected %d (base=%d * 110/100), got %d", expected, base, result)
	}
}
