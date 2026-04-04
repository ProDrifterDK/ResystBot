package memory

import (
	"context"
	"errors"
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
		stats.ChunksDecayed = 2
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
	if stats.ChunksDecayed != 2 {
		t.Errorf("expected 2 decayed, got %d", stats.ChunksDecayed)
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
