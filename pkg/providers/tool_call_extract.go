package providers

import (
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// ExtractToolCallsFromText parses tool call JSON from response text.
func ExtractToolCallsFromText(text string) []ToolCall {
	return protocoltypes.ExtractToolCallsFromText(text)
}

// ExtractXMLToolCalls parses tool calls found within <tool_call> tags.
func ExtractXMLToolCalls(text string) []ToolCall {
	return protocoltypes.ExtractXMLToolCalls(text)
}

// StripToolCallsFromText removes tool call JSON and XML from response text.
func StripToolCallsFromText(text string) string {
	return protocoltypes.StripToolCallsFromText(text)
}

// FindMatchingBrace finds the index after the closing brace matching the opening brace at pos.
func FindMatchingBrace(text string, pos int) int {
	return protocoltypes.FindMatchingBrace(text, pos)
}
