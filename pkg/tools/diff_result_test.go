package tools

import (
	"strings"
	"testing"
)

func TestDiffResult_ShowsUnifiedDiff(t *testing.T) {
	res := DiffResult("pkg/foo/bar.go", []byte("line one\nline two\n"), []byte("line one\nline TWO\n"))

	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.ForLLM)
	}
	if res.Silent {
		t.Fatal("diff result must be user-visible (Silent=false)")
	}
	if !strings.Contains(res.ForUser, "```diff") {
		t.Fatalf("expected a fenced diff block, got: %s", res.ForUser)
	}
	if !strings.Contains(res.ForUser, "-line two") || !strings.Contains(res.ForUser, "+line TWO") {
		t.Fatalf("expected removed/added lines in diff, got: %s", res.ForUser)
	}
	if !strings.Contains(res.ForUser, "a/pkg/foo/bar.go") || !strings.Contains(res.ForUser, "b/pkg/foo/bar.go") {
		t.Fatalf("expected a/ b/ headers, got: %s", res.ForUser)
	}
}

func TestDiffResult_NoChange(t *testing.T) {
	same := []byte("unchanged\n")
	res := DiffResult("x.txt", same, same)

	if !strings.Contains(res.ForUser, noContentChangeDiffMessage) {
		t.Fatalf("expected no-change marker, got: %s", res.ForUser)
	}
}
