package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ArchiveRecord is a chunk preserved in cold storage with its vector.
type ArchiveRecord struct {
	ID           string    `json:"id"`
	Text         string    `json:"text"`
	Source       string    `json:"source"`
	SourceType   string    `json:"source_type"`
	Importance   int       `json:"importance"`
	AccessCount  int       `json:"access_count"`
	CreatedAt    string    `json:"created_at"`
	LastAccessed string    `json:"last_accessed"`
	Tags         []string  `json:"tags"`
	Vector       []float64 `json:"vector"`
	ArchivedAt   string    `json:"archived_at"`
	Reason       string    `json:"reason"`
	MergedInto   *string   `json:"merged_into"`
}

// ArchiveWriter writes chunk records to JSONL files in cold storage.
type ArchiveWriter struct {
	basePath string
}

// NewArchiveWriter creates an archive writer rooted at basePath.
func NewArchiveWriter(basePath string) *ArchiveWriter {
	return &ArchiveWriter{basePath: basePath}
}

// WriteRecords appends records to basePath/date/reason.jsonl.
func (a *ArchiveWriter) WriteRecords(date string, reason string, records []ArchiveRecord) error {
	dir := filepath.Join(a.basePath, date)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create archive dir %s: %w", dir, err)
	}

	filePath := filepath.Join(dir, reason+".jsonl")
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open archive file %s: %w", filePath, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, record := range records {
		if err := enc.Encode(record); err != nil {
			return fmt.Errorf("write archive record: %w", err)
		}
	}

	return nil
}
