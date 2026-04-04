package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/memory"
)

const (
	searchMemoryDefaultTopK = 5
	searchMemoryMaxTopK     = 20
	searchMemoryMaxChars    = 1200
)

// SearchMemoryTool performs semantic memory search via a MemoryRetriever.
type SearchMemoryTool struct {
	retriever memory.MemoryRetriever
}

// NewSearchMemoryTool creates a SearchMemoryTool backed by the given retriever.
func NewSearchMemoryTool(retriever memory.MemoryRetriever) *SearchMemoryTool {
	return &SearchMemoryTool{retriever: retriever}
}

func (t *SearchMemoryTool) Name() string { return "search_memory" }

func (t *SearchMemoryTool) Description() string {
	return "Search your memory by meaning. Use when you need specific information not shown in the auto-retrieved context above. Describe what you're looking for in natural language."
}

func (t *SearchMemoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Natural language description of the information you are looking for.",
			},
			"top_k": map[string]any{
				"type":        "integer",
				"description": "Number of results to return (default 5, max 20).",
				"default":     searchMemoryDefaultTopK,
				"maximum":     searchMemoryMaxTopK,
			},
		},
		"required": []string{"query"},
	}
}

// Execute runs the semantic search and formats the results for the LLM.
func (t *SearchMemoryTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	// --- validate query ---
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return ErrorResult("query is required and must be a non-empty string")
	}

	// --- parse top_k (JSON numbers arrive as float64) ---
	topK := searchMemoryDefaultTopK
	if raw, ok := args["top_k"]; ok {
		if v, ok := raw.(float64); ok {
			topK = int(v)
		}
	}
	if topK <= 0 {
		topK = searchMemoryDefaultTopK
	}
	if topK > searchMemoryMaxTopK {
		topK = searchMemoryMaxTopK
	}

	// --- call retriever ---
	chunks, err := t.retriever.Search(ctx, query, topK)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Memory search unavailable: %v. Use recall_memory with a file path instead.", err))
	}

	if len(chunks) == 0 {
		return SilentResult("No relevant memories found for this query.")
	}

	// --- format results ---
	var sb strings.Builder
	fmt.Fprintf(&sb, "Memory search results for %q (%d found):\n\n", query, len(chunks))

	for i, c := range chunks {
		text := c.Text
		if len(text) > searchMemoryMaxChars {
			text = text[:searchMemoryMaxChars] + "…"
		}

		fmt.Fprintf(&sb, "[%d] source: %s\n", i+1, c.Source)
		fmt.Fprintf(&sb, "    date: %s | importance: %d/10 | score: %.3f\n",
			c.CreatedAt.Format("2006-01-02"), c.Importance, c.FinalScore)
		fmt.Fprintf(&sb, "    %s\n\n", strings.ReplaceAll(text, "\n", "\n    "))
	}

	return SilentResult(strings.TrimRight(sb.String(), "\n"))
}
