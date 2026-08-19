package config

import "strings"

// NormalizeMCPTransportType canonicalizes MCP transport names.
// "http" is the streamable HTTP request-response transport; the
// streamable-http spellings are accepted as aliases.
func NormalizeMCPTransportType(transport string) string {
	normalized := strings.ToLower(strings.TrimSpace(transport))
	switch normalized {
	case "streamable-http", "streamable_http", "streamablehttp":
		return "http"
	default:
		return normalized
	}
}
