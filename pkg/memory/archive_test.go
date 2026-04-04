package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveWriter_WriteRecords(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewArchiveWriter(tmpDir)

	records := []ArchiveRecord{
		{
			ID:           "point-1",
			Text:         "test memory",
			Source:       "test.md",
			SourceType:   "memory_file",
			Importance:   5,
			AccessCount:  0,
			CreatedAt:    "2026-01-01T00:00:00Z",
			LastAccessed: "2026-01-01T00:00:00Z",
			Tags:         []string{"topic:memory"},
			Vector:       []float64{0.1, 0.2, 0.3},
			Reason:       "pruned",
			MergedInto:   nil,
		},
	}

	err := writer.WriteRecords("2026-04-05", "pruned", records)
	if err != nil {
		t.Fatalf("WriteRecords failed: %v", err)
	}

	filePath := filepath.Join(tmpDir, "2026-04-05", "pruned.jsonl")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read archive file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var record ArchiveRecord
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if record.ID != "point-1" {
		t.Errorf("expected point-1, got %s", record.ID)
	}
	if record.Reason != "pruned" {
		t.Errorf("expected pruned reason, got %s", record.Reason)
	}
	if len(record.Vector) != 3 {
		t.Errorf("expected 3-element vector, got %d", len(record.Vector))
	}
}

func TestArchiveWriter_AppendToExisting(t *testing.T) {
	tmpDir := t.TempDir()
	writer := NewArchiveWriter(tmpDir)

	record1 := []ArchiveRecord{{ID: "p1", Text: "first", Reason: "pruned"}}
	record2 := []ArchiveRecord{{ID: "p2", Text: "second", Reason: "pruned"}}

	writer.WriteRecords("2026-04-05", "pruned", record1)
	writer.WriteRecords("2026-04-05", "pruned", record2)

	data, _ := os.ReadFile(filepath.Join(tmpDir, "2026-04-05", "pruned.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines after append, got %d", len(lines))
	}
}
