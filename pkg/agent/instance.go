package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AgentInstance represents a fully configured agent with its own workspace,
// session manager, context builder, and tool registry.
type AgentInstance struct {
	ID                  string
	Name                string
	Model               string
	Fallbacks           []string
	Workspace           string
	MaxIterations       int
	MaxToolCallsPerIter int
	MaxTokens           int
	Temperature         float64
	ContextWindow       int
	ThinkingBudget      int
	ThinkingLevel       string
	Provider            providers.LLMProvider
	ProvidersByName     map[string]providers.LLMProvider // provider-name → provider, for fallback routing
	Sessions            *session.SessionManager
	ContextBuilder      *ContextBuilder
	Tools               *tools.ToolRegistry
	Subagents           *config.SubagentsConfig
	SkillsFilter        []string
	Candidates          []providers.FallbackCandidate
	MCPManager          *mcp.Manager
}

// NewAgentInstance creates an agent instance from config.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	return newAgentInstanceWithMCPManager(agentCfg, defaults, cfg, provider, nil)
}

func newAgentInstanceWithMCPManager(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
	sharedMCPManager *mcp.Manager,
) *AgentInstance {
	workspace := resolveAgentWorkspace(agentCfg, defaults)
	os.MkdirAll(workspace, 0o755)

	model := resolveAgentModel(agentCfg, defaults)
	fallbacks := resolveAgentFallbacks(agentCfg, defaults)

	restrict := defaults.RestrictToWorkspace
	toolsRegistry := tools.NewToolRegistry()
	execSessionManager := tools.NewSessionManager()
	toolsRegistry.Register(tools.NewReadFileTool(workspace, restrict))
	toolsRegistry.Register(tools.NewWriteFileTool(workspace, restrict))
	toolsRegistry.Register(tools.NewListDirTool(workspace, restrict))
	toolsRegistry.Register(tools.NewExecToolWithConfig(workspace, restrict, cfg))
	toolsRegistry.Register(tools.NewExecSessionTool(execSessionManager))
	toolsRegistry.Register(tools.NewEditFileTool(workspace, restrict))
	toolsRegistry.Register(tools.NewAppendFileTool(workspace, restrict))
	toolsRegistry.Register(tools.NewRecallMemoryTool(workspace))

	// MCP tool initialization — skipped silently if no servers are configured
	var mcpManager *mcp.Manager
	if len(cfg.Tools.MCP.Servers) > 0 {
		if sharedMCPManager != nil {
			mcpManager = sharedMCPManager
		} else {
			mcpCtx := context.Background()
			mcpMgr, err := mcp.NewManager(mcpCtx, cfg.Tools.MCP)
			if err != nil {
				logger.WarnCF("agent", "MCP manager initialization failed", map[string]any{
					"error": err.Error(),
				})
			}
			mcpManager = mcpMgr
		}

		if mcpManager != nil {
			var agentMCPServers []string
			if agentCfg != nil {
				agentMCPServers = agentCfg.MCPServers
			}
			count := mcp.RegisterMCPTools(mcpManager, toolsRegistry, agentMCPServers)
			logger.InfoCF("agent", "Registered MCP tools", map[string]any{
				"count": count,
			})
		}
	}

	sessionsDir := filepath.Join(workspace, "sessions")
	sessionsManager := session.NewSessionManager(sessionsDir)

	contextBuilder := NewContextBuilder(workspace)
	contextBuilder.SetToolsRegistry(toolsRegistry)

	// Skills v2: create trigger engine with agent's skill config
	triggerConfig := skills.TriggerConfig{
		Enabled:       true,
		AutoInject:    true,
		MaxAutoInject: 3,
	}
	if agentCfg != nil {
		triggerConfig.Enabled = agentCfg.SkillsConfig.Enabled
		triggerConfig.AutoInject = agentCfg.SkillsConfig.AutoInject
		triggerConfig.MaxAutoInject = agentCfg.SkillsConfig.MaxAutoInject
		triggerConfig.AllowList = agentCfg.SkillsConfig.AllowList
		triggerConfig.BlockList = agentCfg.SkillsConfig.BlockList
		triggerConfig.TokenBudget = agentCfg.SkillsConfig.TokenBudget
	}
	if triggerConfig.MaxAutoInject == 0 {
		triggerConfig.MaxAutoInject = 3
	}
	triggerEngine := skills.NewTriggerEngine(contextBuilder.GetSkillsLoader(), triggerConfig)
	contextBuilder.SetTriggerEngine(triggerEngine)

	agentID := routing.DefaultAgentID
	agentName := ""
	var subagents *config.SubagentsConfig
	var skillsFilter []string

	if agentCfg != nil {
		agentID = routing.NormalizeAgentID(agentCfg.ID)
		agentName = agentCfg.Name
		subagents = agentCfg.Subagents
		skillsFilter = agentCfg.Skills
	}

	maxIter := defaults.MaxToolIterations
	if maxIter == 0 {
		maxIter = 20
	}
	if agentCfg != nil && agentCfg.MaxToolIterations > 0 {
		maxIter = agentCfg.MaxToolIterations
	}

	maxTokens := defaults.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}
	if agentCfg != nil && agentCfg.MaxTokens > 0 {
		maxTokens = agentCfg.MaxTokens
	}

	temperature := 0.7
	if defaults.Temperature != nil {
		temperature = *defaults.Temperature
	}
	if agentCfg != nil && agentCfg.Temperature != nil {
		temperature = *agentCfg.Temperature
	}

	contextWindow := maxTokens
	if agentCfg != nil && agentCfg.ContextWindow > 0 {
		contextWindow = agentCfg.ContextWindow
	}

	thinkingBudget := defaults.ThinkingBudget
	if agentCfg != nil && agentCfg.ThinkingBudget > 0 {
		thinkingBudget = agentCfg.ThinkingBudget
	}

	thinkingLevel := ""

	// Build candidates using the actual API model strings from model_list, not the model_name shorthands.
	// This ensures OpenRouter gets "google/gemini-3.1-pro-preview" not just "gemini-3.1-pro-preview".
	// Provider routing is keyed by model config identity, not just protocol, because multiple
	// OpenAI-compatible providers can share the "openai" prefix while using different API bases.
	providersByName := make(map[string]providers.LLMProvider)
	var candidates []providers.FallbackCandidate

	// Primary candidate
	if mc, err := cfg.GetModelConfig(model); err == nil && mc != nil {
		thinkingLevel = mc.ThinkingLevel
		ref := providers.ParseModelRef(mc.Model, defaults.Provider)
		if ref != nil {
			providerKey := fallbackProviderKey(ref, mc.ModelName)
			candidates = append(candidates, providers.FallbackCandidate{Provider: providerKey, Model: ref.Model, IdentityKey: providerKey})
			providersByName[providerKey] = provider
		}
	}
	if len(candidates) == 0 {
		// Fallback: parse model_name directly
		ref := providers.ParseModelRef(model, defaults.Provider)
		if ref != nil {
			providerKey := fallbackProviderKey(ref, "")
			candidates = append(candidates, providers.FallbackCandidate{Provider: providerKey, Model: ref.Model, IdentityKey: providerKey})
			providersByName[providerKey] = provider
		} else {
			providersByName[defaults.Provider] = provider
		}
	}

	// Fallback candidates — look up each fallback model in model_list to get the real API model string.
	for _, fb := range fallbacks {
		mc, err := cfg.GetModelConfig(fb)
		if err != nil || mc == nil {
			// Try parsing as a raw model ref
			ref := providers.ParseModelRef(fb, defaults.Provider)
			if ref == nil {
				continue
			}
			providerKey := fallbackProviderKey(ref, "")
			if _, exists := providersByName[providerKey]; !exists {
				providersByName[providerKey] = provider
			}
			candidates = append(candidates, providers.FallbackCandidate{Provider: providerKey, Model: ref.Model, IdentityKey: providerKey})
			continue
		}
		ref := providers.ParseModelRef(mc.Model, defaults.Provider)
		if ref == nil {
			continue
		}
		providerKey := fallbackProviderKey(ref, mc.ModelName)
		if _, exists := providersByName[providerKey]; !exists {
			fbProvider, _, fbErr := providers.CreateProviderFromConfig(mc)
			if fbErr == nil {
				providersByName[providerKey] = fbProvider
			}
		}
		// Only add if not already in candidates
		alreadyAdded := false
		for _, c := range candidates {
			if c.Provider == providerKey && c.Model == ref.Model {
				alreadyAdded = true
				break
			}
		}
		if !alreadyAdded {
			candidates = append(candidates, providers.FallbackCandidate{Provider: providerKey, Model: ref.Model, IdentityKey: providerKey})
		}
	}

	return &AgentInstance{
		ID:              agentID,
		Name:            agentName,
		Model:           model,
		Fallbacks:       fallbacks,
		Workspace:       workspace,
		MaxIterations:   maxIter,
		MaxTokens:       maxTokens,
		Temperature:     temperature,
		ContextWindow:   contextWindow,
		ThinkingBudget:  thinkingBudget,
		ThinkingLevel:   thinkingLevel,
		Provider:        provider,
		ProvidersByName: providersByName,
		Sessions:        sessionsManager,
		ContextBuilder:  contextBuilder,
		Tools:           toolsRegistry,
		Subagents:       subagents,
		SkillsFilter:    skillsFilter,
		Candidates:      candidates,
		MCPManager:      mcpManager,
	}
}

