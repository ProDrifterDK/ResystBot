package protocoltypes

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	xmlToolCallRegex = regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)
)

// ExtractToolCallsFromText parses tool call JSON from response text.
func ExtractToolCallsFromText(text string) []ToolCall {
	start := strings.Index(text, `{"tool_calls"`)
	if start == -1 {
		return nil
	}

	end := FindMatchingBrace(text, start)
	if end == start {
		return nil
	}

	jsonStr := text[start:end]

	var wrapper struct {
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &wrapper); err != nil {
		return nil
	}

	var result []ToolCall
	for _, tc := range wrapper.ToolCalls {
		var args map[string]any
		json.Unmarshal([]byte(tc.Function.Arguments), &args)

		result = append(result, ToolCall{
			ID:        tc.ID,
			Type:      tc.Type,
			Name:      tc.Function.Name,
			Arguments: args,
			Function: &FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}

	return result
}

// ExtractXMLToolCalls parses tool calls found within <tool_call> tags.
// Format: <tool_call> {"name": "...", "arguments": {...}} </tool_call>
func ExtractXMLToolCalls(text string) []ToolCall {
	matches := xmlToolCallRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	var result []ToolCall
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := match[1]

		var tc struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}

		if err := json.Unmarshal([]byte(jsonStr), &tc); err != nil {
			// Try to handle arguments as string if map fails
			var tcStr struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal([]byte(jsonStr), &tcStr); err == nil {
				var args map[string]any
				json.Unmarshal([]byte(tcStr.Arguments), &args)
				result = append(result, ToolCall{
					Name:      tcStr.Name,
					Arguments: args,
					Function: &FunctionCall{
						Name:      tcStr.Name,
						Arguments: tcStr.Arguments,
					},
				})
			}
			continue
		}

		argsBytes, _ := json.Marshal(tc.Arguments)
		result = append(result, ToolCall{
			Name:      tc.Name,
			Arguments: tc.Arguments,
			Function: &FunctionCall{
				Name:      tc.Name,
				Arguments: string(argsBytes),
			},
		})
	}

	return result
}

// FindMatchingBrace finds the index after the closing brace matching the opening brace at pos.
func FindMatchingBrace(text string, pos int) int {
	if pos >= len(text) || text[pos] != '{' {
		return pos
	}

	balance := 0
	for i := pos; i < len(text); i++ {
		switch text[i] {
		case '{':
			balance++
		case '}':
			balance--
		}
		if balance == 0 {
			return i + 1 // Return index after the closing brace
		}
	}
	return pos // No matching brace found
}

// StripToolCallsFromText removes tool call JSON and XML from response text.
func StripToolCallsFromText(text string) string {
	// Strip JSON tool calls
	start := strings.Index(text, `{"tool_calls"`)
	if start != -1 {
		end := FindMatchingBrace(text, start)
		if end != start {
			text = text[:start] + text[end:]
		}
	}

	// Strip XML tool calls
	text = xmlToolCallRegex.ReplaceAllString(text, "")

	return strings.TrimSpace(text)
}
