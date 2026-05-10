package learning

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestDryRunLessonClustersGroupsRelatedLessons(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	lessons := []LessonRecord{
		{
			ID:             "lesson-1",
			Situation:      "docker compose bridge network cannot resolve api service",
			ErrorMessage:   "docker compose network name resolution failed",
			BetterApproach: "docker compose up with shared bridge network and compose alias",
			Source:         "tool_error_recovered",
			CreatedAt:      base.Add(-48 * time.Hour).Format(time.RFC3339),
			Tags:           []string{"docker", "networking"},
		},
		{
			ID:             "lesson-2",
			Situation:      "docker container on bridge network cannot reach postgres service",
			ErrorMessage:   "bridge network alias missing for postgres",
			BetterApproach: "configure docker bridge network alias for compose service",
			Source:         "tool_error_recovered",
			CreatedAt:      base.Add(-36 * time.Hour).Format(time.RFC3339),
			Tags:           []string{"docker", "networking"},
		},
		{
			ID:             "lesson-3",
			Situation:      "docker bridge network lookup fails between api and worker",
			ErrorMessage:   "compose worker cannot resolve bridge network hostname",
			BetterApproach: "use one docker bridge network and stable compose aliases",
			Source:         "tool_error_recovered",
			CreatedAt:      base.Add(-12 * time.Hour).Format(time.RFC3339),
			Tags:           []string{"docker", "networking"},
		},
		{
			ID:             "lesson-4",
			Situation:      "golang module download failed behind proxy",
			ErrorMessage:   "proxy timeout while downloading go module",
			BetterApproach: "set GOPROXY and retry go mod download",
			Source:         "tool_error_recovered",
			CreatedAt:      base.Add(-18 * time.Hour).Format(time.RFC3339),
			Tags:           []string{"golang", "proxy"},
		},
		{
			ID:             "lesson-5",
			Situation:      "docker networking issue from last month",
			ErrorMessage:   "stale bridge network state",
			BetterApproach: "reset docker network state",
			Source:         "tool_error_recovered",
			CreatedAt:      base.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
			Tags:           []string{"docker", "networking"},
		},
	}

	result := DryRunLessonClusters(&config.LearningConfig{CrossSessionClustering: true}, lessons, ClusterScanOptions{
		Topic:         "docker",
		Source:        "tool_error_recovered",
		Window:        7 * 24 * time.Hour,
		ReferenceTime: base,
	})

	if !result.Enabled {
		t.Fatalf("expected clustering enabled result, got %+v", result)
	}
	if result.DisabledReason != "" {
		t.Fatalf("unexpected disabled reason: %q", result.DisabledReason)
	}
	if result.ScannedLessons != 3 {
		t.Fatalf("scanned lessons = %d, want 3", result.ScannedLessons)
	}
	if result.MatchedLessons != 3 {
		t.Fatalf("matched lessons = %d, want 3", result.MatchedLessons)
	}
	if len(result.Clusters) != 1 {
		t.Fatalf("cluster count = %d, want 1", len(result.Clusters))
	}
	cluster := result.Clusters[0]
	if cluster.Source != "tool_error_recovered" {
		t.Fatalf("cluster source = %q", cluster.Source)
	}
	if cluster.Topic != "docker" {
		t.Fatalf("cluster topic = %q, want docker", cluster.Topic)
	}
	if cluster.LessonCount != 3 {
		t.Fatalf("cluster lesson count = %d, want 3", cluster.LessonCount)
	}
	wantIDs := []string{"lesson-1", "lesson-2", "lesson-3"}
	for i, id := range wantIDs {
		if cluster.LessonIDs[i] != id {
			t.Fatalf("cluster lesson_ids[%d] = %q, want %q", i, cluster.LessonIDs[i], id)
		}
	}
	if len(cluster.SharedTags) == 0 || cluster.SharedTags[0] != "docker" {
		t.Fatalf("shared tags = %v, want docker", cluster.SharedTags)
	}
	if len(cluster.SharedTopicTerms) == 0 {
		t.Fatalf("expected shared topic terms, got none: %+v", cluster)
	}
}

func TestDryRunLessonClustersDisabledByDefault(t *testing.T) {
	t.Parallel()

	lessons := []LessonRecord{{
		ID:        "lesson-1",
		Source:    "tool_error_recovered",
		CreatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Tags:      []string{"docker"},
	}}

	result := DryRunLessonClusters(&config.LearningConfig{}, lessons, ClusterScanOptions{Topic: "docker"})
	if result.Enabled {
		t.Fatalf("expected disabled result, got %+v", result)
	}
	if result.DisabledReason == "" {
		t.Fatal("expected disabled reason")
	}
	if len(result.Clusters) != 0 {
		t.Fatalf("expected no clusters when disabled, got %d", len(result.Clusters))
	}
	if result.ScannedLessons != 0 || result.MatchedLessons != 0 {
		t.Fatalf("disabled result should not scan lessons, got scanned=%d matched=%d", result.ScannedLessons, result.MatchedLessons)
	}

	result = DryRunLessonClusters(nil, lessons, ClusterScanOptions{Topic: "docker"})
	if result.Enabled {
		t.Fatalf("expected nil config result to stay disabled, got %+v", result)
	}
}
