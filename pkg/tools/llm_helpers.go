package tools

import (
	"encoding/json"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// NormalizeToolCalls normalizes a slice of raw tool calls from an LLM response.
func NormalizeToolCalls(raw []providers.ToolCall) []providers.ToolCall {
	normalized := make([]providers.ToolCall, 0, len(raw))
	for _, tc := range raw {
		normalized = append(normalized, providers.NormalizeToolCall(tc))
	}
	return normalized
}

// BuildAssistantMessage constructs an assistant message with tool calls for the conversation history.
func BuildAssistantMessage(content string, toolCalls []providers.ToolCall) providers.Message {
	msg := providers.Message{
		Role:    "assistant",
		Content: content,
	}
	for _, tc := range toolCalls {
		argumentsJSON, _ := json.Marshal(tc.Arguments)
		extraContent := tc.ExtraContent
		thoughtSignature := ""
		if tc.Function != nil {
			thoughtSignature = tc.Function.ThoughtSignature
		}
		msg.ToolCalls = append(msg.ToolCalls, providers.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Name: tc.Name,
			Function: &providers.FunctionCall{
				Name:             tc.Name,
				Arguments:        string(argumentsJSON),
				ThoughtSignature: thoughtSignature,
			},
			ExtraContent:     extraContent,
			ThoughtSignature: thoughtSignature,
		})
	}
	return msg
}

// ToolCallLogNames extracts tool names from a slice of tool calls for logging.
func ToolCallLogNames(toolCalls []providers.ToolCall) []string {
	names := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		names = append(names, tc.Name)
	}
	return names
}

// FormatToolCallPreview formats a tool call as "name(args_preview)" for logging.
func FormatToolCallPreview(tc providers.ToolCall) string {
	argsJSON, _ := json.Marshal(tc.Arguments)
	if len(argsJSON) > 200 {
		return fmt.Sprintf("%s(%s...)", tc.Name, string(argsJSON[:200]))
	}
	return fmt.Sprintf("%s(%s)", tc.Name, string(argsJSON))
}

// ToolNamesString returns a comma-separated list of tool names for logging.
func ToolNamesString(toolCalls []providers.ToolCall) string {
	return fmt.Sprintf("%v", ToolCallLogNames(toolCalls))
}

// BuildToolResultMessage constructs a tool result message for the conversation history.
func BuildToolResultMessage(toolCallID, content string) providers.Message {
	return providers.Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: toolCallID,
	}
}
