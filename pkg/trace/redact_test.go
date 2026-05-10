package trace

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRedactString(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor()
	tests := []struct {
		name      string
		input     string
		forbidden []string
		required  []string
	}{
		{
			name:      "bearer token",
			input:     "Authorization: Bearer sk-test-1234567890abcdefghijk",
			forbidden: []string{"sk-test-1234567890abcdefghijk"},
			required:  []string{RedactedPlaceholder},
		},
		{
			name:      "env api key",
			input:     "OPENAI_API_KEY=sk-live-abcdef1234567890abcdef",
			forbidden: []string{"sk-live-abcdef1234567890abcdef"},
			required:  []string{RedactedPlaceholder},
		},
		{
			name:      "json password",
			input:     `{"password":"super-secret-pass"}`,
			forbidden: []string{"super-secret-pass"},
			required:  []string{`"password":"` + RedactedPlaceholder + `"`},
		},
		{
			name:      "private key block",
			input:     "-----BEGIN PRIVATE KEY-----\nabc123secret\n-----END PRIVATE KEY-----",
			forbidden: []string{"abc123secret", "BEGIN PRIVATE KEY"},
			required:  []string{RedactedPlaceholder},
		},
		{
			name:      "credential paths and urls",
			input:     "use /home/al/.aws/credentials and https://alice:hunter2@example.com",
			forbidden: []string{"/home/al/.aws/credentials", "hunter2"},
			required:  []string{RedactedPlaceholder},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactor.RedactString(tc.input)
			for _, forbidden := range tc.forbidden {
				if strings.Contains(got, forbidden) {
					t.Fatalf("redacted output still contains secret substring %q", forbidden)
				}
			}
			for _, required := range tc.required {
				if !strings.Contains(got, required) {
					t.Fatalf("redacted output %q missing %q", got, required)
				}
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	t.Parallel()

	s := strings.Repeat("a", 128)
	got := TruncateString(s, 32)
	if countChars(got) != 32 {
		t.Fatalf("truncated length = %d, want 32", countChars(got))
	}
	if !strings.Contains(got, TruncationMarker) {
		t.Fatalf("truncated output missing marker: %q", got)
	}
}

func TestTraceWriterRedactsAndTruncates(t *testing.T) {
	t.Parallel()

	basePath := t.TempDir()
	writer := NewTraceWriterWithConfig(basePath, &config.LearningConfig{
		MaxUserMessageChars:   48,
		MaxFinalResponseChars: 48,
		MaxToolArgsChars:      40,
		MaxToolResultChars:    64,
		MaxErrorMessageChars:  44,
	})
	secretToolResult := strings.Repeat("tool-output-", 120) + " OPENAI_API_KEY=sk-super-secret-123456789"
	traceRecord := &TurnTrace{
		ID:               "trace_redaction",
		SessionKey:       "telegram:1",
		AgentID:          "main",
		Channel:          "telegram",
		ChatID:           "1",
		Timestamp:        time.Date(2026, 5, 3, 15, 0, 0, 0, time.UTC),
		UserMessage:      "Bearer sk-user-secret-1234567890abcdefghijklmnopqrstuvwxyz",
		UserMessageChars: 0,
		FinalResponse:    `saved password="dont-log-me" then ` + strings.Repeat("x", 90),
		ToolCalls: []ToolCallTrace{{
			Name: "exec",
			Args: map[string]any{
				"command": []any{
					"curl",
					"https://alice:hunter2@example.com",
					"OPENAI_API_KEY=sk-inline-secret-0987654321",
				},
			},
			Result: secretToolResult,
		}},
		FallbackAttempts: []FallbackAttemptTrace{{
			Provider: "openrouter",
			Model:    "openrouter/auto",
			Error:    "failed to open /home/al/.aws/credentials with token sk-error-secret-987654321",
		}},
		ExitReason: ExitReasonSuccess,
	}

	if err := writer.WriteTrace(context.Background(), traceRecord); err != nil {
		t.Fatalf("WriteTrace() error = %v", err)
	}

	filePath := filepath.Join(basePath, "2026", "2026-05", "2026-05-03.jsonl")
	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("open trace file: %v", err)
	}
	defer f.Close()

	var stored TurnTrace
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("expected one trace line")
	}
	if err := json.Unmarshal(scanner.Bytes(), &stored); err != nil {
		t.Fatalf("unmarshal stored trace: %v", err)
	}

	assertNoSecretSubstrings(t, marshalJSON(t, stored), []string{
		"sk-user-secret-1234567890abcdefghijklmnopqrstuvwxyz",
		"dont-log-me",
		"hunter2",
		"sk-inline-secret-0987654321",
		"sk-super-secret-123456789",
		"/home/al/.aws/credentials",
		"sk-error-secret-987654321",
	})

	if countChars(stored.UserMessage) > 48 {
		t.Fatalf("user message chars = %d, want <= 48", countChars(stored.UserMessage))
	}
	if countChars(stored.FinalResponse) > 48 {
		t.Fatalf("final response chars = %d, want <= 48", countChars(stored.FinalResponse))
	}
	if countChars(stored.ToolCalls[0].Result) > 64 {
		t.Fatalf("tool result chars = %d, want <= 64", countChars(stored.ToolCalls[0].Result))
	}
	if !strings.Contains(stored.ToolCalls[0].Result, TruncationMarker) {
		t.Fatalf("tool result missing truncation marker: %q", stored.ToolCalls[0].Result)
	}
	if !strings.Contains(stored.UserMessage, RedactedPlaceholder) {
		t.Fatalf("user message missing redaction placeholder: %q", stored.UserMessage)
	}
}

func marshalJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	return string(raw)
}

func assertNoSecretSubstrings(t *testing.T, got string, forbidden []string) {
	t.Helper()
	for _, secret := range forbidden {
		if strings.Contains(got, secret) {
			t.Fatalf("found forbidden secret substring %q", secret)
		}
	}
}
