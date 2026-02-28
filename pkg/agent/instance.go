package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AgentInstance represents a fully configured agent with its own workspace,
// session manager, context builder, and tool registry.
type AgentInstance struct {
	ID              string
	Name            string
	Model           string
	Fallbacks       []string
	Workspace       string
	MaxIterations   int
	MaxTokens       int
	Temperature     float64
	ContextWindow   int
	ThinkingBudget  int
	Provider        providers.LLMProvider
	ProvidersByName map[string]providers.LLMProvider // provider-name → provider, for fallback routing
	Sessions        *session.SessionManager
	ContextBuilder  *ContextBuilder
	Tools           *tools.ToolRegistry
	Subagents       *config.SubagentsConfig
	SkillsFilter    []string
	Candidates      []providers.FallbackCandidate
}

// NewAgentInstance creates an agent instance from config.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	workspace := resolveAgentWorkspace(agentCfg, defaults)
	os.MkdirAll(workspace, 0o755)

	model := resolveAgentModel(agentCfg, defaults)
	fallbacks := resolveAgentFallbacks(agentCfg, defaults)

	restrict := defaults.RestrictToWorkspace
	toolsRegistry := tools.NewToolRegistry()
	toolsRegistry.Register(tools.NewReadFileTool(workspace, restrict))
	toolsRegistry.Register(tools.NewWriteFileTool(workspace, restrict))
	toolsRegistry.Register(tools.NewListDirTool(workspace, restrict))
	toolsRegistry.Register(tools.NewExecToolWithConfig(workspace, restrict, cfg))
	toolsRegistry.Register(tools.NewEditFileTool(workspace, restrict))
	toolsRegistry.Register(tools.NewAppendFileTool(workspace, restrict))

	sessionsDir := filepath.Join(workspace, "sessions")
	sessionsManager := session.NewSessionManager(sessionsDir)

	contextBuilder := NewContextBuilder(workspace)
	contextBuilder.SetToolsRegistry(toolsRegistry)

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

	thinkingBudget := 0
	if agentCfg != nil && agentCfg.ThinkingBudget > 0 {
		thinkingBudget = agentCfg.ThinkingBudget
	}

	// Build candidates using the actual API model strings from model_list, not the model_name shorthands.
	// This ensures OpenRouter gets "google/gemini-3.1-pro-preview" not just "gemini-3.1-pro-preview".
	providersByName := make(map[string]providers.LLMProvider)
	var candidates []providers.FallbackCandidate

	// Primary candidate
	if mc, err := cfg.GetModelConfig(model); err == nil && mc != nil {
		ref := providers.ParseModelRef(mc.Model, defaults.Provider)
		if ref != nil {
			candidates = append(candidates, providers.FallbackCandidate{Provider: ref.Provider, Model: ref.Model})
			providersByName[ref.Provider] = provider
		}
	}
	if len(candidates) == 0 {
		// Fallback: parse model_name directly
		ref := providers.ParseModelRef(model, defaults.Provider)
		if ref != nil {
			candidates = append(candidates, providers.FallbackCandidate{Provider: ref.Provider, Model: ref.Model})
			providersByName[ref.Provider] = provider
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
			if _, exists := providersByName[ref.Provider]; !exists {
				providersByName[ref.Provider] = provider
			}
			candidates = append(candidates, providers.FallbackCandidate{Provider: ref.Provider, Model: ref.Model})
			continue
		}
		ref := providers.ParseModelRef(mc.Model, defaults.Provider)
		if ref == nil {
			continue
		}
		if _, exists := providersByName[ref.Provider]; !exists {
			fbProvider, _, fbErr := providers.CreateProviderFromConfig(mc)
			if fbErr == nil {
				providersByName[ref.Provider] = fbProvider
			}
		}
		// Only add if not already in candidates
		alreadyAdded := false
		for _, c := range candidates {
			if c.Provider == ref.Provider && c.Model == ref.Model {
				alreadyAdded = true
				break
			}
		}
		if !alreadyAdded {
			candidates = append(candidates, providers.FallbackCandidate{Provider: ref.Provider, Model: ref.Model})
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
		Provider:        provider,
		ProvidersByName: providersByName,
		Sessions:        sessionsManager,
		ContextBuilder:  contextBuilder,
		Tools:           toolsRegistry,
		Subagents:       subagents,
		SkillsFilter:    skillsFilter,
		Candidates:      candidates,
	}
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
