# ResystBot/PicoClaw — Codebase Structure & MCP Configuration Report

## 1. Project Structure

```
~/Documentos/projects/ResystBot/
├── cmd/
│   └── picoclaw/              # CLI entry points
│       ├── main.go            # Binary entry point
│       ├── cmd_agent.go       # `picoclaw agent` — one-shot/interactive chat
│       ├── cmd_gateway.go     # `picoclaw gateway` — daemon mode (Telegram, Discord, etc.)
│       ├── cmd_onboard.go     # `picoclaw onboard` — initial setup wizard
│       ├── cmd_auth.go        # `picoclaw auth` — OAuth login for providers
│       ├── cmd_cron.go        # Scheduled tasks management
│       ├── cmd_status.go      # Status display
│       ├── cmd_memory.go      # Memory system management
│       ├── cmd_migrate.go     # Config migration
│       ├── cmd_consolidate.go # Memory consolidation
│       ├── cmd_skills.go      # Skills management
│       ├── daemon.go          # Daemon lifecycle (start/stop/restart)
│       └── workspace/         # Workspace template files
├── pkg/                       # Core packages
│   ├── agent/                 # Agent orchestration (loop, registry, instance)
│   ├── mcp/                   # MCP client implementation
│   ├── config/                # Configuration loading & validation
│   ├── providers/             # LLM provider integrations (OpenAI, Anthropic, etc.)
│   ├── tools/                 # Built-in tool implementations
│   ├── channels/              # Chat channel integrations (Telegram, Discord, QQ, etc.)
│   ├── session/               # Session history management
│   ├── memory/                # Hippocampal memory system (Qdrant + embeddings)
│   ├── hooks/                 # Hook system (PreToolUse, PostToolUse, etc.)
│   ├── routing/               # Agent routing/binding
│   ├── bus/                   # Internal message bus (inbound/outbound)
│   ├── skills/                # Skill discovery & installation
│   ├── cron/                  # Cron/scheduled tasks
│   ├── logger/                # Structured logging
│   ├── state/                 # Persistent state management
│   ├── health/                # Health checks
│   ├── heartbeat/             # Periodic heartbeat tasks
│   ├── auth/                  # Authentication helpers
│   ├── constants/             # Shared constants
│   ├── devices/               # Hardware device support (I2C, SPI)
│   ├── migrate/               # Config migration
│   ├── utils/                 # Utility functions
│   └── voice/                 # Voice transcription (Groq Whisper)
├── config/
│   └── config.example.json    # Example configuration template
├── go.mod                     # Module: github.com/sipeed/picoclaw
├── Makefile                   # Build targets (build, test, lint)
├── Dockerfile                 # Container build
├── docker-compose.yml         # Docker Compose setup
└── docs/                      # Documentation
```

### Architectural Patterns

- **Module**: `github.com/sipeed/picoclaw` — Go 1.25.7
- **Key dependency**: `github.com/mark3labs/mcp-go v0.44.1` — MCP client library
- **Pattern**: Registry-based tool system with `Tool` interface (`Name()`, `Description()`, `Parameters()`, `Execute()`)
- **Pattern**: Message bus (`bus.MessageBus`) for inbound/outbound message routing
- **Pattern**: Multi-agent with `AgentRegistry` — each agent has its own tool registry, session manager, and MCP manager
- **Pattern**: Fallback chain for LLM providers — primary model + fallbacks with cooldown tracking

## 2. MCP Configuration

### `~/.picoclaw/config.json` — MCP Section

Located at `tools.mcp.servers`:

```json
{
  "tools": {
    "mcp": {
      "servers": {
        "<server-name>": {
          "transport": "stdio|sse|http",
          "command": "...",        // Required for stdio
          "args": ["..."],         // Optional, for stdio
          "env": {"KEY": "VAL"},   // Optional, for stdio
          "url": "...",            // Required for sse/http
          "headers": {"K": "V"},   // Optional, for sse/http
          "enabled": true|false,   // Optional, defaults to true
          "timeout": 30,           // Optional, seconds
          "max_retries": 3,        // Optional, for reconnection
          "oauth": {               // Optional, for http transport
            "enabled": true,
            "client_id": "...",
            "client_secret": "...",
            "redirect_uri": "...",
            "callback_port": 9876,
            "scopes": "..."
          }
        }
      }
    }
  }
}
```

### `~/.picoclaw/engine.json`

Minimal — just selects the active engine profile:

```json
{
  "active": "picoclaw",
  "claude": { "model": "opus", "effort": "max" }
}
```

This is used for the Claude Code delegation tool, not MCP configuration.

### Per-Agent MCP Filtering

Each agent in `agents.list` can have an optional `mcp_servers` array:

