package session

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// CompressForLLM reduces token usage in message history by stripping reasoning
// content, truncating large tool arguments/results, and collapsing old tool
// call/result pairs into summary messages.
func CompressForLLM(messages []protocoltypes.Message) []protocoltypes.Message {
	if len(messages) <= 10 {
		return messages
	}

	// Find index of the boundary: messages older than the most recent 10
	boundary := len(messages) - 10

	// Find the most recent assistant message index across all messages
	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	// Make a shallow copy of all messages
	result := make([]protocoltypes.Message, len(messages))
	copy(result, messages)

	// Apply standard compression to messages older than the most recent 10
	for i := 0; i < boundary; i++ {
		msg := result[i]

		if msg.Role == "assistant" {
			// Strip reasoning from all assistant messages except the most recent
			if i != lastAssistantIdx {
				msg.ReasoningContent = ""
				msg.ReasoningDetails = nil
			}

			// Truncate long tool call arguments
			if len(msg.ToolCalls) > 0 {
				newToolCalls := make([]protocoltypes.ToolCall, len(msg.ToolCalls))
				copy(newToolCalls, msg.ToolCalls)
				for j, tc := range newToolCalls {
					if tc.Function != nil && len(tc.Function.Arguments) > 200 {
						argLen := len(tc.Function.Arguments)
						fc := *tc.Function
						fc.Arguments = fmt.Sprintf("[args: %d chars]", argLen)
						newToolCalls[j].Function = &fc
					}
				}
				msg.ToolCalls = newToolCalls
			}
		}

		// Truncate long tool result content (role == "tool")
		if msg.Role == "tool" && len(msg.Content) > 500 {
			content := msg.Content
			total := len(content)
			msg.Content = content[:200] + fmt.Sprintf("\n...[%d chars truncated]...\n", total-400) + content[total-200:]
		}

		result[i] = msg
	}

	// Aggressive compression: collapse old tool call+result pairs (>20 turns old)
	aggressiveBoundary := len(messages) - 20
	if aggressiveBoundary > 0 {
		result = collapseToolPairs(result, aggressiveBoundary)
	}

	return result
}

// collapseToolPairs collapses consecutive assistant(tool_call)+tool(result)
// pairs within [0, boundary) into single summary messages.
func collapseToolPairs(messages []protocoltypes.Message, boundary int) []protocoltypes.Message {
	out := make([]protocoltypes.Message, 0, len(messages))
	i := 0
	for i < len(messages) {
		msg := messages[i]

		// Only collapse within the aggressive boundary
		if i < boundary && msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Look ahead for a following tool result message
			if i+1 < len(messages) && messages[i+1].Role == "tool" {
				toolName := ""
				if len(msg.ToolCalls) > 0 && msg.ToolCalls[0].Function != nil {
					toolName = msg.ToolCalls[0].Function.Name
				}

				resultContent := messages[i+1].Content
				preview := resultContent
				if len(preview) > 80 {
					preview = preview[:80]
				}

				collapsed := protocoltypes.Message{
					Role:    "assistant",
					Content: fmt.Sprintf("[Used %s → %s]", toolName, preview),
				}
				out = append(out, collapsed)
				i += 2 // skip the tool result message too
				continue
			}
		}

		out = append(out, msg)
		i++
	}
	return out
}
