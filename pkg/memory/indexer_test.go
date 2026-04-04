package memory

import (
	"strings"
	"testing"
)

func TestChunkByHeading(t *testing.T) {
	content := `# Title

Some intro text.

## Section One

Content of section one.

## Section Two

Content of section two.
`
	chunks := chunkByHeading(content)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0], "## Section One") {
		t.Errorf("chunk 0 should start with '## Section One', got: %q", chunks[0])
	}
	if !strings.HasPrefix(chunks[1], "## Section Two") {
		t.Errorf("chunk 1 should start with '## Section Two', got: %q", chunks[1])
	}
}

func TestChunkBySeparator(t *testing.T) {
	content := "Entry one\n\n---\n\nEntry two\n\n---\n\nEntry three"
	chunks := chunkBySeparator(content, "---")
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %v", len(chunks), chunks)
	}
	if strings.TrimSpace(chunks[0]) != "Entry one" {
		t.Errorf("chunk 0 = %q, want 'Entry one'", chunks[0])
	}
	if strings.TrimSpace(chunks[1]) != "Entry two" {
		t.Errorf("chunk 1 = %q, want 'Entry two'", chunks[1])
	}
	if strings.TrimSpace(chunks[2]) != "Entry three" {
		t.Errorf("chunk 2 = %q, want 'Entry three'", chunks[2])
	}
}

func TestChunkByParagraphs(t *testing.T) {
	// Build content with multiple short paragraphs and a very low token limit
	// so that we force multiple chunks.
	para := "This is a paragraph with some words in it."
	paragraphs := make([]string, 10)
	for i := range paragraphs {
		paragraphs[i] = para
	}
	content := strings.Join(paragraphs, "\n\n")

	// maxTokens = 20 means ~80 chars per chunk; each paragraph is ~42 chars.
	// So roughly two paragraphs per chunk → expect ~5 chunks.
	chunks := chunkByParagraphs(content, 20)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks with low token limit, got %d", len(chunks))
	}
	for i, c := range chunks {
		if strings.TrimSpace(c) == "" {
			t.Errorf("chunk %d is empty", i)
		}
	}
}

func TestClassifyFile(t *testing.T) {
	tests := []struct {
		path           string
		wantSourceType string
		wantChunkType  string
	}{
		{
			path:           "memory/202501/20250115.md",
			wantSourceType: SourceTypeDailyNote,
			wantChunkType:  ChunkTypeEntry,
		},
		{
			path:           "mind/night_research/2025-01-15.md",
			wantSourceType: SourceTypeMindDoc,
			wantChunkType:  ChunkTypeParagraph,
		},
		{
			path:           "mind/projects/picoclaw.md",
			wantSourceType: SourceTypeMindDoc,
			wantChunkType:  ChunkTypeSection,
		},
		{
			path:           "memory/notes/session.md",
			wantSourceType: SourceTypeMemoryFile,
			wantChunkType:  ChunkTypeSection,
		},
		{
			path:           "memory/202503/20250301.md",
			wantSourceType: SourceTypeDailyNote,
			wantChunkType:  ChunkTypeEntry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			gotSource, gotChunk := classifyFile(tt.path)
			if gotSource != tt.wantSourceType {
				t.Errorf("classifyFile(%q) source_type = %q, want %q", tt.path, gotSource, tt.wantSourceType)
			}
			if gotChunk != tt.wantChunkType {
				t.Errorf("classifyFile(%q) chunk_type = %q, want %q", tt.path, gotChunk, tt.wantChunkType)
			}
		})
	}
}
