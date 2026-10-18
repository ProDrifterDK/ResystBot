package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	ToolNamePrefix    = "mcp"
	ToolNameSeparator = "__"

	// defaultMCPInlineTextRunes mirrors config.DefaultMCPMaxInlineTextChars.
	// It is kept local so a bridge built without explicit configuration still
	// bounds how much MCP text reaches model context.
	defaultMCPInlineTextRunes = 16 * 1024
)

type MCPToolBridge struct {
	serverName         string
	mcpTool            mcpgo.Tool
	manager            *Manager
	timeout            time.Duration
	workspace          string
	maxInlineTextRunes int
}

func NewMCPToolBridge(serverName string, mcpTool mcpgo.Tool, manager *Manager, timeout time.Duration) *MCPToolBridge {
	return &MCPToolBridge{
		serverName:         serverName,
		mcpTool:            mcpTool,
		manager:            manager,
		timeout:            timeout,
		maxInlineTextRunes: defaultMCPInlineTextRunes,
	}
}

// SetWorkspace sets the directory where oversized MCP text results are stored
// as local artifacts. A blank workspace disables artifact persistence, leaving
// the previous inline behavior in place.
func (b *MCPToolBridge) SetWorkspace(workspace string) {
	b.workspace = strings.TrimSpace(workspace)
}

// SetMaxInlineTextRunes overrides the inline text limit. Non-positive values
// are ignored so the default stays in force.
func (b *MCPToolBridge) SetMaxInlineTextRunes(limit int) {
	if limit > 0 {
		b.maxInlineTextRunes = limit
	}
}

func (b *MCPToolBridge) Name() string {
	return fmt.Sprintf("%s%s%s%s%s",
		ToolNamePrefix, ToolNameSeparator,
		b.serverName, ToolNameSeparator,
		b.mcpTool.Name)
}

func (b *MCPToolBridge) Description() string {
	desc := b.mcpTool.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from %s server", b.serverName)
	}
	return fmt.Sprintf("[MCP:%s] %s", b.serverName, desc)
}

// Parameters returns the tool's input schema for the provider payload and for local
// argument validation.
//
// It must stay a faithful projection of the schema the MCP server declared. The schema is
// carried as a JSON Schema object, so dropping a field silently corrupts it: $defs holds the
// definitions that properties reference with $ref, and additionalProperties tells the
// validator whether unknown argument keys are allowed. Rebuild this field by field only
// against the full set of fields on mcpgo.ToolInputSchema.
func (b *MCPToolBridge) Parameters() map[string]any {
	schema := make(map[string]any)

	if b.mcpTool.InputSchema.Type != "" {
		schema["type"] = b.mcpTool.InputSchema.Type
	} else {
		schema["type"] = "object"
	}

	if b.mcpTool.InputSchema.Properties != nil {
		schema["properties"] = b.mcpTool.InputSchema.Properties
	}

	if len(b.mcpTool.InputSchema.Required) > 0 {
		schema["required"] = b.mcpTool.InputSchema.Required
	}

	// Dropping $defs left every #/$defs/... reference dangling. Lenient providers ignore
	// unresolvable refs; strict consumers reject the whole tool list, so the agent loop
	// failed on its first iteration with a JSON schema error rather than a tool error.
	if len(b.mcpTool.InputSchema.Defs) > 0 {
		schema["$defs"] = b.mcpTool.InputSchema.Defs
	}

	// Omitting additionalProperties made validateToolArgs fall back to deny-by-default,
	// rejecting argument keys every MCP server explicitly allows.
	if b.mcpTool.InputSchema.AdditionalProperties != nil {
		schema["additionalProperties"] = b.mcpTool.InputSchema.AdditionalProperties
	}

	return schema
}

