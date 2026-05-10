package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// TraceWriter writes turn traces to append-only JSONL files.
type TraceWriter struct {
	basePath string
	mu       sync.Mutex
	config   config.LearningConfig
	redactor *Redactor
}

func NewTraceWriter(basePath string) *TraceWriter {
	return NewTraceWriterWithConfig(basePath, nil)
}

func NewTraceWriterWithConfig(basePath string, cfg *config.LearningConfig) *TraceWriter {
	settings := config.LearningConfig{}
	if cfg != nil {
		settings = *cfg
	}
	return &TraceWriter{basePath: basePath, config: settings, redactor: NewRedactor()}
}

func (w *TraceWriter) WriteTrace(ctx context.Context, trace *TurnTrace) error {
	if trace == nil {
		return fmt.Errorf("trace is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	ts := trace.Timestamp.UTC()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	dir := filepath.Join(w.basePath, ts.Format("2006"), ts.Format("2006-01"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create trace dir %s: %w", dir, err)
	}

	filePath := filepath.Join(dir, ts.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open trace file %s: %w", filePath, err)
	}
	defer f.Close()

	sanitized := w.sanitizeTrace(trace)
	enc := json.NewEncoder(f)
	if err := enc.Encode(sanitized); err != nil {
		return fmt.Errorf("write trace record: %w", err)
	}

	return nil
}

func (w *TraceWriter) sanitizeTrace(trace *TurnTrace) *TurnTrace {
	sanitized := cloneTurnTrace(trace)
	if sanitized == nil {
		return nil
	}
	r := defaultRedactor(w.redactor)
	sanitized.UserMessage = r.SanitizeString(sanitized.UserMessage, w.config.GetMaxUserMessageChars())
	sanitized.UserMessageChars = countChars(sanitized.UserMessage)
	sanitized.FinalResponse = r.SanitizeString(sanitized.FinalResponse, w.config.GetMaxFinalResponseChars())
	sanitized.FinalResponseChars = countChars(sanitized.FinalResponse)
	sanitized.UserNextMessage = sanitizeStringPointer(r, sanitized.UserNextMessage, w.config.GetMaxUserMessageChars())
	for i := range sanitized.ToolCalls {
		sanitized.ToolCalls[i].Args = sanitizeArgs(r, sanitized.ToolCalls[i].Args, w.config.GetMaxToolArgsChars())
		sanitized.ToolCalls[i].Result = r.SanitizeString(sanitized.ToolCalls[i].Result, w.config.GetMaxToolResultChars())
	}
	for i := range sanitized.FallbackAttempts {
		sanitized.FallbackAttempts[i].Error = r.SanitizeString(sanitized.FallbackAttempts[i].Error, w.config.GetMaxErrorMessageChars())
	}
	return sanitized
}

func sanitizeArgs(r *Redactor, args map[string]any, maxChars int) map[string]any {
	if len(args) == 0 {
		return nil
	}
	value := r.SanitizeValue(args, maxChars)
	sanitized, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return sanitized
}
