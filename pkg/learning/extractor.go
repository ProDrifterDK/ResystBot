package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/trace"
)

const (
	lessonSourceToolError          = "tool_error"
	lessonSourceToolErrorRecovered = "tool_error_recovered"
	lessonSourceUserCorrection     = "user_correction"

	lessonOutcomeFailure = "failure"
	lessonOutcomeSuccess = "success"
)

type correctionPattern struct {
	re     *regexp.Regexp
	minLen int
}

var correctionPatterns = []correctionPattern{
	{re: regexp.MustCompile(`(?i)\bno[,.]?\s+(.+)`)},
	{re: regexp.MustCompile(`(?i)\bthat(?: was|'?s)?\s+(?:wrong|incorrect|not right|not what I meant)\b`)},
	{re: regexp.MustCompile(`(?i)\bdon'?t\s+(?:do|use|run|call|try)\s+(?:that|this|it)\b`)},
	{re: regexp.MustCompile(`(?i)\buse\s+(.+?)\s+instead\b`)},
	{re: regexp.MustCompile(`(?i)\binstead[,.]?\s+(.+)`)},
	{re: regexp.MustCompile(`(?i)\bactually[,.]?\s+(.+)`), minLen: 20},
	{re: regexp.MustCompile(`(?i)\byou should(?: have|'ve)?\s+(.+)`)},
	{re: regexp.MustCompile(`(?i)\bnext time[,.]?\s*(.+)`)},
	{re: regexp.MustCompile(`(?i)\bthe (?:correct|right|proper) way (?:is|would be)\s+(.+)`)},
}

var infraErrorSubstrings = []string{
	"rate limit",
	"too many requests",
	"status code 429",
	"429",
	"unauthorized",
	"authentication failed",
	"authentication error",
	"auth failed",
	"invalid api key",
	"api key",
	"forbidden",
	"connection refused",
	"connection reset",
	"no such host",
	"network is unreachable",
	"tls handshake timeout",
	"i/o timeout",
	"timeout awaiting response headers",
	"temporary failure in name resolution",
	"qdrant unavailable",
	"qdrant",
	"command blocked by safety guard",
	"path outside working dir",
	"dangerous pattern detected",
	"restrict_to_workspace",
	"sandbox denied",
	"daemon shutting down",
	"shutdown in progress",
	"context canceled",
	"context cancelled",
	"context deadline exceeded",
}

func NewOutcomeExtractor(encoder lessonStore, cfg *config.LearningConfig) *OutcomeExtractor {
	settings := config.LearningConfig{}
	if cfg != nil {
		settings = *cfg
	}
	return &OutcomeExtractor{
		encoder:            encoder,
		config:             settings,
		redactor:           trace.NewRedactor(),
		lastTraceBySession: make(map[string]*trace.TurnTrace),
		now:                time.Now,
	}
}

func (e *OutcomeExtractor) ProcessTrace(ctx context.Context, current *trace.TurnTrace) (*LessonRecord, error) {
	if current == nil {
		return nil, nil
	}
	if e == nil {
		return nil, nil
	}

	currentTrace := cloneTrace(current)
	if currentTrace.Timestamp.IsZero() {
		currentTrace.Timestamp = e.now().UTC()
	}

	e.PruneStaleSessions()

	matchedCorrection, betterApproach := matchCorrection(currentTrace.UserMessage)
	var lesson *LessonRecord
	if matchedCorrection {
		if previous := e.GetAndClearLastTrace(currentTrace.SessionKey); previous != nil {
			lesson = e.buildCorrectionLesson(previous, currentTrace, betterApproach)
		}
	} else {
		lesson = e.extractToolLesson(currentTrace)
	}

	e.SetLastTrace(currentTrace.SessionKey, currentTrace)

	if lesson == nil || e.encoder == nil {
		return lesson, nil
	}
	if err := e.encoder.Store(ctx, lesson); err != nil {
		return lesson, fmt.Errorf("outcome extractor: store lesson: %w", err)
	}
	return lesson, nil
}

func (e *OutcomeExtractor) SetLastTrace(sessionKey string, t *trace.TurnTrace) {
	if e == nil || sessionKey == "" || t == nil {
		return
	}
	e.lastTraceMu.Lock()
	defer e.lastTraceMu.Unlock()
	e.lastTraceBySession[sessionKey] = cloneTrace(t)
}

func (e *OutcomeExtractor) GetAndClearLastTrace(sessionKey string) *trace.TurnTrace {
	if e == nil || sessionKey == "" {
		return nil
	}
	e.lastTraceMu.Lock()
	defer e.lastTraceMu.Unlock()
	t := e.lastTraceBySession[sessionKey]
	delete(e.lastTraceBySession, sessionKey)
	return cloneTrace(t)
}

