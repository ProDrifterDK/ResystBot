package learning

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/trace"
)

type fakeLessonStore struct {
	mu      sync.Mutex
	records []LessonRecord
	err     error
}

func (f *fakeLessonStore) Store(ctx context.Context, record *LessonRecord) error {
	if f.err != nil {
		return f.err
	}
	if record == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, *record)
	return nil
}

func TestOutcomeRecoveredToolErrorWinsOverPlainFailure(t *testing.T) {
	t.Parallel()

	store := &fakeLessonStore{}
	extractor := NewOutcomeExtractor(store, &config.LearningConfig{MinUserMessageChars: 10})

	lesson, err := extractor.ProcessTrace(context.Background(), &trace.TurnTrace{
		ID:          "trace-recovered",
		SessionKey:  "telegram:1",
		AgentID:     "main",
		Timestamp:   time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		UserMessage: "install numpy on Pop!_OS with pip in this environment",
		ToolCalls: []trace.ToolCallTrace{
			{Name: "exec", Args: map[string]any{"command": "pip install numpy"}, Result: "error: externally-managed-environment", IsError: true},
			{Name: "exec", Args: map[string]any{"command": "pip install --break-system-packages numpy"}, Result: "Successfully installed numpy", IsError: false},
			{Name: "read_file", Args: map[string]any{"path": "notes.txt"}, Result: "ok", IsError: false},
		},
	})
	if err != nil {
		t.Fatalf("ProcessTrace() error = %v", err)
	}
	if lesson == nil {
		t.Fatal("expected recovered tool lesson")
	}
	if lesson.Source != lessonSourceToolErrorRecovered {
		t.Fatalf("lesson source = %q", lesson.Source)
	}
	if lesson.Outcome != lessonOutcomeSuccess {
		t.Fatalf("lesson outcome = %q", lesson.Outcome)
	}
	if !strings.Contains(lesson.BetterApproach, "--break-system-packages") {
		t.Fatalf("better approach = %q", lesson.BetterApproach)
	}
	if len(store.records) != 1 {
		t.Fatalf("stored lessons = %d, want 1", len(store.records))
	}
}

func TestOutcomeUsesFirstMeaningfulFailureWithinFirstThreeCalls(t *testing.T) {
	t.Parallel()

	store := &fakeLessonStore{}
	extractor := NewOutcomeExtractor(store, &config.LearningConfig{MinUserMessageChars: 10})

	lesson, err := extractor.ProcessTrace(context.Background(), &trace.TurnTrace{
		ID:          "trace-failure",
		SessionKey:  "telegram:2",
		AgentID:     "main",
		Timestamp:   time.Date(2026, 5, 3, 11, 0, 0, 0, time.UTC),
		UserMessage: "please install a package and explain what broke when the first try fails",
		ToolCalls: []trace.ToolCallTrace{
			{Name: "exec", Args: map[string]any{"command": "pip install foo"}, Result: "", IsError: true},
			{Name: "exec", Args: map[string]any{"command": "pip install foo"}, Result: "permission denied", IsError: true},
			{Name: "exec", Args: map[string]any{"command": "pip install foo --user"}, Result: "error: externally-managed-environment", IsError: true},
		},
	})
	if err != nil {
		t.Fatalf("ProcessTrace() error = %v", err)
	}
	if lesson == nil {
		t.Fatal("expected lesson")
	}
	if lesson.Source != lessonSourceToolError {
		t.Fatalf("lesson source = %q", lesson.Source)
	}
	if !strings.Contains(lesson.ErrorMessage, "permission denied") {
		t.Fatalf("error message = %q", lesson.ErrorMessage)
	}
	if strings.Contains(lesson.BetterApproach, "--break-system-packages") {
		t.Fatalf("unexpected recovery from fourth tool call: %q", lesson.BetterApproach)
	}
}

func TestOutcomeExtractorConcurrentSessionUpdates(t *testing.T) {
	store := &fakeLessonStore{}
	extractor := NewOutcomeExtractor(store, &config.LearningConfig{MinUserMessageChars: 10})

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = extractor.ProcessTrace(context.Background(), &trace.TurnTrace{
				ID:          fmt.Sprintf("trace-concurrent-%d", i),
				SessionKey:  fmt.Sprintf("session-%d", i%8),
				AgentID:     "main",
				Timestamp:   time.Date(2026, 5, 3, 12, i, 0, 0, time.UTC),
				UserMessage: "install python package with enough detail to be learnable",
				ToolCalls: []trace.ToolCallTrace{{
					Name:    "exec",
					Args:    map[string]any{"command": "pip install foo"},
					Result:  "error: externally-managed-environment",
					IsError: true,
				}},
			})
		}(i)
	}
	wg.Wait()
	extractor.lastTraceMu.Lock()
	defer extractor.lastTraceMu.Unlock()
	if len(extractor.lastTraceBySession) == 0 {
		t.Fatal("expected session traces to be retained")
	}
}

