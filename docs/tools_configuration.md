# Tools Configuration

PicoClaw's tools configuration is located in the `tools` field of `config.json`.

## Directory Structure

```json
{
  "tools": {
    "web": { ... },
    "exec": { ... },
    "cron": { ... },
    "skills": { ... }
  }
}
```

## Web Tools

Web tools are used for web search and fetching.

### Brave

| Config | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | false | Enable Brave search |
| `api_key` | string | - | Brave Search API key |
| `max_results` | int | 5 | Maximum number of results |

### DuckDuckGo

| Config | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | true | Enable DuckDuckGo search |
| `max_results` | int | 5 | Maximum number of results |

### Perplexity

| Config | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | false | Enable Perplexity search |
| `api_key` | string | - | Perplexity API key |
| `max_results` | int | 5 | Maximum number of results |

## Exec Tool

The exec tool is used to execute shell commands.

| Config | Type | Default | Description |
|--------|------|---------|-------------|
| `enable_deny_patterns` | bool | true | Enable default dangerous command blocking |
| `custom_deny_patterns` | array | [] | Custom deny patterns (regular expressions) |

### Functionality

- **`enable_deny_patterns`**: Set to `false` to completely disable the default dangerous command blocking patterns
- **`custom_deny_patterns`**: Add custom deny regex patterns; commands matching these will be blocked

### Default Blocked Command Patterns

By default, PicoClaw blocks the following dangerous commands:

- Delete commands: `rm -rf`, `del /f/q`, `rmdir /s`
- Disk operations: `format`, `mkfs`, `diskpart`, `dd if=`, writing to `/dev/sd*`
- System operations: `shutdown`, `reboot`, `poweroff`
- Command substitution: `$()`, `${}`, backticks
- Pipe to shell: `| sh`, `| bash`
- Privilege escalation: `sudo`, `chmod`, `chown`
- Process control: `pkill`, `killall`, `kill -9`
- Remote operations: `curl | sh`, `wget | sh`, `ssh`
- Package management: `apt`, `yum`, `dnf`, `npm install -g`, `pip install --user`
- Containers: `docker run`, `docker exec`
- Git: `git push`, `git force`
- Other: `eval`, `source *.sh`

### Configuration Example

```json
{
  "tools": {
    "exec": {
      "enable_deny_patterns": true,
      "custom_deny_patterns": [
        "\\brm\\s+-r\\b",
        "\\bkillall\\s+python"
      ]
    }
  }
}
```

## Cron Tool

The cron tool is used for scheduling periodic tasks.

| Config | Type | Default | Description |
|--------|------|---------|-------------|
| `exec_timeout_minutes` | int | 5 | Execution timeout in minutes, 0 means no limit |

## Skills Tool

The skills tool configures skill discovery and installation via registries like ClawHub.

### Registries

| Config | Type | Default | Description |
|--------|------|---------|-------------|
| `registries.clawhub.enabled` | bool | true | Enable ClawHub registry |
| `registries.clawhub.base_url` | string | `https://clawhub.ai` | ClawHub base URL |
| `registries.clawhub.search_path` | string | `/api/v1/search` | Search API path |
| `registries.clawhub.skills_path` | string | `/api/v1/skills` | Skills API path |
| `registries.clawhub.download_path` | string | `/api/v1/download` | Download API path |

### Configuration Example

```json
{
  "tools": {
    "skills": {
      "registries": {
        "clawhub": {
          "enabled": true,
          "base_url": "https://clawhub.ai",
          "search_path": "/api/v1/search",
          "skills_path": "/api/v1/skills",
          "download_path": "/api/v1/download"
        }
      }
    }
  }
}
```

## MCP Tools

MCP servers are declared under `tools.mcp.servers.<name>` in `config.json`. Each bridged
tool is registered as `mcp__<server>__<tool>`.

### Large text results as artifacts

`tools.mcp.max_inline_text_chars` controls how much text from an MCP tool result is kept
inline in model context. The threshold is counted in Unicode characters (Go runes), not
bytes — `16384` means up to 16,384 characters, which may occupy more than 16 KB for
multibyte text such as CJK.

Above this threshold, the text result is saved as a local artifact under
`<workspace>/.artifacts/mcp/` and the model receives a short note plus the artifact path
instead of the full payload. If the artifact cannot be written, the oversized text is
still omitted from context so a single large result cannot blow the context window.

Non-text content (images, audio, resource links) is never inlined as a payload; the model
sees a short placeholder such as `[non-text content: image (image/png)]`.

```json
{
  "tools": {
    "mcp": {
      "max_inline_text_chars": 16384,
      "servers": {}
    }
  }
}
```

## Delegation Tools

Three tools move work to another agent. They differ on two axes — blocking vs
background, and generic vs named-target:

| Tool | Blocking | Target | Notes |
|------|----------|--------|-------|
| `spawn` | no (async) | optional `agent_id` | Fire-and-forget; the result arrives later as a system message. |
| `subagent` | yes | none | Runs synchronously with the *current* agent's profile. |
| `delegate` | yes | required `agent_id` | Runs synchronously with the *target* agent's own workspace, model, and tools. |

`delegate(agent_id, task)` hands the task to a named agent and waits for the
answer, which is returned in the same turn and attributed as
`[Response from agent "<agent_id>"]`.

Both `spawn` and `delegate` are gated by `subagents.allow_agents` on the calling
agent. The list accepts exact agent IDs or `"*"` for any:

```json
{
  "agents": {
    "list": [
      {
        "id": "main",
        "subagents": {
          "allow_agents": ["specialist", "researcher"]
        }
      }
    ]
  }
}
```

Self-delegation is always rejected. Delegation cycles (A → B → A → …) are
bounded by a per-agent depth guard (default 3 re-entries) so a cycle cannot
recurse without limit.

## Environment Variables

All configuration options can be overridden via environment variables with the format `PICOCLAW_TOOLS_<SECTION>_<KEY>`:

For example:
- `PICOCLAW_TOOLS_WEB_BRAVE_ENABLED=true`
- `PICOCLAW_TOOLS_EXEC_ENABLE_DENY_PATTERNS=false`
- `PICOCLAW_TOOLS_CRON_EXEC_TIMEOUT_MINUTES=10`
- `PICOCLAW_TOOLS_MCP_MAX_INLINE_TEXT_CHARS=16384`

Note: Array-type environment variables are not currently supported and must be set via the config file.
