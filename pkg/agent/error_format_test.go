package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestFormatProcessingErrorInvalidAPIKey(t *testing.T) {
	err := errors.New(`LLM call failed after retries: API request failed: Status: 401 Body: {"error":{"message":"Incorrect API key provided"}}`)
	got := formatProcessingError(err)
	if !strings.Contains(got, "API key appears to be invalid") {
		t.Fatalf("formatted error missing friendly API key hint: %q", got)
	}
	if !strings.Contains(got, "Original error:") || !strings.Contains(got, err.Error()) {
		t.Fatalf("formatted error missing original error: %q", got)
	}
}

func TestFormatProcessingErrorKeepsGenericMessage(t *testing.T) {
	got := formatProcessingError(errors.New("database unavailable"))
	if !strings.HasPrefix(got, "Error processing message:") {
		t.Fatalf("generic error format changed: %q", got)
	}
}

func TestTransientLLMFailureUsesProviderClassifier(t *testing.T) {
	reason, ok := transientLLMFailure(errors.New("API request failed: Status: 503 Body: unavailable"), "test-model")
	if !ok || reason != "timeout" {
		t.Fatalf("transientLLMFailure() = %q, %t; want timeout, true", reason, ok)
	}
	if _, ok := transientLLMFailure(errors.New("API request failed: Status: 400 Body: bad request"), "test-model"); ok {
		t.Fatal("transientLLMFailure() classified format error as retryable")
	}
}
