package providers

import (
	"context"
	"errors"
	"testing"
	"time"
)

func makeCandidate(provider, model string) FallbackCandidate {
	return FallbackCandidate{Provider: provider, Model: model}
}

func successRun(content string) func(ctx context.Context, provider, model string) (*LLMResponse, error) {
	return func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		return &LLMResponse{Content: content, FinishReason: "stop"}, nil
	}
}

func TestFallback_SingleCandidate_Success(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{makeCandidate("openai", "gpt-4")}
	result, err := fc.Execute(context.Background(), candidates, successRun("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Response.Content != "hello" {
		t.Errorf("content = %q, want hello", result.Response.Content)
	}
	if result.Provider != "openai" || result.Model != "gpt-4" {
		t.Errorf("provider/model = %s/%s, want openai/gpt-4", result.Provider, result.Model)
	}
}

func TestFallback_SecondCandidateSuccess(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4"),
		makeCandidate("anthropic", "claude-opus"),
	}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		if attempt == 1 {
			return nil, errors.New("rate limit exceeded")
		}
		return &LLMResponse{Content: "from claude", FinishReason: "stop"}, nil
	}

	result, err := fc.Execute(context.Background(), candidates, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", result.Provider)
	}
	if result.Response.Content != "from claude" {
		t.Errorf("content = %q, want 'from claude'", result.Response.Content)
	}
	if len(result.Attempts) != 1 {
		t.Errorf("attempts = %d, want 1 (failed attempt recorded)", len(result.Attempts))
	}
}

func TestFallback_RetryOnNetworkError(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4"),
		makeCandidate("anthropic", "claude-opus"),
	}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		if attempt == 1 {
			return nil, errors.New("dial tcp 192.168.1.1:443: connection refused")
		}
		return &LLMResponse{Content: "from claude", FinishReason: "stop"}, nil
	}

	result, err := fc.Execute(context.Background(), candidates, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", result.Provider)
	}
	if len(result.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(result.Attempts))
	}
	if result.Attempts[0].Reason != FailoverNetwork {
		t.Errorf("reason = %q, want network", result.Attempts[0].Reason)
	}
}

func TestFallback_AllFail(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4"),
		makeCandidate("anthropic", "claude"),
		makeCandidate("groq", "llama"),
	}

	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		return nil, errors.New("rate limit exceeded")
	}

	_, err := fc.Execute(context.Background(), candidates, run)
	if err == nil {
		t.Fatal("expected error when all candidates fail")
	}
	var exhausted *FallbackExhaustedError
	if !errors.As(err, &exhausted) {
		t.Errorf("expected FallbackExhaustedError, got %T: %v", err, err)
	}
	if len(exhausted.Attempts) != 3 {
		t.Errorf("attempts = %d, want 3", len(exhausted.Attempts))
	}
}

func TestFallback_ContextCanceled(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	ctx, cancel := context.WithCancel(context.Background())
	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4"),
		makeCandidate("anthropic", "claude"),
	}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		if attempt == 1 {
			cancel() // cancel context
			return nil, context.Canceled
		}
		t.Error("should not reach second candidate after cancel")
		return nil, nil
	}

	_, err := fc.Execute(ctx, candidates, run)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestFallback_NonRetriableError(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4"),
		makeCandidate("anthropic", "claude"),
	}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		return nil, errors.New("string should match pattern")
	}

	_, err := fc.Execute(context.Background(), candidates, run)
	if err == nil {
		t.Fatal("expected error for non-retriable")
	}
	var fe *FailoverError
	if !errors.As(err, &fe) {
		t.Fatalf("expected FailoverError, got %T", err)
	}
	if fe.Reason != FailoverFormat {
		t.Errorf("reason = %q, want format", fe.Reason)
	}
	if attempt != 1 {
		t.Errorf("attempt = %d, want 1 (non-retriable should not try next)", attempt)
	}
}

