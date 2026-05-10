package trace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestTurnTraceCollectorFinalizeOnce(t *testing.T) {
	collector := NewTurnTraceCollector("telegram:123", "main", "telegram", "123", "hello")
	collector.SetInjectedLearningIDs([]string{"learn_1"})
	collector.RecordLLMCall("model-a", "provider-a", 42, 99)
	collector.RecordToolCall("exec", map[string]any{"command": "true"}, "ok", false, 12)
	collector.RecordFallbackAttempts([]providers.FallbackAttempt{{
		Provider: "openrouter",
		Model:    "openrouter/qwen3-coder",
		Reason:   providers.FailoverRateLimit,
		Error:    errors.New("rate limit"),
		Duration: 1500 * time.Millisecond,
	}})

	collector.Finalize("first", false, 1, context.Canceled)
	collector.Finalize("second", true, 9, nil)

	trace := collector.Build()
	if trace.FinalResponse != "first" {
		t.Fatalf("final response = %q, want first", trace.FinalResponse)
	}
	if trace.DefaultResponseUsed {
		t.Fatalf("default response used = true, want false")
	}
	if trace.ExitReason != ExitReasonContextCancelled {
		t.Fatalf("exit reason = %q, want %q", trace.ExitReason, ExitReasonContextCancelled)
	}
	if trace.LLMIterations != 1 {
		t.Fatalf("llm iterations = %d, want 1", trace.LLMIterations)
	}
	if len(trace.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(trace.ToolCalls))
	}
	if len(trace.LLMCalls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(trace.LLMCalls))
	}
	if len(trace.FallbackAttempts) != 1 {
		t.Fatalf("fallback attempts = %d, want 1", len(trace.FallbackAttempts))
	}
	if got := trace.FallbackAttempts[0].Reason; got != string(providers.FailoverRateLimit) {
		t.Fatalf("fallback reason = %q, want %q", got, providers.FailoverRateLimit)
	}
	if trace.UserMessageChars != len("hello") {
		t.Fatalf("user message chars = %d, want %d", trace.UserMessageChars, len("hello"))
	}
	if trace.FinalResponseChars != len("first") {
		t.Fatalf("final response chars = %d, want %d", trace.FinalResponseChars, len("first"))
	}
}

func TestInferExitReason(t *testing.T) {
	tests := []struct {
		name        string
		usedDefault bool
		err         error
		want        string
	}{
		{name: "success", want: ExitReasonSuccess},
		{name: "default response", usedDefault: true, want: ExitReasonDefaultResponse},
		{name: "context canceled", err: context.Canceled, want: ExitReasonContextCancelled},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: ExitReasonContextDeadline},
		{name: "generic error", err: errors.New("boom"), want: ExitReasonError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferExitReason(tc.usedDefault, tc.err); got != tc.want {
				t.Fatalf("inferExitReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTurnTraceCollectorFinalizeWithExitReasonPreservesExplicitReason(t *testing.T) {
	tests := []struct {
		name       string
		exitReason string
	}{
		{name: "llm error", exitReason: ExitReasonLLMError},
		{name: "tool error", exitReason: ExitReasonToolError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			collector := NewTurnTraceCollector("telegram:123", "main", "telegram", "123", "hello")
			collector.FinalizeWithExitReason("first", false, 2, tc.exitReason, errors.New("boom"))
			collector.FinalizeWithExitReason("second", false, 9, ExitReasonSuccess, nil)

			trace := collector.Build()
			if trace.ExitReason != tc.exitReason {
				t.Fatalf("exit reason = %q, want %q", trace.ExitReason, tc.exitReason)
			}
			if trace.FinalResponse != "first" {
				t.Fatalf("final response = %q, want first", trace.FinalResponse)
			}
			if trace.LLMIterations != 2 {
				t.Fatalf("llm iterations = %d, want 2", trace.LLMIterations)
			}
		})
	}
}

func TestFinalizeWithExitReasonStillPrefersDefaultAndContext(t *testing.T) {
	collector := NewTurnTraceCollector("telegram:123", "main", "telegram", "123", "hello")
	collector.FinalizeWithExitReason("fallback", true, 1, ExitReasonLLMError, errors.New("boom"))
	if got := collector.Build().ExitReason; got != ExitReasonDefaultResponse {
		t.Fatalf("default response exit reason = %q, want %q", got, ExitReasonDefaultResponse)
	}

	collector = NewTurnTraceCollector("telegram:123", "main", "telegram", "123", "hello")
	collector.FinalizeWithExitReason("cancelled", false, 1, ExitReasonToolError, context.Canceled)
	if got := collector.Build().ExitReason; got != ExitReasonContextCancelled {
		t.Fatalf("context exit reason = %q, want %q", got, ExitReasonContextCancelled)
	}
}