func fallbackProviderKey(ref *providers.ModelRef, modelName string) string {
	if ref == nil {
		return ""
	}
	if modelName != "" {
		return providers.ModelKey(ref.Provider, modelName)
	}
	return providers.ModelKey(ref.Provider, ref.Model)
}

// resolveAgentWorkspace determines the workspace directory for an agent.
func resolveAgentWorkspace(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && strings.TrimSpace(agentCfg.Workspace) != "" {
		return expandHome(strings.TrimSpace(agentCfg.Workspace))
	}
	if agentCfg == nil || agentCfg.Default || agentCfg.ID == "" || routing.NormalizeAgentID(agentCfg.ID) == "main" {
		return expandHome(defaults.Workspace)
	}
	home, _ := os.UserHomeDir()
	id := routing.NormalizeAgentID(agentCfg.ID)
	return filepath.Join(home, ".picoclaw", "workspace-"+id)
}

// resolveAgentModel resolves the primary model for an agent.
func resolveAgentModel(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && agentCfg.Model != nil && strings.TrimSpace(agentCfg.Model.Primary) != "" {
		return strings.TrimSpace(agentCfg.Model.Primary)
	}
	return defaults.GetModelName()
}

// resolveAgentFallbacks resolves the fallback models for an agent.
func resolveAgentFallbacks(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) []string {
	if agentCfg != nil && agentCfg.Model != nil && agentCfg.Model.Fallbacks != nil {
		return agentCfg.Model.Fallbacks
	}
	return defaults.ModelFallbacks
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
}
