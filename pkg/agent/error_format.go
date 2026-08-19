package agent

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func formatProcessingError(err error) string {
	if kind, ok := providers.ClassifyAuthError(err); ok {
		return fmt.Sprintf("%s\n\nOriginal error: %s", authErrorFriendlyMessage(kind), err.Error())
	}
	return fmt.Sprintf("Error processing message: %v", err)
}

func authErrorFriendlyMessage(kind providers.AuthErrorKind) string {
	switch kind {
	case providers.AuthErrorInvalidAPIKey:
		return "Authentication failed: the API key appears to be invalid. Check the API key configured for this model or provider."
	case providers.AuthErrorMissingAPIKey:
		return "Authentication failed: no API key is configured for this model or provider. Add an API key in the model settings or config."
	case providers.AuthErrorExpiredToken:
		return "Authentication failed: the saved login or token appears to be expired. Re-authenticate the provider."
	default:
		return "Authentication failed: check the API key, token, OAuth login, or provider permissions for this model."
	}
}

func transientLLMFailure(err error, model string) (providers.FailoverReason, bool) {
	classifiedErr := providers.ClassifyError(err, "", model)
	if classifiedErr == nil {
		return "", false
	}
	switch classifiedErr.Reason {
	case providers.FailoverTimeout, providers.FailoverNetwork, providers.FailoverRateLimit, providers.FailoverOverloaded:
		return classifiedErr.Reason, true
	default:
		return "", false
	}
}