func TestFallback_CooldownSkip(t *testing.T) {
	now := time.Now()
	ct, _ := newTestTracker(now)
	fc := NewFallbackChain(ct)

	// Put openai/gpt-4 in cooldown using the per-model key
	ct.MarkFailure(ModelKey("openai", "gpt-4"), FailoverRateLimit)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4"),
		makeCandidate("anthropic", "claude"),
	}

	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		if provider == "openai" {
			t.Error("should not call openai (in cooldown)")
		}
		return &LLMResponse{Content: "claude response", FinishReason: "stop"}, nil
	}

	result, err := fc.Execute(context.Background(), candidates, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", result.Provider)
	}
	// Should have 1 skipped attempt
	skipped := 0
	for _, a := range result.Attempts {
		if a.Skipped {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
}

func TestFallback_AllInCooldown(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	// Put all provider/model pairs in cooldown
	ct.MarkFailure(ModelKey("openai", "gpt-4"), FailoverRateLimit)
	ct.MarkFailure(ModelKey("anthropic", "claude"), FailoverBilling)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4"),
		makeCandidate("anthropic", "claude"),
	}

	_, err := fc.Execute(context.Background(), candidates,
		func(ctx context.Context, provider, model string) (*LLMResponse, error) {
			t.Error("should not call any provider (all in cooldown)")
			return nil, nil
		})

	if err == nil {
		t.Fatal("expected error when all in cooldown")
	}
	var exhausted *FallbackExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected FallbackExhaustedError, got %T", err)
	}
}

func TestFallback_NoCandidates(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	_, err := fc.Execute(context.Background(), nil, successRun("ok"))
	if err == nil {
		t.Error("expected error for empty candidates")
	}
}

func TestFallback_EmptyFallbacks(t *testing.T) {
	// Single primary, no fallbacks: should work like direct call
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{makeCandidate("openai", "gpt-4")}
	result, err := fc.Execute(context.Background(), candidates, successRun("ok"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Response.Content != "ok" {
		t.Error("expected success with single candidate")
	}
}

func TestFallback_UnclassifiedError(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4"),
		makeCandidate("anthropic", "claude"),
	}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		return nil, errors.New("completely unknown internal error")
	}

	_, err := fc.Execute(context.Background(), candidates, run)
	if err == nil {
		t.Fatal("expected error for unclassified error")
	}
	if attempt != 1 {
		t.Errorf("attempt = %d, want 1 (should not fallback on unclassified)", attempt)
	}
}

func TestFallback_SuccessResetsCooldown(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{makeCandidate("openai", "gpt-4")}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		if attempt == 1 {
			ct.MarkFailure(ModelKey("openai", "gpt-4"), FailoverRateLimit)
		}
		return &LLMResponse{Content: "ok", FinishReason: "stop"}, nil
	}

	_, err := fc.Execute(context.Background(), candidates, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ct.IsAvailable(ModelKey("openai", "gpt-4")) {
		t.Error("success should reset cooldown")
	}
}

// --- Image Fallback Tests ---

func TestImageFallback_Success(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{makeCandidate("openai", "gpt-4o")}
	result, err := fc.ExecuteImage(context.Background(), candidates, successRun("image result"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Response.Content != "image result" {
		t.Error("expected image result")
	}
}

func TestImageFallback_DimensionError(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4o"),
		makeCandidate("anthropic", "claude"),
	}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		return nil, errors.New("image dimensions exceed max 4096x4096")
	}

	_, err := fc.ExecuteImage(context.Background(), candidates, run)
	if err == nil {
		t.Fatal("expected error for image dimension error")
	}
	if attempt != 1 {
		t.Errorf("attempt = %d, want 1 (image dimension error should not retry)", attempt)
	}
}

func TestImageFallback_SizeError(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4o"),
		makeCandidate("anthropic", "claude"),
	}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		return nil, errors.New("image exceeds 20 mb")
	}

	_, err := fc.ExecuteImage(context.Background(), candidates, run)
	if err == nil {
		t.Fatal("expected error for image size error")
	}
	if attempt != 1 {
		t.Errorf("attempt = %d, want 1 (image size error should not retry)", attempt)
	}
}

func TestImageFallback_RetryOnOtherErrors(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "gpt-4o"),
		makeCandidate("anthropic", "claude-sonnet"),
	}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		if attempt == 1 {
			return nil, errors.New("rate limit exceeded")
		}
		return &LLMResponse{Content: "image ok", FinishReason: "stop"}, nil
	}

	result, err := fc.ExecuteImage(context.Background(), candidates, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", result.Provider)
	}
}

func TestImageFallback_NoCandidates(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	_, err := fc.ExecuteImage(context.Background(), nil, successRun("ok"))
	if err == nil {
		t.Error("expected error for empty candidates")
	}
}

// --- ResolveCandidates Tests ---

