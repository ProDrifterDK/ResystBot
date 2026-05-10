// PicoClaw - Hooks system
package hooks

import (
	"encoding/json"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/config"
)

type HookEvent string

const (
	PreToolUse       HookEvent = "PreToolUse"
	PostToolUse      HookEvent = "PostToolUse"
	SessionStart     HookEvent = "SessionStart"
	PreCompact       HookEvent = "PreCompact"
	UserPromptSubmit HookEvent = "UserPromptSubmit"
)

type HookDecision string

const (
	DecisionAllow    HookDecision = "allow"
	DecisionBlock    HookDecision = "block"
	DecisionRedirect HookDecision = "redirect"
)

type HookInput struct {
	Event          HookEvent      `json:"event"`
	ToolName       string         `json:"tool_name,omitempty"`
	ToolInput      map[string]any `json:"tool_input,omitempty"`
	ToolResponse   string         `json:"tool_response,omitempty"`
	ToolSuccess    bool           `json:"tool_success"`
	ToolIsError    bool           `json:"tool_is_error"`
	SessionID      string         `json:"session_id,omitempty"`
	UserPrompt     string         `json:"user_prompt,omitempty"`
	CompactContext string         `json:"compact_context,omitempty"`
}

type HookOutput struct {
	Decision         HookDecision   `json:"decision,omitempty"`
	Reason           string         `json:"reason,omitempty"`
	ReplacementTool  string         `json:"replacement_tool,omitempty"`
	ReplacementInput map[string]any `json:"replacement_input,omitempty"`
	ModifiedPrompt   string         `json:"modified_prompt,omitempty"`
	InjectedContext  string         `json:"injected_context,omitempty"`
	SuppressOutput   bool           `json:"suppress_output,omitempty"`
}

type HookResult struct {
	Decision         HookDecision
	Reason           string
	ReplacementTool  string
	ReplacementInput map[string]any
	ModifiedPrompt   string
	InjectedContext  string
	Err              error
}

func IsEmpty(cfg *config.HooksConfig) bool {
	return cfg == nil ||
		(len(cfg.PreToolUse) == 0 &&
			len(cfg.PostToolUse) == 0 &&
			len(cfg.SessionStart) == 0 &&
			len(cfg.PreCompact) == 0 &&
			len(cfg.UserPromptSubmit) == 0)
}

func ParseHookOutput(data []byte) (*HookOutput, error) {
	data = trimWhitespace(data)
	if len(data) == 0 || string(data) == "null" {
		return &HookOutput{Decision: DecisionAllow}, nil
	}

	var output HookOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, fmt.Errorf("hooks: invalid JSON output: %w", err)
	}

	if output.Decision == "" {
		output.Decision = DecisionAllow
	}

	return &output, nil
}

func trimWhitespace(data []byte) []byte {
	start := 0
	end := len(data)
	for start < end && (data[start] == ' ' || data[start] == '\t' || data[start] == '\n' || data[start] == '\r') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\t' || data[end-1] == '\n' || data[end-1] == '\r') {
		end--
	}
	return data[start:end]
}
