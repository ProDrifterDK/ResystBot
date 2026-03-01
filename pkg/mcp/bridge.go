package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	ToolNamePrefix    = "mcp"
	ToolNameSeparator = "__"
)

type MCPToolBridge struct {
	serverName string
	mcpTool    mcpgo.Tool
	manager    *Manager
	timeout    time.Duration
}

func NewMCPToolBridge(serverName string, mcpTool mcpgo.Tool, manager *Manager, timeout time.Duration) *MCPToolBridge {
	return &MCPToolBridge{
		serverName: serverName,
		mcpTool:    mcpTool,
		manager:    manager,
		timeout:    timeout,
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

	return convertMCPResult(result)
}

func convertMCPResult(result *mcpgo.CallToolResult) *tools.ToolResult {
	if result == nil {
		return tools.ErrorResult("MCP tool returned nil result")
	}

	var sb strings.Builder
	for _, content := range result.Content {
		text := mcpgo.GetTextFromContent(content)
		if text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(text)
		} else {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(fmt.Sprintf("[non-text content: %T]", content))
		}
	}

	text := sb.String()
	if text == "" {
		text = "(empty result)"
	}

	if result.IsError {
		return tools.ErrorResult(text)
	}

	return tools.NewToolResult(text)
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

func BridgeTools(manager *Manager, serverFilter []string) []tools.Tool {
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

func RegisterMCPTools(manager *Manager, registry *tools.ToolRegistry, serverNames []string) int {
	bridged := BridgeTools(manager, serverNames)

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