func (e *OutcomeExtractor) PruneStaleSessions() {
	if e == nil {
		return
	}
	cutoff := e.now().UTC().Add(-time.Duration(e.config.GetCorrectionSessionTTL()) * time.Minute)
	e.lastTraceMu.Lock()
	defer e.lastTraceMu.Unlock()
	for sessionKey, stored := range e.lastTraceBySession {
		if stored == nil || stored.Timestamp.IsZero() || stored.Timestamp.Before(cutoff) {
			delete(e.lastTraceBySession, sessionKey)
		}
	}
}

func (e *OutcomeExtractor) buildCorrectionLesson(previous, current *trace.TurnTrace, betterApproach string) *LessonRecord {
	if previous == nil || current == nil {
		return nil
	}
	if strings.TrimSpace(betterApproach) == "" {
		betterApproach = strings.TrimSpace(current.UserMessage)
	}
	record := &LessonRecord{
		Situation:      strings.TrimSpace(previous.UserMessage),
		Approach:       strings.TrimSpace(previous.FinalResponse),
		Outcome:        lessonOutcomeFailure,
		ErrorMessage:   firstFailureMessage(previous),
		Correction:     strings.TrimSpace(current.UserMessage),
		BetterApproach: strings.TrimSpace(betterApproach),
		Confidence:     0.85,
		Source:         lessonSourceUserCorrection,
		SessionKey:     current.SessionKey,
		AgentID:        current.AgentID,
		TraceID:        previous.ID,
		Tags: []string{
			"correction",
			"append_only",
		},
	}
	if strings.TrimSpace(record.Approach) == "" {
		record.Approach = summarizeTraceApproach(previous)
	}
	return record
}

func (e *OutcomeExtractor) extractToolLesson(current *trace.TurnTrace) *LessonRecord {
	if current == nil || len(current.ToolCalls) == 0 {
		return nil
	}
	if ignoreTraceExitReason(current.ExitReason) {
		return nil
	}
	if len(strings.TrimSpace(current.UserMessage)) < e.config.GetMinUserMessageChars() {
		return nil
	}

	failedIdx, failedCall := firstMeaningfulFailure(current.ToolCalls)
	if failedIdx < 0 || failedCall == nil {
		return nil
	}
	if isInfraFailure(current.ExitReason, failedCall.Result) {
		return nil
	}

	if recovery := findRecoveryCall(current.ToolCalls, failedIdx, failedCall.Name); recovery != nil {
		return &LessonRecord{
			Situation:      strings.TrimSpace(current.UserMessage),
			Approach:       summarizeToolCall(*failedCall),
			Outcome:        lessonOutcomeSuccess,
			ErrorMessage:   strings.TrimSpace(failedCall.Result),
			BetterApproach: summarizeToolCall(*recovery),
			Confidence:     0.75,
			Source:         lessonSourceToolErrorRecovered,
			SessionKey:     current.SessionKey,
			AgentID:        current.AgentID,
			TraceID:        current.ID,
			Tags: []string{
				"tool:" + failedCall.Name,
				"recovered",
			},
		}
	}

	return &LessonRecord{
		Situation:    strings.TrimSpace(current.UserMessage),
		Approach:     summarizeToolCall(*failedCall),
		Outcome:      lessonOutcomeFailure,
		ErrorMessage: strings.TrimSpace(failedCall.Result),
		Confidence:   0.6,
		Source:       lessonSourceToolError,
		SessionKey:   current.SessionKey,
		AgentID:      current.AgentID,
		TraceID:      current.ID,
		Tags: []string{
			"tool:" + failedCall.Name,
			"failed",
		},
	}
}

func matchCorrection(message string) (bool, string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return false, ""
	}
	for _, pattern := range correctionPatterns {
		matches := pattern.re.FindStringSubmatch(message)
		if len(matches) == 0 {
			continue
		}
		betterApproach := ""
		for i := len(matches) - 1; i >= 1; i-- {
			candidate := strings.TrimSpace(matches[i])
			if candidate != "" {
				betterApproach = candidate
				break
			}
		}
		if pattern.minLen > 0 && len([]rune(betterApproach)) < pattern.minLen {
			continue
		}
		return true, betterApproach
	}
	return false, ""
}

