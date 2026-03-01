package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type ServerConnection struct {
	Name      string
	Client    *mcpclient.Client
	Tools     []mcpgo.Tool
	Config    config.MCPServerConfig
	mu        sync.RWMutex
	connected bool
	lastError error
}

type Manager struct {
	connections map[string]*ServerConnection
	mu          sync.RWMutex
}

func NewManager(ctx context.Context, mcpCfg config.MCPConfig) (*Manager, error) {
	m := &Manager{
		connections: make(map[string]*ServerConnection),
	}

	var lastErr error
	successCount := 0

	for name, cfg := range mcpCfg.Servers {
		if !cfg.IsEnabled() {
			logger.InfoCF("mcp", "Skipping disabled MCP server", map[string]any{
				"server": name,
			})
			continue
		}

		if err := cfg.Validate(); err != nil {
			logger.WarnCF("mcp", "Invalid MCP server config", map[string]any{
				"server": name,
				"error":  err.Error(),
			})
			lastErr = err
			continue
		}

		conn, err := m.connectServer(ctx, name, cfg)
		if err != nil {
			logger.WarnCF("mcp", "Failed to connect to MCP server", map[string]any{
				"server": name,
				"error":  err.Error(),
			})
			lastErr = err
			continue
		}

		m.mu.Lock()
		m.connections[name] = conn
		m.mu.Unlock()
		successCount++
	}

	if successCount == 0 && lastErr != nil {
		return m, fmt.Errorf("all MCP servers failed to connect: %w", lastErr)
	}

	return m, nil
}

func (m *Manager) connectServer(ctx context.Context, name string, cfg config.MCPServerConfig) (*ServerConnection, error) {
	connCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var c *mcpclient.Client
	var err error

	switch cfg.Transport {
	case "stdio":
		c, err = mcpclient.NewStdioMCPClient(cfg.Command, cfg.Env, cfg.Args...)
	case "sse":
		c, err = mcpclient.NewSSEMCPClient(cfg.URL, mcpclient.WithHeaders(cfg.Headers))
	default:
		return nil, fmt.Errorf("unsupported transport %q (use stdio or sse)", cfg.Transport)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client for %q: %w", name, err)
	}

	if err := c.Start(connCtx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("failed to start MCP client for %q: %w", name, err)
	}

	initResult, err := initializeClient(connCtx, c)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("failed to initialize MCP server %q: %w", name, err)
	}

	logger.InfoCF("mcp", "Connected to MCP server", map[string]any{
		"server":  name,
		"name":    initResult.ServerInfo.Name,
		"version": initResult.ServerInfo.Version,
	})

	tools, err := discoverTools(connCtx, c)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("failed to discover tools from %q: %w", name, err)
	}

	logger.InfoCF("mcp", "Discovered MCP tools", map[string]any{
		"server":     name,
		"tool_count": len(tools),
	})

	return &ServerConnection{
		Name:      name,
		Client:    c,
		Tools:     tools,
		Config:    cfg,
		connected: true,
	}, nil
}

func initializeClient(ctx context.Context, c *mcpclient.Client) (*mcpgo.InitializeResult, error) {
	req := mcpgo.InitializeRequest{}
	req.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcpgo.Implementation{
		Name:    "picoclaw",
		Version: "1.0.0",
	}
	req.Params.Capabilities = mcpgo.ClientCapabilities{}

	return c.Initialize(ctx, req)
}

func discoverTools(ctx context.Context, c *mcpclient.Client) ([]mcpgo.Tool, error) {
	result, err := c.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func (m *Manager) GetConnection(name string) (*ServerConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conn, ok := m.connections[name]
	return conn, ok
}

func (m *Manager) GetTools() map[string][]mcpgo.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]mcpgo.Tool, len(m.connections))
	for name, conn := range m.connections {
		conn.mu.RLock()
		if conn.connected {
			result[name] = conn.Tools
		}
		conn.mu.RUnlock()
	}
	return result
}

func (m *Manager) GetToolsForServers(serverNames []string) map[string][]mcpgo.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]mcpgo.Tool)
	for _, name := range serverNames {
		conn, ok := m.connections[name]
		if !ok {
			logger.WarnCF("mcp", "Server not found in GetToolsForServers", map[string]any{
				"server": name,
			})
			continue
		}
		conn.mu.RLock()
		if conn.connected {
			result[name] = conn.Tools
		}
		conn.mu.RUnlock()
	}
	return result
}

func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*mcpgo.CallToolResult, error) {
	m.mu.RLock()
	conn, ok := m.connections[serverName]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("MCP server %q not found", serverName)
	}

	conn.mu.RLock()
	connected := conn.connected
	conn.mu.RUnlock()

	if !connected {
		return nil, fmt.Errorf("MCP server %q is not connected", serverName)
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	return conn.Client.CallTool(ctx, req)
}

func (m *Manager) Reconnect(ctx context.Context, serverName string) error {
	m.mu.RLock()
	conn, ok := m.connections[serverName]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("unknown MCP server: %s", serverName)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()

	if conn.Client != nil {
		_ = conn.Client.Close()
	}
	conn.connected = false

	maxRetries := conn.Config.MaxRetries
	if maxRetries == 0 {
		maxRetries = 3
	}

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			backoff := time.Duration(1<<uint(i)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		logger.InfoCF("mcp", "Attempting to reconnect MCP server", map[string]any{
			"server":  serverName,
			"attempt": i + 1,
		})

		newConn, err := m.connectServer(ctx, serverName, conn.Config)
		if err != nil {
			lastErr = err
			logger.WarnCF("mcp", "Reconnect attempt failed", map[string]any{
				"server":  serverName,
				"attempt": i + 1,
				"error":   err.Error(),
			})
			continue
		}

		conn.Client = newConn.Client
		conn.Tools = newConn.Tools
		conn.connected = true
		conn.lastError = nil
		logger.InfoCF("mcp", "Successfully reconnected MCP server", map[string]any{
			"server": serverName,
		})
		return nil
	}

	conn.lastError = lastErr
	return fmt.Errorf("reconnection failed after %d attempts: %w", maxRetries, lastErr)
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var lastErr error
	for name, conn := range m.connections {
		conn.mu.Lock()
		if conn.Client != nil {
			if err := conn.Client.Close(); err != nil {
				logger.WarnCF("mcp", "Error closing MCP client", map[string]any{
					"server": name,
					"error":  err.Error(),
				})
				lastErr = err
			}
			conn.connected = false
		}
		conn.mu.Unlock()
		logger.InfoCF("mcp", "Closed MCP server connection", map[string]any{
			"server": name,
		})
	}

	return lastErr
}
