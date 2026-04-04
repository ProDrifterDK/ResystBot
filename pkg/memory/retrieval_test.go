package memory

import (
	"math"
	"testing"
	"time"
)

func TestRecencyScore(t *testing.T) {
	// decayRate = 0.0001 per hour gives a reasonable decay curve.
	// 1h:  exp(-0.0001 * 1)    ≈ 0.9999  → within 0.02 of 0.999
	// 7d:  exp(-0.0001 * 168)  ≈ 0.9834  → within 0.02 of 0.846  (use larger rate)
	// We test with decayRate = 0.001, which gives:
	//   1h  → exp(-0.001*1)     ≈ 0.999
	//   7d  → exp(-0.001*168)   ≈ 0.846
	//   30d → exp(-0.001*720)   ≈ 0.487
	//   90d → exp(-0.001*2160)  ≈ 0.115
	const decayRate = 0.001
	const tol = 0.02

	tests := []struct {
		name    string
		hoursAgo float64
		want    float64
	}{
		{"1h", 1, 0.999},
		{"7d", 24 * 7, 0.846},
		{"30d", 24 * 30, 0.487},
		{"90d", 24 * 90, 0.115},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			createdAt := time.Now().Add(-time.Duration(tc.hoursAgo * float64(time.Hour)))
			got := recencyScore(createdAt, decayRate)
			if math.Abs(got-tc.want) > tol {
				t.Errorf("recencyScore(-%v h, %v) = %.4f, want %.4f ± %.4f",
					tc.hoursAgo, decayRate, got, tc.want, tol)
			}
		})
	}
}

func TestNormalizeScores(t *testing.T) {
	scores := []float64{1.0, 2.0, 3.0, 4.0, 5.0}
	got := normalizeMinMax(scores)

	if got[0] != 0.0 {
		t.Errorf("min value should map to 0.0, got %v", got[0])
	}
	if got[len(got)-1] != 1.0 {
		t.Errorf("max value should map to 1.0, got %v", got[len(got)-1])
	}

	// Values should be monotonically increasing
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Errorf("normalized scores not monotonically increasing at index %d: %v <= %v", i, got[i], got[i-1])
		}
	}
}

func TestNormalizeScores_AllSame(t *testing.T) {
	scores := []float64{0.7, 0.7, 0.7}
	got := normalizeMinMax(scores)

	for i, v := range got {
		if v != 1.0 {
			t.Errorf("all-equal input: index %d = %v, want 1.0", i, v)
		}
	}
}

func TestCombinedScore(t *testing.T) {
	// relevance=0.9, importance=0.6, recency=0.976 → 0.9*0.6*0.976 ≈ 0.527
	relevance := 0.9
	importance := 0.6
	recency := 0.976
	want := 0.527
	tol := 0.01

	got := relevance * importance * recency
	if math.Abs(got-want) > tol {
		t.Errorf("combined score = %.4f, want %.4f ± %.4f", got, want, tol)
	}
}
