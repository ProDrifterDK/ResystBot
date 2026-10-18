package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

const noContentChangeDiffMessage = "(no content change)"

// DiffResult creates a user-visible tool result containing a unified diff for a
// successful file edit. The diff is included for both the LLM and the user so
// the follow-up assistant response can reason about the exact change set.
//
// Port of upstream 4a81f0e7, adapted to our flat pkg/tools layout.
func DiffResult(path string, before, after []byte) *ToolResult {
	diff, err := buildUnifiedDiff(path, before, after)
	if err != nil {
		return UserResult(fmt.Sprintf("File edited: %s\n[diff unavailable: %v]", path, err))
	}

	content := fmt.Sprintf("File edited: %s\n```diff\n%s\n```", path, diff)
	return UserResult(content)
}

func buildUnifiedDiff(path string, before, after []byte) (string, error) {
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(before)),
		B:        difflib.SplitLines(string(after)),
		FromFile: "a/" + diffDisplayPath(path),
		ToFile:   "b/" + diffDisplayPath(path),
		Context:  3,
	})
	if err != nil {
		return "", err
	}

	diff = strings.TrimRight(diff, "\n")
	if diff == "" {
		return noContentChangeDiffMessage, nil
	}

	return diff, nil
}

func diffDisplayPath(path string) string {
	displayPath := strings.TrimLeft(filepath.ToSlash(path), "/")
	if displayPath == "" {
		return "file"
	}
	return displayPath
}
