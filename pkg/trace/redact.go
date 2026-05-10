package trace

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	RedactedPlaceholder = "[REDACTED]"
	TruncationMarker    = "...[TRUNCATED]"
)

type replaceRule struct {
	pattern  *regexp.Regexp
	replacer func([]string) string
}

// Redactor removes common secret formats deterministically before persistence.
type Redactor struct {
	rules []replaceRule
}

func NewRedactor() *Redactor {
	return &Redactor{rules: []replaceRule{
		{
			pattern:  regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
			replacer: func(_ []string) string { return RedactedPlaceholder },
		},
		{
			pattern:  regexp.MustCompile(`(?i)\b(bearer)(\s+)([^\s"']+)`),
			replacer: func(m []string) string { return m[1] + m[2] + RedactedPlaceholder },
		},
		{
			pattern: regexp.MustCompile(`(?i)\b(export\s+)?([A-Z0-9_]*(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PRIVATE[_-]?KEY|AUTH(?:ORIZATION)?|CREDENTIALS?)[A-Z0-9_]*)(\s*=\s*)([^\s]+)`),
			replacer: func(m []string) string {
				return m[1] + m[2] + m[3] + RedactedPlaceholder
			},
		},
		{
			pattern:  regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|apikey|secret|token|password|passwd|auth|credential)["']?\s*[:=]\s*["']?)([^"'\s,}&]+)(["']?)`),
			replacer: func(m []string) string { return m[1] + RedactedPlaceholder + m[3] },
		},
		{
			pattern:  regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s:/@]+:)([^\s/@]+)(@)`),
			replacer: func(m []string) string { return m[1] + RedactedPlaceholder + m[3] },
		},
		{
			pattern:  regexp.MustCompile(`(?i)(^|[\s"'=:(])((?:~?/|/)[^\s"']*(?:\.aws/(?:credentials|config)|\.docker/config\.json|\.git-credentials|\.netrc|\.npmrc|\.pypirc|\.ssh/(?:id_[^\s"']+|config)|[^\s"']*\.(?:pem|p12|pfx|key)))([\s"'):,]|$)`),
			replacer: func(m []string) string { return m[1] + RedactedPlaceholder + m[3] },
		},
		{
			pattern:  regexp.MustCompile(`(?i)\b(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`),
			replacer: func(_ []string) string { return RedactedPlaceholder },
		},
	}}
}

func (r *Redactor) RedactString(s string) string {
	if s == "" {
		return ""
	}
	redacted := s
	for _, rule := range r.rules {
		redacted = rule.pattern.ReplaceAllStringFunc(redacted, func(match string) string {
			groups := rule.pattern.FindStringSubmatch(match)
			if len(groups) == 0 {
				return RedactedPlaceholder
			}
			return rule.replacer(groups)
		})
	}
	return redacted
}

func (r *Redactor) SanitizeString(s string, maxChars int) string {
	return TruncateString(r.RedactString(s), maxChars)
}

func (r *Redactor) SanitizeValue(v any, maxChars int) any {
	switch value := v.(type) {
	case string:
		return r.SanitizeString(value, maxChars)
	case map[string]any:
		cloned := make(map[string]any, len(value))
		for k, inner := range value {
			cloned[k] = r.SanitizeValue(inner, maxChars)
		}
		return cloned
	case []any:
		cloned := make([]any, len(value))
		for i, inner := range value {
			cloned[i] = r.SanitizeValue(inner, maxChars)
		}
		return cloned
	case []string:
		cloned := make([]string, len(value))
		for i, inner := range value {
			cloned[i] = r.SanitizeString(inner, maxChars)
		}
		return cloned
	case fmt.Stringer:
		return r.SanitizeString(value.String(), maxChars)
	default:
		return v
	}
}

func TruncateString(s string, maxChars int) string {
	if maxChars <= 0 || s == "" {
		if maxChars <= 0 {
			return ""
		}
		return s
	}
	if utf8.RuneCountInString(s) <= maxChars {
		return s
	}
	markerRunes := []rune(TruncationMarker)
	if maxChars <= len(markerRunes) {
		return string(markerRunes[:maxChars])
	}
	runes := []rune(s)
	keep := maxChars - len(markerRunes)
	return string(runes[:keep]) + TruncationMarker
}

func countChars(s string) int {
	if s == "" {
		return 0
	}
	return utf8.RuneCountInString(s)
}

func defaultRedactor(r *Redactor) *Redactor {
	if r != nil {
		return r
	}
	return NewRedactor()
}

func sanitizeStringPointer(r *Redactor, s *string, maxChars int) *string {
	if s == nil {
		return nil
	}
	sanitized := r.SanitizeString(strings.Clone(*s), maxChars)
	return &sanitized
}
