package trace

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// TurnTraceCollector incrementally collects a single turn trace.
type TurnTraceCollector struct {
	mu        sync.Mutex
	trace     *TurnTrace
	finalized bool
}

func NewTurnTraceCollector(sessionKey, agentID, channel, chatID, userMessage string) *TurnTraceCollector {
	now := time.Now().UTC()
	return &TurnTraceCollector{
		trace: &TurnTrace{
			ID:               "trace_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			SessionKey:       sessionKey,
			AgentID:          agentID,
			Channel:          channel,
			ChatID:           chatID,
			Timestamp:        now,
			UserMessage:      userMessage,
			UserMessageChars: len(userMessage),
			ExitReason:       ExitReasonUnknown,
		},
	}
}

func (c *TurnTraceCollector) SetSystemPromptChars(chars int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace.SystemPromptChars = chars
}

func (c *TurnTraceCollector) SetInjectedMemoryIDs(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace.InjectedMemoryIDs = cloneStrings(ids)
}

func (c *TurnTraceCollector) SetInjectedLearningIDs(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace.InjectedLearningIDs = cloneStrings(ids)
}

func (c *TurnTraceCollector) SetOutcome(outcomeDetected, outcomeLessonID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace.OutcomeDetected = outcomeDetected
	c.trace.OutcomeLessonID = outcomeLessonID
}

func (c *TurnTraceCollector) SetUserFollowup(corrected bool, nextMessage *string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace.UserCorrected = corrected
	if nextMessage == nil {
		c.trace.UserNextMessage = nil
		return
	}
	msg := *nextMessage
	c.trace.UserNextMessage = &msg
}

func (c *TurnTraceCollector) RecordToolCall(name string, args map[string]any, result string, isError bool, durationMs int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace.ToolCalls = append(c.trace.ToolCalls, ToolCallTrace{
		Name:       name,
		Args:       cloneMap(args),
		Result:     result,
		IsError:    isError,
		Timestamp:  time.Now().UTC(),
		DurationMs: durationMs,
	})
}

func (c *TurnTraceCollector) RecordLLMCall(model, provider string, tokens int, durationMs int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	call := LLMCallTrace{
		Model:      model,
		Provider:   provider,
		Tokens:     tokens,
		Timestamp:  time.Now().UTC(),
		DurationMs: durationMs,
	}
	c.trace.LLMCalls = append(c.trace.LLMCalls, call)
	c.trace.LLMModel = model
	c.trace.LLMProvider = provider
	c.trace.LLMIterations++
	c.trace.LLMTotalTokens += tokens
	c.trace.LLMTotalDurationMs += durationMs
}

func (c *TurnTraceCollector) RecordFallbackAttempts(attempts []providers.FallbackAttempt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, attempt := range attempts {
		traceAttempt := FallbackAttemptTrace{
			Provider:   attempt.Provider,
			Model:      attempt.Model,
			Reason:     string(attempt.Reason),
			Timestamp:  time.Now().UTC(),
			DurationMs: attempt.Duration.Milliseconds(),
			Skipped:    attempt.Skipped,
		}
		if attempt.Error != nil {
			traceAttempt.Error = attempt.Error.Error()
		}
		c.trace.FallbackAttempts = append(c.trace.FallbackAttempts, traceAttempt)
	}
}

func (c *TurnTraceCollector) Finalize(finalContent string, usedDefault bool, iteration int, err error) {
	c.FinalizeWithExitReason(finalContent, usedDefault, iteration, "", err)
}

func (c *TurnTraceCollector) FinalizeWithExitReason(
	finalContent string,
	usedDefault bool,
	iteration int,
	exitReason string,
	err error,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finalized {
		return
	}
	c.finalized = true
	c.trace.FinalResponse = finalContent
	c.trace.FinalResponseChars = len(finalContent)
	c.trace.DefaultResponseUsed = usedDefault
	if iteration > c.trace.LLMIterations {
		c.trace.LLMIterations = iteration
	}
	c.trace.ExitReason = resolveExitReason(exitReason, usedDefault, err)
}

func (c *TurnTraceCollector) Build() *TurnTrace {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneTurnTrace(c.trace)
}

func inferExitReason(usedDefault bool, err error) string {
	return resolveExitReason("", usedDefault, err)
}

func resolveExitReason(explicit string, usedDefault bool, err error) string {
	if usedDefault {
		return ExitReasonDefaultResponse
	}
	if errors.Is(err, context.Canceled) {
		return ExitReasonContextCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ExitReasonContextDeadline
	}
	if isExplicitExitReason(explicit) {
		return explicit
	}
	if err == nil {
		return ExitReasonSuccess
	}
	return ExitReasonError
}

func isExplicitExitReason(reason string) bool {
	switch reason {
	case ExitReasonSuccess,
		ExitReasonDefaultResponse,
		ExitReasonLLMError,
		ExitReasonToolError,
		ExitReasonError,
		ExitReasonContextCancelled,
		ExitReasonContextDeadline,
		ExitReasonUnknown:
		return true
	default:
		return false
	}
}

func cloneTurnTrace(src *TurnTrace) *TurnTrace {
	if src == nil {
		return nil
	}
	dst := *src
	dst.InjectedMemoryIDs = cloneStrings(src.InjectedMemoryIDs)
	dst.InjectedLearningIDs = cloneStrings(src.InjectedLearningIDs)
	dst.ToolCalls = cloneToolCalls(src.ToolCalls)
	dst.LLMCalls = cloneLLMCalls(src.LLMCalls)
	dst.FallbackAttempts = cloneFallbackAttempts(src.FallbackAttempts)
	if src.UserNextMessage != nil {
		msg := *src.UserNextMessage
		dst.UserNextMessage = &msg
	}
	return &dst
}

func cloneStrings(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneToolCalls(src []ToolCallTrace) []ToolCallTrace {
	if len(src) == 0 {
		return nil
	}
	dst := make([]ToolCallTrace, len(src))
	copy(dst, src)
	for i := range dst {
		dst[i].Args = cloneMap(src[i].Args)
	}
	return dst
}

func cloneLLMCalls(src []LLMCallTrace) []LLMCallTrace {
	if len(src) == 0 {
		return nil
	}
	dst := make([]LLMCallTrace, len(src))
	copy(dst, src)
	return dst
}

func cloneFallbackAttempts(src []FallbackAttemptTrace) []FallbackAttemptTrace {
	if len(src) == 0 {
		return nil
	}
	dst := make([]FallbackAttemptTrace, len(src))
	copy(dst, src)
	return dst
}