func (b *MCPToolBridge) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	timeout := b.timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	conn, ok := b.manager.GetConnection(b.serverName)
	if !ok {
		return tools.ErrorResult(fmt.Sprintf("MCP server %q not found", b.serverName))
	}

	conn.mu.RLock()
	connected := conn.connected
	conn.mu.RUnlock()

	if !connected {
		if err := b.manager.Reconnect(ctx, b.serverName); err != nil {
			return tools.ErrorResult(fmt.Sprintf("MCP server %q not connected and reconnection failed: %v", b.serverName, err))
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := b.manager.CallTool(callCtx, b.serverName, b.mcpTool.Name, args)
	if err != nil {
		logger.ErrorCF("mcp", "MCP tool call failed", map[string]any{
			"server": b.serverName,
			"tool":   b.mcpTool.Name,
			"error":  err.Error(),
		})

		if ctx.Err() != nil {
			return tools.ErrorResult(fmt.Sprintf("MCP tool %q timed out after %v", b.mcpTool.Name, timeout)).WithError(ctx.Err())
		}

		if isConnectionError(err) {
			go b.manager.Reconnect(context.Background(), b.serverName) //nolint:errcheck
			return tools.ErrorResult(fmt.Sprintf("MCP server %q connection lost: %v", b.serverName, err)).WithError(err)
		}

		return tools.ErrorResult(fmt.Sprintf("MCP tool %q on server %q failed: %v", b.mcpTool.Name, b.serverName, err)).WithError(err)
	}

	return b.convertMCPResult(result)
}

// convertMCPResult maps an MCP call result onto a ToolResult.
//
// Text is tracked twice: inlineText is what the model would normally see (with
// short placeholders for non-text content), while rawText is the unmodified
// textual payload. When rawText is oversized it is persisted to a local
// artifact and only a short note plus the artifact path reach the model,
// keeping context bounded (port of upstream d8c5183d, adapted to our mcp-go
// bridge).
func (b *MCPToolBridge) convertMCPResult(result *mcpgo.CallToolResult) *tools.ToolResult {
	if result == nil {
		return tools.ErrorResult("MCP tool returned nil result")
	}

	var inlineText strings.Builder
	var rawText strings.Builder
	for _, content := range result.Content {
		text, isText := mcpContentText(content)
		if !isText {
			appendLine(&inlineText, describeMCPNonTextContent(content))
			continue
		}
		appendLine(&inlineText, text)
		appendLine(&rawText, text)
	}

	if artifact := b.persistLargeTextArtifact(rawText.String(), result.IsError); artifact != nil {
		return artifact
	}

	text := inlineText.String()
	if text == "" {
		text = "(empty result)"
	}

	if result.IsError {
		return tools.ErrorResult(text)
	}

	return tools.NewToolResult(text)
}

// appendLine appends a newline-separated chunk to sb.
func appendLine(sb *strings.Builder, s string) {
	if sb.Len() > 0 {
		sb.WriteString("\n")
	}
	sb.WriteString(s)
}

// mcpContentText returns the textual payload of an MCP content item and whether
// the item is genuinely text.
//
// We deliberately do not use mcp-go's GetTextFromContent here: its fallback for
// non-text content is fmt.Sprintf("%v", content), which stringifies the whole
// struct and leaks entire base64 image/audio payloads into model context.
func mcpContentText(content mcpgo.Content) (string, bool) {
	switch c := content.(type) {
	case mcpgo.TextContent:
		return c.Text, true
	case mcpgo.EmbeddedResource:
		switch r := c.Resource.(type) {
		case mcpgo.TextResourceContents:
			return r.Text, true
		case *mcpgo.TextResourceContents:
			return r.Text, true
		}
	}
	return "", false
}

// describeMCPNonTextContent produces a short, bounded placeholder for a
// non-text content item so the model knows something was returned without
// paying for the payload.
func describeMCPNonTextContent(content mcpgo.Content) string {
	switch c := content.(type) {
	case mcpgo.ImageContent:
		return fmt.Sprintf("[non-text content: image (%s)]", c.MIMEType)
	case mcpgo.AudioContent:
		return fmt.Sprintf("[non-text content: audio (%s)]", c.MIMEType)
	case mcpgo.ResourceLink:
		return fmt.Sprintf("[resource link: %s]", c.URI)
	case mcpgo.EmbeddedResource:
		return "[non-text content: embedded resource]"
	default:
		return fmt.Sprintf("[non-text content: %T]", content)
	}
}

// persistLargeTextArtifact stores an oversized MCP text payload under the agent
// workspace and returns a ToolResult that points the model at the file instead
// of the payload. It returns nil when the text fits inline, is empty, or no
// workspace is configured, so the caller falls back to normal inline handling.
func (b *MCPToolBridge) persistLargeTextArtifact(text string, isError bool) *tools.ToolResult {
	text = strings.TrimSpace(text)

	limit := b.maxInlineTextRunes
	if limit <= 0 {
		limit = defaultMCPInlineTextRunes
	}
	size := utf8.RuneCountInString(text)
	if text == "" || size <= limit || b.workspace == "" {
		return nil
	}

	dir := filepath.Join(b.workspace, ".artifacts", "mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return b.largeTextArtifactFallback(text, isError, err)
	}
	// TODO: add lifecycle cleanup/retention for MCP artifact files.

	pattern := fmt.Sprintf(
		"%s_%s_*.txt",
		sanitizeArtifactComponent(b.serverName),
		sanitizeArtifactComponent(b.mcpTool.Name),
	)
	tmpFile, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return b.largeTextArtifactFallback(text, isError, err)
	}
	path := tmpFile.Name()
	if _, err = tmpFile.WriteString(text); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(path)
		return b.largeTextArtifactFallback(text, isError, err)
	}
	if err = tmpFile.Close(); err != nil {
		_ = os.Remove(path)
		return b.largeTextArtifactFallback(text, isError, err)
	}

	logger.InfoCF("mcp", "Stored oversized MCP text result as artifact", map[string]any{
		"server": b.serverName,
		"tool":   b.mcpTool.Name,
		"chars":  size,
		"path":   path,
	})

	return &tools.ToolResult{
		ForLLM: fmt.Sprintf(
			"[MCP returned a large text result (%d chars); omitted from model context and saved as a local artifact.]\n"+
				"Local artifact path: [file:%s]\n"+
				"Use the read_file or exec tools to inspect it.",
			size, path,
		),
		IsError: isError,
	}
}

