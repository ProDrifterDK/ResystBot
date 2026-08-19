package providers

import (
	"errors"
	"regexp"
	"strings"
)

type AuthErrorKind string

const (
	AuthErrorInvalidAPIKey AuthErrorKind = "invalid_api_key"
	AuthErrorMissingAPIKey AuthErrorKind = "missing_api_key"
	AuthErrorExpiredToken  AuthErrorKind = "expired_token"
	AuthErrorGeneric       AuthErrorKind = "auth"
)

var (
	invalidAPIKeyPattern = regexp.MustCompile(
		`(?i)\b(?:invalid|incorrect|malformed|wrong)[-_\s]+(?:api[-_\s]*)?key\b|\b(?:api[-_\s]*)?key[-_\s]+(?:is[-_\s]+)?(?:invalid|incorrect|malformed|wrong)\b|\binvalid[-_]?api[-_]?key\b`,
	)
	missingAPIKeyPattern = regexp.MustCompile(
		`(?i)\b(?:missing|no|required|absent)[-_\s]+(?:api[-_\s]*)?key\b|\b(?:api[-_\s]*)?key[-_\s]+(?:is[-_\s]+)?(?:missing|required|not[-_\s]+configured)\b|\bno api key is configured\b`,
	)
	expiredTokenPattern = regexp.MustCompile(
		`(?i)\b(?:token|credential|session|login|oauth)[-_\s]*(?:is[-_\s]+)?(?:expired|invalidated|revoked)\b|\b(?:expired[-_\s]+token|token[-_\s]+expired|re-authenticate)\b`,
	)
	genericAuthPattern = regexp.MustCompile(
		`(?i)\b(?:unauthorized|forbidden|authentication[-_\s]+(?:failed|required)|access[-_\s]+denied|status[:\s]+40[13])\b`,
	)
)

// ClassifyAuthError returns a user-actionable authentication failure kind.
func ClassifyAuthError(err error) (AuthErrorKind, bool) {
	if err == nil {
		return "", false
	}

	var exhausted *FallbackExhaustedError
	if errors.As(err, &exhausted) && exhausted != nil {
		return classifyFallbackExhaustedAuthError(exhausted)
	}

	msg := authErrorText(err)
	if missingAPIKeyPattern.MatchString(msg) {
		return AuthErrorMissingAPIKey, true
	}
	if expiredTokenPattern.MatchString(msg) {
		return AuthErrorExpiredToken, true
	}
	if invalidAPIKeyPattern.MatchString(msg) {
		return AuthErrorInvalidAPIKey, true
	}
	if hasStructuredAuthError(err) || genericAuthPattern.MatchString(msg) {
		return AuthErrorGeneric, true
	}
	return "", false
}

func authErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func hasStructuredAuthError(err error) bool {
	var failErr *FailoverError
	return errors.As(err, &failErr) && failErr != nil && failErr.Reason == FailoverAuth
}

func classifyFallbackExhaustedAuthError(err *FallbackExhaustedError) (AuthErrorKind, bool) {
	if err == nil {
		return "", false
	}

	var messages []string
	nonSkipped := 0
	for _, attempt := range err.Attempts {
		if attempt.Skipped {
			continue
		}
		nonSkipped++
		if attempt.Reason != FailoverAuth {
			return "", false
		}
		if attempt.Error != nil {
			messages = append(messages, authErrorText(attempt.Error))
		}
	}
	if nonSkipped == 0 {
		return "", false
	}

	msg := strings.Join(messages, "\n")
	if missingAPIKeyPattern.MatchString(msg) {
		return AuthErrorMissingAPIKey, true
	}
	if expiredTokenPattern.MatchString(msg) {
		return AuthErrorExpiredToken, true
	}
	if invalidAPIKeyPattern.MatchString(msg) {
		return AuthErrorInvalidAPIKey, true
	}
	return AuthErrorGeneric, true
}
