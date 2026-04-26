package providers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiter_AllowsUpToRPM(t *testing.T) {
	rl := newRateLimiter(5)
	base := time.Now()
	rl.lastTick = base
	rl.nowFunc = func() time.Time { return base }

	for i := 0; i < 5; i++ {
		if !rl.TryAcquire() {
			t.Fatalf("TryAcquire #%d = false, want true", i+1)
		}
	}
	if rl.TryAcquire() {
		t.Fatal("6th TryAcquire = true, want false")
	}
}

func TestRateLimiter_ContextCancellation(t *testing.T) {
	rl := newRateLimiter(1)
	base := time.Now()
	rl.lastTick = base
	rl.nowFunc = func() time.Time { return base }

	if !rl.TryAcquire() {
		t.Fatal("first TryAcquire = false, want true")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := rl.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait error = %v, want deadline exceeded", err)
	}
}

func TestRateLimiter_TokenRefill(t *testing.T) {
	rl := newRateLimiter(30)
	base := time.Now()
	now := base
	rl.lastTick = base
	rl.nowFunc = func() time.Time { return now }

	for i := 0; i < 30; i++ {
		if !rl.TryAcquire() {
			t.Fatalf("TryAcquire #%d = false, want true", i+1)
		}
	}
	if rl.TryAcquire() {
		t.Fatal("extra TryAcquire before refill = true, want false")
	}

	now = now.Add(2 * time.Second)
	if !rl.TryAcquire() {
		t.Fatal("TryAcquire after 2s refill = false, want true")
	}
}

func TestRateLimiterRegistry_NoLimiter(t *testing.T) {
	r := NewRateLimiterRegistry()
	for i := 0; i < 100; i++ {
		if !r.TryAcquire("missing") {
			t.Fatalf("TryAcquire #%d = false for unregistered key", i+1)
		}
	}
	if err := r.Wait(context.Background(), "missing"); err != nil {
		t.Fatalf("Wait error for unregistered key = %v", err)
	}
}

func TestRateLimiterRegistry_ZeroRPM(t *testing.T) {
	r := NewRateLimiterRegistry()
	r.Register("zero", 0)
	if _, ok := r.limiters["zero"]; ok {
		t.Fatal("zero RPM should not register a limiter")
	}
	for i := 0; i < 10; i++ {
		if !r.TryAcquire("zero") {
			t.Fatalf("TryAcquire #%d = false for zero RPM key", i+1)
		}
	}
}

func TestRateLimiterRegistry_Enforcement(t *testing.T) {
	r := NewRateLimiterRegistry()
	r.Register("key", 3)

	rl := r.limiters["key"]
	base := time.Now()
	rl.lastTick = base
	rl.nowFunc = func() time.Time { return base }

	for i := 0; i < 3; i++ {
		if !r.TryAcquire("key") {
			t.Fatalf("TryAcquire #%d = false, want true", i+1)
		}
	}
	if r.TryAcquire("key") {
		t.Fatal("4th TryAcquire = true, want false")
	}
}

func TestRateLimiterRegistry_RegisterCandidates(t *testing.T) {
	r := NewRateLimiterRegistry()
	candidates := []FallbackCandidate{
		{Provider: "openai", Model: "gpt-4", RPM: 2},
		{Provider: "anthropic", Model: "claude", RPM: 0},
	}

	r.RegisterCandidates(candidates)

	if _, ok := r.limiters[ModelKey("openai", "gpt-4")]; !ok {
		t.Fatal("expected registered limiter for RPM candidate")
	}
	if _, ok := r.limiters[ModelKey("anthropic", "claude")]; ok {
		t.Fatal("unexpected limiter for zero RPM candidate")
	}
}

func TestRateLimiterRegistry_RegisterCandidatesUsesStableIdentity(t *testing.T) {
	r := NewRateLimiterRegistry()
	candidates := []FallbackCandidate{{
		Provider:    "openai",
		Model:       "gpt-4",
		RPM:         2,
		IdentityKey: "primary-openai",
	}}

	r.RegisterCandidates(candidates)

	if _, ok := r.limiters["primary-openai"]; !ok {
		t.Fatal("expected limiter to be registered by stable identity")
	}
	if _, ok := r.limiters[ModelKey("openai", "gpt-4")]; ok {
		t.Fatal("unexpected limiter registered by runtime provider/model key")
	}
}

func TestRateLimiter_Concurrency(t *testing.T) {
	rl := newRateLimiter(20)
	base := time.Now()
	rl.lastTick = base
	rl.nowFunc = func() time.Time { return base }

	var successCount atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if rl.TryAcquire() {
				successCount.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	if got := successCount.Load(); got != 20 {
		t.Fatalf("successful acquisitions = %d, want 20", got)
	}
}