// largeTextArtifactFallback omits an oversized payload when it could not be
// persisted. Keeping the text out of context matters more than keeping it, so
// the model is told the result was dropped rather than being handed the whole
// blob (upstream "avoid leaking large text when artifact persistence fails").
func (b *MCPToolBridge) largeTextArtifactFallback(text string, isError bool, err error) *tools.ToolResult {
	size := utf8.RuneCountInString(text)
	logger.WarnCF("mcp", "Failed to persist large MCP text artifact", map[string]any{
		"server": b.serverName,
		"tool":   b.mcpTool.Name,
		"chars":  size,
		"error":  err.Error(),
	})

	return &tools.ToolResult{
		ForLLM: fmt.Sprintf(
			"[MCP returned a large text result (%d chars); omitted from model context because artifact persistence failed.]",
			size,
		),
		IsError: isError,
	}
}

// sanitizeArtifactComponent makes a server or tool name safe to embed in a
// generated artifact filename.
func sanitizeArtifactComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unnamed"
	}

	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func isConnectionError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "transport") ||
		strings.Contains(msg, "closed")
}

// BridgeOptions configures how MCP tools are bridged into the local registry.
type BridgeOptions struct {
	// Workspace is where oversized MCP text results are stored as artifacts.
	// Blank disables artifact persistence.
	Workspace string
	// MaxInlineTextRunes bounds how much MCP text stays inline in model
	// context. Zero falls back to the bridge default.
	MaxInlineTextRunes int
}

func BridgeTools(manager *Manager, serverFilter []string, opts BridgeOptions) []tools.Tool {
	var toolsMap map[string][]mcpgo.Tool
	if len(serverFilter) == 0 {
		toolsMap = manager.GetTools()
	} else {
		toolsMap = manager.GetToolsForServers(serverFilter)
	}

	var result []tools.Tool
	for serverName, mcpTools := range toolsMap {
		conn, ok := manager.GetConnection(serverName)
		if !ok {
			continue
		}

		timeout := time.Duration(conn.Config.Timeout) * time.Second

		for _, mcpTool := range mcpTools {
			bridge := NewMCPToolBridge(serverName, mcpTool, manager, timeout)
			bridge.SetWorkspace(opts.Workspace)
			bridge.SetMaxInlineTextRunes(opts.MaxInlineTextRunes)
			result = append(result, bridge)

			logger.DebugCF("mcp", "Bridged MCP tool", map[string]any{
				"name":        bridge.Name(),
				"server":      serverName,
				"original":    mcpTool.Name,
				"description": mcpTool.Description,
			})
		}
	}

	return result
}

func RegisterMCPTools(manager *Manager, registry *tools.ToolRegistry, serverNames []string, opts BridgeOptions) int {
	bridged := BridgeTools(manager, serverNames, opts)

	for _, tool := range bridged {
		registry.Register(tool)
	}

	return len(bridged)
}

func ParseMCPToolName(name string) (serverName, toolName string, ok bool) {
	parts := strings.SplitN(name, ToolNameSeparator, 3)
	if len(parts) != 3 || parts[0] != ToolNamePrefix {
		return "", "", false
	}
	return parts[1], parts[2], true
}