func TestResolveCandidates_Simple(t *testing.T) {
	cfg := ModelConfig{
		Primary:   "gpt-4",
		Fallbacks: []string{"anthropic/claude-opus", "groq/llama-3"},
	}

	candidates := ResolveCandidates(cfg, "openai")
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d, want 3", len(candidates))
	}

	if candidates[0].Provider != "openai" || candidates[0].Model != "gpt-4" {
		t.Errorf("candidate[0] = %s/%s, want openai/gpt-4", candidates[0].Provider, candidates[0].Model)
	}
	if candidates[1].Provider != "anthropic" || candidates[1].Model != "claude-opus" {
		t.Errorf("candidate[1] = %s/%s, want anthropic/claude-opus", candidates[1].Provider, candidates[1].Model)
	}
	if candidates[2].Provider != "groq" || candidates[2].Model != "llama-3" {
		t.Errorf("candidate[2] = %s/%s, want groq/llama-3", candidates[2].Provider, candidates[2].Model)
	}
}

func TestResolveCandidates_Deduplication(t *testing.T) {
	cfg := ModelConfig{
		Primary:   "openai/gpt-4",
		Fallbacks: []string{"openai/gpt-4", "anthropic/claude"},
	}

	candidates := ResolveCandidates(cfg, "default")
	if len(candidates) != 2 {
		t.Errorf("candidates = %d, want 2 (duplicate removed)", len(candidates))
	}
}

func TestResolveCandidates_EmptyFallbacks(t *testing.T) {
	cfg := ModelConfig{
		Primary:   "gpt-4",
		Fallbacks: nil,
	}

	candidates := ResolveCandidates(cfg, "openai")
	if len(candidates) != 1 {
		t.Errorf("candidates = %d, want 1", len(candidates))
	}
}

func TestResolveCandidates_EmptyPrimary(t *testing.T) {
	cfg := ModelConfig{
		Primary:   "",
		Fallbacks: []string{"anthropic/claude"},
	}

	candidates := ResolveCandidates(cfg, "openai")
	if len(candidates) != 1 {
		t.Errorf("candidates = %d, want 1", len(candidates))
	}
}

func TestFallbackExhaustedError_Message(t *testing.T) {
	e := &FallbackExhaustedError{
		Attempts: []FallbackAttempt{
			{
				Provider: "openai",
				Model:    "gpt-4",
				Error:    errors.New("rate limited"),
				Reason:   FailoverRateLimit,
				Duration: 500 * time.Millisecond,
			},
			{Provider: "anthropic", Model: "claude", Skipped: true},
		},
	}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
}

// TestFallback_SharedProviderIndependentCooldown reproduces the bug where two
// models under the same provider (e.g. openai/glm-5.1 and openai/glm-5) shared
// a single cooldown entry. When the primary failed, the fallback was incorrectly
// skipped because both used provider="openai" as the cooldown key.
//
// With the fix, cooldown is keyed by ModelKey(provider, model) so each model
// has an independent cooldown state.
func TestFallback_SharedProviderIndependentCooldown(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "glm-5.1"),
		makeCandidate("openai", "glm-5"),
		makeCandidate("ollama-cloud", "gemini-3-flash-preview"),
	}

	attempt := 0
	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		attempt++
		if attempt == 1 {
			return nil, errors.New("rate limit exceeded")
		}
		return &LLMResponse{Content: "from fallback", FinishReason: "stop"}, nil
	}

	result, err := fc.Execute(context.Background(), candidates, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The second candidate (openai/glm-5) should have been attempted — NOT skipped.
	if result.Provider != "openai" || result.Model != "glm-5" {
		t.Errorf("provider/model = %s/%s, want openai/glm-5", result.Provider, result.Model)
	}
	if result.Response.Content != "from fallback" {
		t.Errorf("content = %q, want 'from fallback'", result.Response.Content)
	}

	// Verify: only the first model is in cooldown, second model should be fine
	if ct.IsAvailable(ModelKey("openai", "glm-5.1")) {
		t.Error("openai/glm-5.1 should be in cooldown after failure")
	}
	if !ct.IsAvailable(ModelKey("openai", "glm-5")) {
		t.Error("openai/glm-5 should be available (success resets cooldown)")
	}
}

