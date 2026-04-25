package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
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
		envSlice := make([]string, 0, len(cfg.Env))
		for k, v := range cfg.Env {
			envSlice = append(envSlice, k+"="+v)
		}
		c, err = mcpclient.NewStdioMCPClient(cfg.Command, envSlice, cfg.Args...)
	case "sse":
		c, err = mcpclient.NewSSEMCPClient(cfg.URL, mcpclient.WithHeaders(cfg.Headers))
	case "http":
		c, err = newHTTPClient(cfg, name)
	default:
		return nil, fmt.Errorf("unsupported transport %q (use stdio, sse, or http)", cfg.Transport)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client for %q: %w", name, err)
	}

	if err := c.Start(connCtx); err != nil {
		if handler, ok := extractOAuthHandler(err); ok && cfg.OAuth.Enabled {
			_ = c.Close()
			logger.InfoCF("mcp-oauth", "Server requires OAuth authorization", map[string]any{
				"server": name,
			})

			if oauthErr := authorizeWithHandler(connCtx, cfg, name, handler); oauthErr != nil {
				return nil, fmt.Errorf("OAuth authorization failed for %q: %w", name, oauthErr)
			}

			c, err = newHTTPClient(cfg, name)
			if err != nil {
				return nil, fmt.Errorf("failed to recreate MCP client for %q: %w", name, err)
			}
			if err := c.Start(connCtx); err != nil {
				_ = c.Close()
				return nil, fmt.Errorf("failed to start MCP client after OAuth for %q: %w", name, err)
			}
		} else {
			_ = c.Close()
			return nil, fmt.Errorf("failed to start MCP client for %q: %w", name, err)
		}
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

// newHTTPClient creates a Streamable HTTP MCP client.
// When OAuth is enabled, it uses a persistent FileTokenStore and handles
// the OAuth authorization flow (including PKCE) before starting the client.
func newHTTPClient(cfg config.MCPServerConfig, serverName string) (*mcpclient.Client, error) {
	opts := []mcptransport.StreamableHTTPCOption{}

	if len(cfg.Headers) > 0 {
		opts = append(opts, mcptransport.WithHTTPHeaders(cfg.Headers))
	}

	if !cfg.OAuth.Enabled {
		transport, err := mcptransport.NewStreamableHTTP(cfg.URL, opts...)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP transport: %w", err)
		}
		return mcpclient.NewClient(transport), nil
	}

	tokenStore, err := NewFileTokenStore(serverName)
	if err != nil {
		return nil, fmt.Errorf("cannot create token store for %q: %w", serverName, err)
	}

	redirectURI := cfg.OAuth.RedirectURI
	if redirectURI == "" {
		port := cfg.OAuth.CallbackPort
		if port <= 0 {
			port = 9876
		}
		redirectURI = fmt.Sprintf("http://localhost:%d/callback", port)
	}

	oauthConfig := mcpclient.OAuthConfig{
		ClientID:     cfg.OAuth.ClientID,
		ClientSecret: cfg.OAuth.ClientSecret,
		RedirectURI:  redirectURI,
		PKCEEnabled:  true,
		TokenStore:   tokenStore,
	}

	if cfg.OAuth.Scopes != "" {
		oauthConfig.Scopes = strings.Fields(cfg.OAuth.Scopes)
	}

	client, err := mcpclient.NewOAuthStreamableHttpClient(cfg.URL, oauthConfig, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth HTTP client: %w", err)
	}

	return client, nil
}

func authorizeWithHandler(ctx context.Context, cfg config.MCPServerConfig, serverName string, handler *mcptransport.OAuthHandler) error {
	if _, err := handler.GetServerMetadata(ctx); err != nil {
		logger.WarnCF("mcp-oauth", "Server metadata discovery failed", map[string]any{
			"server": serverName,
			"error":  err.Error(),
		})
	}

	if handler.GetClientID() == "" {
		logger.InfoCF("mcp-oauth", "Attempting dynamic client registration", map[string]any{
			"server": serverName,
		})
		if err := handler.RegisterClient(ctx, "picoclaw"); err != nil {
			logger.WarnCF("mcp-oauth", "Dynamic registration failed", map[string]any{
				"server": serverName,
				"error":  err.Error(),
			})
		}
	}

	codeVerifier, err := mcptransport.GenerateCodeVerifier()
	if err != nil {
		return fmt.Errorf("cannot generate code verifier: %w", err)
	}
	codeChallenge := mcptransport.GenerateCodeChallenge(codeVerifier)
	state, err := mcptransport.GenerateState()
	if err != nil {
		return fmt.Errorf("cannot generate state: %w", err)
	}
	handler.SetExpectedState(state)

	authURL, err := handler.GetAuthorizationURL(ctx, state, codeChallenge)
	if err != nil {
		return fmt.Errorf("cannot build authorization URL: %w", err)
	}

	cbServer := NewCallbackServer(cfg.OAuth.CallbackPort)
	if err := cbServer.Start(); err != nil {
		return fmt.Errorf("cannot start callback server: %w", err)
	}
	defer cbServer.Close()

	logger.InfoCF("mcp-oauth", "Open this URL to authorize", map[string]any{
		"server": serverName,
		"url":    authURL,
	})

	result, err := cbServer.WaitForCallback(ctx)
	if err != nil {
		return fmt.Errorf("OAuth callback failed: %w", err)
	}

	if err := handler.ProcessAuthorizationResponse(ctx, result.Code, result.State, codeVerifier); err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	logger.InfoCF("mcp-oauth", "OAuth authorization successful", map[string]any{
		"server": serverName,
	})

	return nil
}