```json
{
  "id": "main",
  "mcp_servers": ["teamforge", "gitlab"]
}
```

If empty/omitted, the agent gets ALL enabled MCP servers.

## 3. How MCPs Are Loaded in Go Code

### Initialization Flow

```
main.go
  → cmd_agent.go / cmd_gateway.go
    → agent.NewAgentRegistry(cfg, provider)
      → agent.NewAgentInstance(agentCfg, defaults, cfg, provider)
        → mcp.NewManager(ctx, cfg.Tools.MCP)           // Create MCP manager
        → mcp.RegisterMCPTools(mgr, toolsRegistry, agentMCPServers)  // Bridge tools
```

### Key Files

| File | Purpose |
|------|---------|
| `pkg/mcp/manager.go` | MCP Manager — connects to servers, discovers tools, calls tools |
| `pkg/mcp/bridge.go` | MCPToolBridge — adapts MCP tools to PicoClaw's `Tool` interface |
| `pkg/mcp/oauth_flow.go` | OAuth authorization flow for HTTP transport |
| `pkg/mcp/oauth_callback.go` | Local HTTP callback server for OAuth |
| `pkg/mcp/file_token_store.go` | Persistent OAuth token storage |
| `pkg/config/config.go` | Config structs: `MCPConfig`, `MCPServerConfig`, `MCPOAuthConfig` |

### Manager Lifecycle (`manager.go`)

1. **`NewManager(ctx, MCPConfig)`**: Iterates all servers in config, skips disabled, validates, connects each.
2. **`connectServer(ctx, name, cfg)`**: Creates transport client, starts it, handles OAuth if needed, initializes MCP protocol, discovers tools.
3. **Transport selection**:
   - `stdio` → `mcpclient.NewStdioMCPClient(command, env, args...)` — spawns subprocess
   - `sse` → `mcpclient.NewSSEMCPClient(url, headers)` — Server-Sent Events
   - `http` → `newHTTPClient(cfg, name)` — Streamable HTTP (with optional OAuth)
4. **`initializeClient(ctx, client)`**: Sends MCP `InitializeRequest` with protocol version and client info.
5. **`discoverTools(ctx, client)`**: Calls `ListTools` on the MCP server to get all available tools.
6. **`CallTool(ctx, serverName, toolName, args)`**: Executes a tool call on a specific MCP server.
7. **`Reconnect(ctx, serverName)`**: Reconnects with exponential backoff (default 3 retries).
8. **`Shutdown(ctx)`**: Closes all MCP client connections.

### Bridge System (`bridge.go`)

The `MCPToolBridge` adapts MCP server tools to PicoClaw's `tools.Tool` interface:

- **Naming convention**: `mcp__<server_name>__<tool_name>` (e.g., `mcp__teamforge__send_message`)
- **Description prefix**: `[MCP:<server>] <original description>`
- **Execute flow**:
  1. Check connection status
  2. If disconnected → attempt reconnect
  3. Call `manager.CallTool()` with timeout
  4. On connection error → trigger background reconnect
  5. Convert MCP result to `ToolResult`

### `RegisterMCPTools(manager, registry, serverNames)`

Bridges all (or filtered) MCP tools into a tool registry. Returns count of registered tools.

## 4. Existing MCP Integrations

| Server | Transport | Enabled | Purpose |
|--------|-----------|---------|---------|
| **chrome-devtools** | stdio (`npx chrome-devtools-mcp`) | ❌ Disabled | Browser DevTools integration |
| **playwright** | stdio (`npx @playwright/mcp@latest`) | ❌ Disabled | Browser automation (default transport = stdio) |
| **gitnexus** | stdio (`npx gitnexus@latest mcp`) | ❌ Disabled | Code knowledge graph & analysis |
| **teamforge** | stdio (local binary `teamforge-mcp`) | ✅ Enabled | Team communication hub for AI agents |
| **gitlab** | http (`https://gitlab.com/api/v4/mcp`) | ✅ Enabled | GitLab API integration (OAuth) |
| **atlassian** | http (`https://mcp.atlassian.com/v1/mcp`) | ✅ Enabled | Jira & Confluence integration (OAuth) |

### Currently Active (3 servers)

1. **teamforge**: Local binary at `~/Documentos/projects/teamforge/teamforge-mcp`, connects to hub at `http://localhost:8585`. Used for inter-agent communication between PicoClaw and Claude Code.
2. **gitlab**: HTTP transport with OAuth (client_id `960c07...`, callback port 9876). Used for GitLab project/issue/MR management.
3. **atlassian**: HTTP transport with OAuth (client_id `nWbpTM...`, callback port 9877). Used for Jira issues and Confluence pages.

## 5. Adding a New MCP

### Steps

1. **Add server entry** to `~/.picoclaw/config.json` → `tools.mcp.servers`:

#### For stdio transport (most common):
```json
{
  "tools": {
    "mcp": {
      "servers": {
        "my-new-mcp": {
          "transport": "stdio",
          "command": "npx",
          "args": ["-y", "my-mcp-server@latest"],
          "env": {
            "API_KEY": "your-api-key"
          },
          "timeout": 30,
          "enabled": true
        }
      }
    }
  }
}
```

#### For http/streamable transport:
```json
{
  "my-http-mcp": {
    "transport": "http",
    "url": "https://example.com/mcp",
    "headers": {
      "Authorization": "Bearer token123"
    },
    "timeout": 30,
    "enabled": true
  }
}
```

#### For http with OAuth:
```json
{
  "my-oauth-mcp": {
    "transport": "http",
    "url": "https://example.com/mcp",
    "enabled": true,
    "timeout": 30,
    "oauth": {
      "enabled": true,
      "client_id": "your-client-id",
      "callback_port": 9878,
      "scopes": "read write"
    }
  }
}
```

### Required Fields (Validation Rules from `MCPServerConfig.Validate()`)

| Transport | Required Fields |
|-----------|----------------|
| `stdio` | `command` |
| `sse` | `url` |
| `http` | `url` |
| Any | `transport` (defaults to `"stdio"` if empty) |

### Optional Fields

| Field | Default | Purpose |
|-------|---------|---------|
| `enabled` | `true` | Enable/disable server |
| `timeout` | `0` (60s in bridge) | Tool call timeout in seconds |
| `max_retries` | `3` | Reconnection attempts |
| `args` | `[]` | Arguments for stdio command |
| `env` | `{}` | Environment variables for stdio |
| `headers` | `{}` | HTTP headers for sse/http |
| `oauth` | (none) | OAuth config for http transport |

### Hot-Reload

**No hot-reload.** MCP servers are initialized once at agent startup in `NewAgentInstance()`. To add/remove/change an MCP server:
1. Edit `~/.picoclaw/config.json`
2. Restart PicoClaw (restart the daemon or run `picoclaw agent` again)

The config is read fresh on each startup. There is no file watcher for MCP config changes.

### Per-Agent Filtering

To restrict which MCP servers a specific agent sees, add `mcp_servers` to the agent config:

```json
{
  "agents": {
    "list": [
      {
        "id": "researcher",
        "mcp_servers": ["gitlab", "atlassian"]
      }
    ]
  }
}
```

## 6. Agent-MCP Interaction Flow

```
User sends message (Telegram/Discord/CLI)
    ↓
AgentLoop.processMessage()
    ↓
AgentLoop.runAgentLoop()
    ↓
AgentLoop.runLLMIteration()
    ↓
LLM Provider (OpenAI/Anthropic/etc.) returns tool_calls
    ↓ (tool call: "mcp__gitlab__create_issue")
ToolRegistry.ExecuteWithContext("mcp__gitlab__create_issue", args)
    ↓
HookExecutor.RunPreToolUse() ← hooks can block/redirect
    ↓
MCPToolBridge.Execute(ctx, args)
    ↓
MCPToolBridge checks connection → reconnect if needed
    ↓
Manager.CallTool(ctx, "gitlab", "create_issue", args)
    ↓
mcpclient.Client.CallTool(ctx, CallToolRequest{Name: "create_issue", Arguments: args})
    ↓
[HTTP/Stdio transport to MCP server]
    ↓
MCP server processes request, returns CallToolResult
    ↓
convertMCPResult() → tools.ToolResult
    ↓
ToolRegistry returns result to agent loop
    ↓
Result added to messages as tool role message
    ↓
AgentLoop continues LLM iteration (may call more tools)
    ↓
Final response sent to user via bus
```

### Key Implementation Details

- **Tool naming**: `mcp__<server>__<tool>` — the `__` separator (double underscore) avoids collisions with built-in tools
- **Connection resilience**: If an MCP call fails with a connection error, a background goroutine triggers reconnection. The current call returns an error.
- **Timeout**: Each MCP tool call uses the server's configured `timeout` (in seconds). Default in bridge is 60s.
- **Auto-reconnect**: On connection loss, `Manager.Reconnect()` uses exponential backoff (1s, 2s, 4s...) up to `max_retries` (default 3).
- **OAuth token persistence**: Tokens stored in `~/.picoclaw/` via `FileTokenStore`. Auto-refreshes expired tokens using refresh tokens.
- **Multiple agents**: Each `AgentInstance` gets its own `mcp.Manager` instance. Tools are registered per-agent, filtered by `mcp_servers` config.
- **Tool discovery**: Tools are discovered once at startup via `ListTools` MCP call. No dynamic tool refresh during runtime.