func firstMeaningfulFailure(toolCalls []trace.ToolCallTrace) (int, *trace.ToolCallTrace) {
	limit := len(toolCalls)
	if limit > 3 {
		limit = 3
	}
	for i := 0; i < limit; i++ {
		if !toolCalls[i].IsError || !isMeaningfulFailedToolCall(toolCalls[i]) {
			continue
		}
		call := toolCalls[i]
		return i, &call
	}
	return -1, nil
}

func findRecoveryCall(toolCalls []trace.ToolCallTrace, failedIdx int, toolName string) *trace.ToolCallTrace {
	if failedIdx < 0 || failedIdx >= len(toolCalls) {
		return nil
	}
	for i := failedIdx + 1; i < len(toolCalls); i++ {
		if toolCalls[i].IsError || !isMeaningfulToolCall(toolCalls[i]) || toolCalls[i].Name != toolName {
			continue
		}
		call := toolCalls[i]
		return &call
	}
	for i := failedIdx + 1; i < len(toolCalls); i++ {
		if toolCalls[i].IsError || !isMeaningfulToolCall(toolCalls[i]) {
			continue
		}
		call := toolCalls[i]
		return &call
	}
	return nil
}

func isMeaningfulToolCall(call trace.ToolCallTrace) bool {
	if strings.TrimSpace(call.Name) == "" {
		return false
	}
	if strings.TrimSpace(call.Result) != "" {
		return true
	}
	return len(call.Args) > 0
}

func isMeaningfulFailedToolCall(call trace.ToolCallTrace) bool {
	if strings.TrimSpace(call.Name) == "" {
		return false
	}
	return strings.TrimSpace(call.Result) != ""
}

func ignoreTraceExitReason(exitReason string) bool {
	switch exitReason {
	case trace.ExitReasonContextCancelled, trace.ExitReasonContextDeadline:
		return true
	default:
		return false
	}
}

func isInfraFailure(exitReason, message string) bool {
	if ignoreTraceExitReason(exitReason) {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	for _, token := range infraErrorSubstrings {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func summarizeToolCall(call trace.ToolCallTrace) string {
	parts := []string{fmt.Sprintf("tool=%s", strings.TrimSpace(call.Name))}
	if len(call.Args) > 0 {
		if raw, err := json.Marshal(call.Args); err == nil {
			parts = append(parts, fmt.Sprintf("args=%s", string(raw)))
		}
	}
	if result := strings.TrimSpace(call.Result); result != "" {
		parts = append(parts, fmt.Sprintf("result=%s", result))
	}
	return strings.Join(parts, " ")
}

func summarizeTraceApproach(t *trace.TurnTrace) string {
	if t == nil {
		return ""
	}
	for _, call := range t.ToolCalls {
		if isMeaningfulToolCall(call) {
			return summarizeToolCall(call)
		}
	}
	return strings.TrimSpace(t.FinalResponse)
}

func firstFailureMessage(t *trace.TurnTrace) string {
	if t == nil {
		return ""
	}
	for _, call := range t.ToolCalls {
		if call.IsError && !isInfraFailure(t.ExitReason, call.Result) {
			return strings.TrimSpace(call.Result)
		}
	}
	return ""
}

func cloneTrace(src *trace.TurnTrace) *trace.TurnTrace {
	if src == nil {
		return nil
	}
	dst := *src
	if len(src.InjectedMemoryIDs) > 0 {
		dst.InjectedMemoryIDs = append([]string(nil), src.InjectedMemoryIDs...)
	}
	if len(src.InjectedLearningIDs) > 0 {
		dst.InjectedLearningIDs = append([]string(nil), src.InjectedLearningIDs...)
	}
	if len(src.ToolCalls) > 0 {
		dst.ToolCalls = make([]trace.ToolCallTrace, len(src.ToolCalls))
		copy(dst.ToolCalls, src.ToolCalls)
		for i := range dst.ToolCalls {
			if len(src.ToolCalls[i].Args) > 0 {
				cloned := make(map[string]any, len(src.ToolCalls[i].Args))
				for k, v := range src.ToolCalls[i].Args {
					cloned[k] = v
				}
				dst.ToolCalls[i].Args = cloned
			}
		}
	}
	if len(src.LLMCalls) > 0 {
		dst.LLMCalls = append([]trace.LLMCallTrace(nil), src.LLMCalls...)
	}
	if len(src.FallbackAttempts) > 0 {
		dst.FallbackAttempts = append([]trace.FallbackAttemptTrace(nil), src.FallbackAttempts...)
	}
	if src.UserNextMessage != nil {
		msg := *src.UserNextMessage
		dst.UserNextMessage = &msg
	}
	return &dst
}
