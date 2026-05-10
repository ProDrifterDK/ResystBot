package trace

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTraceWriterConcurrent(t *testing.T) {
	basePath := t.TempDir()
	writer := NewTraceWriter(basePath)
	timestamp := time.Date(2026, 5, 2, 14, 30, 0, 0, time.UTC)

	const writes = 64
	var wg sync.WaitGroup
	wg.Add(writes)

	errCh := make(chan error, writes)
	for i := range writes {
		go func(i int) {
			defer wg.Done()
			trace := &TurnTrace{
				ID:               fmt.Sprintf("trace_%03d", i),
				SessionKey:       fmt.Sprintf("telegram:%d", i),
				AgentID:          "main",
				Channel:          "telegram",
				ChatID:           fmt.Sprintf("%d", i),
				Timestamp:        timestamp,
				UserMessage:      "hello",
				UserMessageChars: 5,
				ExitReason:       ExitReasonSuccess,
			}
			if err := writer.WriteTrace(context.Background(), trace); err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("WriteTrace() error = %v", err)
		}
	}

	filePath := filepath.Join(basePath, "2026", "2026-05", "2026-05-02.jsonl")
	f, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("open trace file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	seen := make(map[string]bool, writes)
	count := 0
	for scanner.Scan() {
		count++
		var trace TurnTrace
		if err := json.Unmarshal(scanner.Bytes(), &trace); err != nil {
			t.Fatalf("invalid jsonl line %d: %v", count, err)
		}
		seen[trace.ID] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan trace file: %v", err)
	}
	if count != writes {
		t.Fatalf("line count = %d, want %d", count, writes)
	}
	if len(seen) != writes {
		t.Fatalf("unique IDs = %d, want %d", len(seen), writes)
	}
}

func TestTraceWriterHonorsContext(t *testing.T) {
	writer := NewTraceWriter(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := writer.WriteTrace(ctx, &TurnTrace{Timestamp: time.Now().UTC()})
	if err == nil {
		t.Fatalf("WriteTrace() error = nil, want canceled context")
	}
}