// TestFallback_SharedProviderCooldownDoesNotCross models verify that putting one
// model in cooldown doesn't affect another model under the same provider.
func TestFallback_SharedProviderCooldownDoesNotCrossModels(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	// Put openai/glm-5.1 into cooldown manually
	ct.MarkFailure(ModelKey("openai", "glm-5.1"), FailoverRateLimit)

	candidates := []FallbackCandidate{
		makeCandidate("openai", "glm-5.1"),
		makeCandidate("openai", "glm-5"),
	}

	run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		if model == "glm-5.1" {
			t.Error("should not call glm-5.1 (in cooldown)")
		}
		return &LLMResponse{Content: "from glm-5", FinishReason: "stop"}, nil
	}

	result, err := fc.Execute(context.Background(), candidates, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Provider != "openai" || result.Model != "glm-5" {
		t.Errorf("provider/model = %s/%s, want openai/glm-5", result.Provider, result.Model)
	}

	skipped := 0
	for _, a := range result.Attempts {
		if a.Skipped {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (only glm-5.1)", skipped)
	}
}

func TestFallback_RateLimited_SkipsCandidate(t *testing.T) {
	ct := NewCooldownTracker()
	rl := NewRateLimiterRegistry()
	fc := NewFallbackChain(ct, rl)

	candidates := []FallbackCandidate{
		{Provider: "openai", Model: "gpt-4", RPM: 1},
		{Provider: "anthropic", Model: "claude"},
	}
	rl.RegisterCandidates(candidates)

	firstResult, err := fc.Execute(context.Background(), candidates, func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		if provider != "openai" {
			t.Fatalf("first provider = %s, want openai", provider)
		}
		return &LLMResponse{Content: "first", FinishReason: "stop"}, nil
	})
	if err != nil {
		t.Fatalf("first Execute error = %v", err)
	}
	if firstResult.Provider != "openai" {
		t.Fatalf("first result provider = %s, want openai", firstResult.Provider)
	}

	var called []string
	secondResult, err := fc.Execute(context.Background(), candidates, func(ctx context.Context, provider, model string) (*LLMResponse, error) {
		called = append(called, provider+"/"+model)
		return &LLMResponse{Content: provider, FinishReason: "stop"}, nil
	})
	if err != nil {
		t.Fatalf("second Execute error = %v", err)
	}
	if len(called) != 1 || called[0] != "anthropic/claude" {
		t.Fatalf("called providers = %v, want [anthropic/claude]", called)
	}
	if secondResult.Provider != "anthropic" {
		t.Fatalf("second result provider = %s, want anthropic", secondResult.Provider)
	}
	if len(secondResult.Attempts) != 1 || !secondResult.Attempts[0].Skipped {
		t.Fatalf("expected one skipped attempt before fallback, got %+v", secondResult.Attempts)
	}
	if secondResult.Attempts[0].Reason != FailoverRateLimit {
		t.Fatalf("skip reason = %s, want %s", secondResult.Attempts[0].Reason, FailoverRateLimit)
	}
}

func TestFallback_RateLimited_WaitsOnLastCandidate(t *testing.T) {
	ct := NewCooldownTracker()
	rl := NewRateLimiterRegistry()
	fc := NewFallbackChain(ct, rl)

	candidates := []FallbackCandidate{{Provider: "openai", Model: "gpt-4", RPM: 1}}
	rl.RegisterCandidates(candidates)

	limiter := rl.limiters[candidates[0].StableKey()]
	limiter.rpm = 1500
	limiter.tokens = 1
	limiter.maxBurst = 1
	limiter.lastTick = time.Now()
	limiter.nowFunc = time.Now

	if _, err := fc.Execute(context.Background(), candidates, successRun("first")); err != nil {
		t.Fatalf("first Execute error = %v", err)
	}

	start := time.Now()
	result, err := fc.Execute(context.Background(), candidates, successRun("second"))
	if err != nil {
		t.Fatalf("second Execute error = %v", err)
	}
	if result.Provider != "openai" {
		t.Fatalf("provider = %s, want openai", result.Provider)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("second Execute waited %s, want at least 30ms", elapsed)
	}
}

func TestFallback_RateLimiterRegistry_NilBackwardCompat(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)
	if fc.rl != nil {
		t.Fatal("expected nil rate limiter registry for backward-compatible constructor")
	}

	result, err := fc.Execute(context.Background(), []FallbackCandidate{makeCandidate("openai", "gpt-4")}, successRun("ok"))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result.Provider != "openai" || result.Model != "gpt-4" {
		t.Fatalf("provider/model = %s/%s, want openai/gpt-4", result.Provider, result.Model)
	}
}
