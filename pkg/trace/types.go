package trace

import "time"

const (
	ExitReasonSuccess          = "success"
	ExitReasonDefaultResponse  = "default_response"
	ExitReasonLLMError         = "llm_error"
	ExitReasonToolError        = "tool_error"
	ExitReasonError            = "error"
	ExitReasonContextCancelled = "context_cancelled"
	ExitReasonContextCanceled  = ExitReasonContextCancelled
	ExitReasonContextDeadline  = "context_deadline_exceeded"
	ExitReasonUnknown          = "unknown"
)

// TurnTrace captures one full turn of agent execution for append-only JSONL storage.
type TurnTrace struct {
	ID                  string                 `json:"id"`
	SessionKey          string                 `json:"session_key"`
	AgentID             string                 `json:"agent_id"`
	Channel             string                 `json:"channel"`
	ChatID              string                 `json:"chat_id"`
	Timestamp           time.Time              `json:"timestamp"`
	UserMessage         string                 `json:"user_message"`
	UserMessageChars    int                    `json:"user_message_chars"`
	SystemPromptChars   int                    `json:"system_prompt_chars"`
	InjectedMemoryIDs   []string               `json:"injected_memory_ids,omitempty"`
	InjectedLearningIDs []string               `json:"injected_learning_ids,omitempty"`
	LLMModel            string                 `json:"llm_model,omitempty"`
	LLMProvider         string                 `json:"llm_provider,omitempty"`
	LLMIterations       int                    `json:"llm_iterations"`
	LLMTotalTokens      int                    `json:"llm_total_tokens"`
	LLMTotalDurationMs  int64                  `json:"llm_total_duration_ms"`
	LLMCalls            []LLMCallTrace         `json:"llm_calls,omitempty"`
	FallbackAttempts    []FallbackAttemptTrace `json:"fallback_attempts,omitempty"`
	ToolCalls           []ToolCallTrace        `json:"tool_calls,omitempty"`
	FinalResponse       string                 `json:"final_response"`
	FinalResponseChars  int                    `json:"final_response_chars"`
	DefaultResponseUsed bool                   `json:"default_response_used"`
	ExitReason          string                 `json:"exit_reason"`
	OutcomeDetected     string                 `json:"outcome_detected,omitempty"`
	OutcomeLessonID     string                 `json:"outcome_lesson_id,omitempty"`
	UserCorrected       bool                   `json:"user_corrected"`
	UserNextMessage     *string                `json:"user_next_message"`
}

type ToolCallTrace struct {
	Name       string         `json:"name"`
	Args       map[string]any `json:"args,omitempty"`
	Result     string         `json:"result"`
	IsError    bool           `json:"is_error"`
	Timestamp  time.Time      `json:"timestamp"`
	DurationMs int64          `json:"duration_ms"`
}

type LLMCallTrace struct {
	Model      string    `json:"model"`
	Provider   string    `json:"provider"`
	Tokens     int       `json:"tokens"`
	Timestamp  time.Time `json:"timestamp"`
	DurationMs int64     `json:"duration_ms"`
}

type FallbackAttemptTrace struct {
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	Error      string    `json:"error,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
	DurationMs int64     `json:"duration_ms"`
	Skipped    bool      `json:"skipped"`
}
