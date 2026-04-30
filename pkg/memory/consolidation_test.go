package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunConsolidation_AllPhases(t *testing.T) {
	callOrder := []string{}

	phase1 := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "phase1")
		stats.ChunksStrengthened = 3
		return nil
	}
	phase2 := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "phase2")
		stats.ChunksScored = 2
		return nil
	}

	deps := &ConsolidationDeps{DryRun: false}
	stats, err := RunConsolidation(context.Background(), deps,
		NamedPhase{Name: "phase1", Fn: phase1},
		NamedPhase{Name: "phase2", Fn: phase2},
	)
	if err != nil {
		t.Fatalf("RunConsolidation failed: %v", err)
	}
	if len(callOrder) != 2 || callOrder[0] != "phase1" || callOrder[1] != "phase2" {
		t.Errorf("unexpected call order: %v", callOrder)
	}
	if stats.ChunksStrengthened != 3 {
		t.Errorf("expected 3 strengthened, got %d", stats.ChunksStrengthened)
	}
	if stats.ChunksScored != 2 {
		t.Errorf("expected 2 scored, got %d", stats.ChunksScored)
	}
}

func TestRunConsolidation_PhaseErrorContinues(t *testing.T) {
	callOrder := []string{}

	failing := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "failing")
		return errors.New("phase failed")
	}
	succeeding := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "succeeding")
		return nil
	}

	deps := &ConsolidationDeps{DryRun: false}
	stats, err := RunConsolidation(context.Background(), deps,
		NamedPhase{Name: "failing", Fn: failing},
		NamedPhase{Name: "succeeding", Fn: succeeding},
	)
	if err != nil {
		t.Fatalf("RunConsolidation should not return error: %v", err)
	}
	if len(callOrder) != 2 {
		t.Fatalf("expected 2 phases called, got %d", len(callOrder))
	}
	if len(stats.Errors) != 1 {
		t.Errorf("expected 1 error in stats, got %d", len(stats.Errors))
	}
}

func TestRunConsolidation_SinglePhaseFilter(t *testing.T) {
	callOrder := []string{}

	p1 := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "abstract")
		return nil
	}
	p2 := func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
		callOrder = append(callOrder, "strengthen")
		return nil
	}

	deps := &ConsolidationDeps{DryRun: false}
	phases := []NamedPhase{
		{Name: "abstract", Fn: p1},
		{Name: "strengthen", Fn: p2},
	}
	filtered := FilterPhases(phases, "strengthen")
	_, err := RunConsolidation(context.Background(), deps, filtered...)
	if err != nil {
		t.Fatalf("RunConsolidation failed: %v", err)
	}
	if len(callOrder) != 1 || callOrder[0] != "strengthen" {
		t.Errorf("expected only strengthen, got %v", callOrder)
	}
}

func TestRunConsolidation_SkipsUnavailableDeps(t *testing.T) {
	var ran []string

	noopPhase := func(name string) Phase {
		return func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
			ran = append(ran, name)
			return nil
		}
	}

	deps := &ConsolidationDeps{
		DryRun:             false,
		StoreAvailable:     true,
		EmbedderAvailable:  false,
		LLMAvailable:       false,
		ArchiverAvailable:  true,
	}

	phases := []NamedPhase{
		{Name: "abstract", Fn: noopPhase("abstract"), Deps: PhaseDeps{Store: true, Embedder: true, LLM: true, Archiver: true}},
		{Name: "strengthen", Fn: noopPhase("strengthen"), Deps: PhaseDeps{Store: true}},
		{Name: "score", Fn: noopPhase("score"), Deps: PhaseDeps{Store: true}},
		{Name: "prune", Fn: noopPhase("prune"), Deps: PhaseDeps{Store: true, Archiver: true}},
		{Name: "reflect", Fn: noopPhase("reflect"), Deps: PhaseDeps{Store: true, Embedder: true, LLM: true}},
	}

	stats, err := RunConsolidation(context.Background(), deps, phases...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"strengthen", "score", "prune"}
	if len(ran) != len(expected) {
		t.Fatalf("expected %d phases to run, got %d: %v", len(expected), len(ran), ran)
	}
	for i, name := range expected {
		if ran[i] != name {
			t.Errorf("phase %d: expected %s, got %s", i, name, ran[i])
		}
	}

	skippedCount := 0
	for _, e := range stats.Errors {
		if strings.Contains(e, "skipped") {
			skippedCount++
		}
	}
	if skippedCount != 2 {
		t.Errorf("expected 2 skipped phases in errors, got %d: %v", skippedCount, stats.Errors)
	}
}

func TestRunConsolidation_NoRunnablePhases(t *testing.T) {
	noopPhase := func(name string) Phase {
		return func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
			t.Errorf("phase %s should not have run", name)
			return nil
		}
	}

	deps := &ConsolidationDeps{
		DryRun:        false,
		StoreAvailable: false,
	}

	phases := []NamedPhase{
		{Name: "abstract", Fn: noopPhase("abstract"), Deps: PhaseDeps{Store: true, Embedder: true, LLM: true, Archiver: true}},
		{Name: "strengthen", Fn: noopPhase("strengthen"), Deps: PhaseDeps{Store: true}},
		{Name: "score", Fn: noopPhase("score"), Deps: PhaseDeps{Store: true}},
		{Name: "prune", Fn: noopPhase("prune"), Deps: PhaseDeps{Store: true, Archiver: true}},
		{Name: "reflect", Fn: noopPhase("reflect"), Deps: PhaseDeps{Store: true, Embedder: true, LLM: true}},
	}

	_, err := RunConsolidation(context.Background(), deps, phases...)
	if err == nil {
		t.Fatal("expected error when no phases are runnable")
	}
	if !strings.Contains(err.Error(), "no runnable phases") {
		t.Errorf("expected 'no runnable phases' error, got: %v", err)
	}
}

func TestRunConsolidation_SinglePhaseBlocked(t *testing.T) {
	noopPhase := func(name string) Phase {
		return func(ctx context.Context, deps *ConsolidationDeps, stats *ConsolidationStats) error {
			t.Errorf("phase %s should not have run", name)
			return nil
		}
	}

	deps := &ConsolidationDeps{
		DryRun:            false,
		StoreAvailable:    true,
		EmbedderAvailable: false,
	}

	phases := []NamedPhase{
		{Name: "abstract", Fn: noopPhase("abstract"), Deps: PhaseDeps{Store: true, Embedder: true, LLM: true, Archiver: true}},
	}

	_, err := RunConsolidation(context.Background(), deps, phases...)
	if err == nil {
		t.Fatal("expected error when single phase is blocked")
	}
	if !strings.Contains(err.Error(), "no runnable phases") {
		t.Errorf("expected 'no runnable phases' error, got: %v", err)
	}
}