func TestCorrectionCreatesLinkedAppendOnlyLesson(t *testing.T) {
	t.Parallel()

	store := &fakeLessonStore{}
	extractor := NewOutcomeExtractor(store, &config.LearningConfig{})
	baseTime := time.Date(2026, 5, 3, 13, 0, 0, 0, time.UTC)
	extractor.now = func() time.Time { return baseTime }

	_, err := extractor.ProcessTrace(context.Background(), &trace.TurnTrace{
		ID:            "trace-original",
		SessionKey:    "telegram:3",
		AgentID:       "main",
		Timestamp:     baseTime,
		UserMessage:   "install numpy on Pop!_OS and keep the system python intact please",
		FinalResponse: "I ran pip install numpy and it failed.",
		ToolCalls: []trace.ToolCallTrace{{
			Name:    "exec",
			Args:    map[string]any{"command": "pip install numpy"},
			Result:  "error: externally-managed-environment",
			IsError: true,
		}},
	})
	if err != nil {
		t.Fatalf("ProcessTrace(first) error = %v", err)
	}

	lesson, err := extractor.ProcessTrace(context.Background(), &trace.TurnTrace{
		ID:            "trace-correction",
		SessionKey:    "telegram:3",
		AgentID:       "main",
		Timestamp:     baseTime.Add(2 * time.Minute),
		UserMessage:   "actually, use pip install --break-system-packages numpy instead on Pop!_OS",
		FinalResponse: "Thanks",
	})
	if err != nil {
		t.Fatalf("ProcessTrace(correction) error = %v", err)
	}
	if lesson == nil {
		t.Fatal("expected correction lesson")
	}
	if lesson.Source != lessonSourceUserCorrection {
		t.Fatalf("lesson source = %q", lesson.Source)
	}
	if lesson.TraceID != "trace-original" {
		t.Fatalf("lesson trace_id = %q", lesson.TraceID)
	}
	if !strings.Contains(lesson.BetterApproach, "--break-system-packages") {
		t.Fatalf("better approach = %q", lesson.BetterApproach)
	}
	if len(store.records) != 2 {
		t.Fatalf("stored lessons = %d, want 2 append-only records", len(store.records))
	}
}

func TestCorrectionAfterTTLDoesNotLink(t *testing.T) {
	t.Parallel()

	store := &fakeLessonStore{}
	extractor := NewOutcomeExtractor(store, &config.LearningConfig{CorrectionSessionTTL: 1})
	baseTime := time.Date(2026, 5, 3, 14, 0, 0, 0, time.UTC)
	extractor.now = func() time.Time { return baseTime }
	extractor.SetLastTrace("telegram:4", &trace.TurnTrace{
		ID:          "trace-expired",
		SessionKey:  "telegram:4",
		AgentID:     "main",
		Timestamp:   baseTime.Add(-2 * time.Minute),
		UserMessage: "old failure",
	})

	lesson, err := extractor.ProcessTrace(context.Background(), &trace.TurnTrace{
		ID:          "trace-after-ttl",
		SessionKey:  "telegram:4",
		AgentID:     "main",
		Timestamp:   baseTime,
		UserMessage: "actually, use the package manager instead",
	})
	if err != nil {
		t.Fatalf("ProcessTrace() error = %v", err)
	}
	if lesson != nil {
		t.Fatalf("expected no linked lesson after TTL, got %+v", lesson)
	}
	if len(store.records) != 0 {
		t.Fatalf("stored lessons = %d, want 0", len(store.records))
	}
}

func TestInfraFailuresAreIgnored(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		exitReason string
		result     string
	}{
		{name: "rate_limit", result: "rate limit exceeded from provider"},
		{name: "auth", result: "authentication failed: invalid api key"},
		{name: "network", result: "connection refused while calling upstream"},
		{name: "qdrant", result: "qdrant unavailable: connection refused"},
		{name: "sandbox", result: "Command blocked by safety guard (path outside working dir)"},
		{name: "daemon_shutdown", result: "daemon shutting down: context canceled"},
		{name: "context_cancelled", exitReason: trace.ExitReasonContextCancelled, result: "context canceled"},
		{name: "context_deadline", exitReason: trace.ExitReasonContextDeadline, result: "context deadline exceeded"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeLessonStore{}
			extractor := NewOutcomeExtractor(store, &config.LearningConfig{MinUserMessageChars: 10})

			lesson, err := extractor.ProcessTrace(context.Background(), &trace.TurnTrace{
				ID:          "trace-" + tc.name,
				SessionKey:  "telegram:5",
				AgentID:     "main",
				Timestamp:   time.Date(2026, 5, 3, 15, 0, 0, 0, time.UTC),
				UserMessage: "analyze the failure with enough detail for learning to consider it",
				ExitReason:  tc.exitReason,
				ToolCalls: []trace.ToolCallTrace{{
					Name:    "exec",
					Args:    map[string]any{"command": "do something"},
					Result:  tc.result,
					IsError: true,
				}},
			})
			if err != nil {
				t.Fatalf("ProcessTrace() error = %v", err)
			}
			if lesson != nil {
				t.Fatalf("expected infra failure to be ignored, got %+v", lesson)
			}
			if len(store.records) != 0 {
				t.Fatalf("stored lessons = %d, want 0", len(store.records))
			}
		})
	}
}
