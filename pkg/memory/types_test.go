package memory

import (
	"testing"
)

func TestGeneratePointID_Deterministic(t *testing.T) {
	id1 := GeneratePointID("source1", "some content")
	id2 := GeneratePointID("source1", "some content")
	if id1 != id2 {
		t.Errorf("expected deterministic ID, got %q and %q", id1, id2)
	}
	if len(id1) != 32 {
		t.Errorf("expected 32-char hex string, got %d chars: %q", len(id1), id1)
	}
}

func TestGeneratePointID_DifferentContent(t *testing.T) {
	id1 := GeneratePointID("source1", "content A")
	id2 := GeneratePointID("source1", "content B")
	if id1 == id2 {
		t.Errorf("expected different IDs for different content, both got %q", id1)
	}
}

func TestGeneratePointID_DifferentSource(t *testing.T) {
	id1 := GeneratePointID("source1", "same content")
	id2 := GeneratePointID("source2", "same content")
	if id1 == id2 {
		t.Errorf("expected different IDs for different sources, both got %q", id1)
	}
}

func TestScoreImportance_BaseScore(t *testing.T) {
	// casual conversation: 3 base - 1 conversation = 2
	score := ScoreImportance("How are you doing today?", SourceTypeConversation)
	if score != 2 {
		t.Errorf("expected score 2 for casual conversation, got %d", score)
	}
}

func TestScoreImportance_Decision(t *testing.T) {
	// "We decided..." in memory_file: 3 base + 3 decision = 6
	score := ScoreImportance("We decided to refactor the auth system", SourceTypeMemoryFile)
	if score != 6 {
		t.Errorf("expected score 6 for decision in memory_file, got %d", score)
	}
}

func TestScoreImportance_MultipleSignals(t *testing.T) {
	// "decided to fix bug in production": decision+3, error+2, critical+2 = 3+3+2+2 = 10
	score := ScoreImportance("decided to fix bug in production", SourceTypeMemoryFile)
	if score != 10 {
		t.Errorf("expected score 10 for multiple signals, got %d", score)
	}
}

func TestScoreImportance_MaxCap(t *testing.T) {
	// All signals: decided + TODO + bug + deploy = 3+3+2+2+2 = 12, capped at 10
	score := ScoreImportance("decided TODO bug deploy", SourceTypeMemoryFile)
	if score != 10 {
		t.Errorf("expected score capped at 10, got %d", score)
	}
}

func TestScoreImportance_MindDoc(t *testing.T) {
	// regular mind doc: 3 base + 1 mind_doc = 4
	score := ScoreImportance("Some notes about the architecture", SourceTypeMindDoc)
	if score != 4 {
		t.Errorf("expected score 4 for mind_doc, got %d", score)
	}
}
