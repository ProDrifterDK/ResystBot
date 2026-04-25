package hooks

import (
	"regexp"
	"strings"
	"sync"
)

var matcherCache sync.Map

// MatchTool checks if a tool name matches a pipe-separated regex pattern.
// Empty pattern matches all tools. Uses cached compiled regexes.
func MatchTool(pattern, toolName string) bool {
	if pattern == "" {
		return true
	}

	re, err := getCompiledRegex(pattern)
	if err != nil {
		for _, p := range strings.Split(pattern, "|") {
			p = strings.TrimSpace(p)
			if p == toolName {
				return true
			}
		}
		return false
	}

	return re.MatchString(toolName)
}

func getCompiledRegex(pattern string) (*regexp.Regexp, error) {
	if cached, ok := matcherCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}

	// Build a single alternation regex from pipe-separated patterns
	parts := strings.Split(pattern, "|")
	regexParts := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		regexParts = append(regexParts, p)
	}

	if len(regexParts) == 0 {
		matcherCache.Store(pattern, regexp.MustCompile(".*"))
		re, _ := matcherCache.Load(pattern)
		return re.(*regexp.Regexp), nil
	}

	combined := "^(?:" + strings.Join(regexParts, "|") + ")$"
	re, err := regexp.Compile(combined)
	if err != nil {
		return nil, err
	}

	actual, _ := matcherCache.LoadOrStore(pattern, re)
	return actual.(*regexp.Regexp), nil
}
