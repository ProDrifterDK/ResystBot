package providers

import (
	"errors"
	"testing"
)

func TestClassifyAuthErrorKinds(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want AuthErrorKind
	}{
		{"invalid key", errors.New(`Status: 401 Body: {"error":{"message":"Incorrect API key provided"}}`), AuthErrorInvalidAPIKey},
		{"missing key", errors.New("no API key is configured for this provider"), AuthErrorMissingAPIKey},
		{"expired token", errors.New("OAuth token expired; re-authenticate"), AuthErrorExpiredToken},
		{"generic 401", errors.New("API request failed: Status: 401"), AuthErrorGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClassifyAuthError(tt.err)
			if !ok || got != tt.want {
				t.Fatalf("ClassifyAuthError() = %q, %t; want %q, true", got, ok, tt.want)
			}
		})
	}
}

func TestClassifyAuthErrorRejectsMixedFallbackFailures(t *testing.T) {
	err := &FallbackExhaustedError{Attempts: []FallbackAttempt{
		{Reason: FailoverAuth, Error: errors.New("401 unauthorized")},
		{Reason: FailoverRateLimit, Error: errors.New("429 rate limit")},
	}}
	if got, ok := ClassifyAuthError(err); ok {
		t.Fatalf("ClassifyAuthError() = %q, true; want no auth classification", got)
	}
}

func TestClassifyAuthErrorFromExhaustedAuthFailures(t *testing.T) {
	err := &FallbackExhaustedError{Attempts: []FallbackAttempt{
		{Reason: FailoverAuth, Error: errors.New("401 unauthorized")},
		{Reason: FailoverAuth, Error: errors.New("403 forbidden")},
	}}
	if got, ok := ClassifyAuthError(err); !ok || got != AuthErrorGeneric {
		t.Fatalf("ClassifyAuthError() = %q, %t; want %q, true", got, ok, AuthErrorGeneric)
	}
}
