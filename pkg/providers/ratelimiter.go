package providers

import (
	"context"
	"sync"
	"time"
)

// RateLimiter implements a token-bucket rate limiter for a single key.
type RateLimiter struct {
	mu       sync.Mutex
	rpm      int
	tokens   float64
	maxBurst float64
	lastTick time.Time
	nowFunc  func() time.Time
}

func (rl *RateLimiter) refillLocked(now time.Time) {
	elapsed := now.Sub(rl.lastTick).Seconds()
	rl.lastTick = now
	refill := elapsed * float64(rl.rpm) / 60.0
	rl.tokens = min(rl.maxBurst, rl.tokens+refill)
}

func newRateLimiter(rpm int) *RateLimiter {
	return &RateLimiter{
		rpm:      rpm,
		tokens:   float64(rpm),
		maxBurst: float64(rpm),
		lastTick: time.Now(),
		nowFunc:  time.Now,
	}
}

func (rl *RateLimiter) Wait(ctx context.Context) error {
	for {
		rl.mu.Lock()
		now := rl.nowFunc()
		rl.refillLocked(now)
		if rl.tokens >= 1.0 {
			rl.tokens--
			rl.mu.Unlock()
			return nil
		}
		deficit := 1.0 - rl.tokens
		waitSec := deficit / (float64(rl.rpm) / 60.0)
		rl.mu.Unlock()
		timer := time.NewTimer(time.Duration(waitSec * float64(time.Second)))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (rl *RateLimiter) TryAcquire() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refillLocked(rl.nowFunc())
	if rl.tokens < 1.0 {
		return false
	}
	rl.tokens--
	return true
}

// RateLimiterRegistry holds per-candidate rate limiters.
type RateLimiterRegistry struct {
	mu       sync.RWMutex
	limiters map[string]*RateLimiter
}

func NewRateLimiterRegistry() *RateLimiterRegistry {
	return &RateLimiterRegistry{
		limiters: make(map[string]*RateLimiter),
	}
}

func (r *RateLimiterRegistry) Register(key string, rpm int) {
	if rpm <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limiters[key] = newRateLimiter(rpm)
}

func (r *RateLimiterRegistry) Wait(ctx context.Context, key string) error {
	r.mu.RLock()
	rl := r.limiters[key]
	r.mu.RUnlock()
	if rl == nil {
		return nil
	}
	return rl.Wait(ctx)
}

func (r *RateLimiterRegistry) TryAcquire(key string) bool {
	r.mu.RLock()
	rl := r.limiters[key]
	r.mu.RUnlock()
	if rl == nil {
		return true
	}
	return rl.TryAcquire()
}

func (r *RateLimiterRegistry) RegisterCandidates(candidates []FallbackCandidate) {
	for _, c := range candidates {
		if c.RPM > 0 {
			r.Register(c.StableKey(), c.RPM)
		}
	}
}
