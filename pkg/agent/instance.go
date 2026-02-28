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

	// Resolve fallback candidates
	modelCfg := providers.ModelConfig{
		Primary:   model,
		Fallbacks: fallbacks,
	}
	candidates := providers.ResolveCandidates(modelCfg, defaults.Provider)

	// Build per-provider map for fallback routing.
	// primary provider is keyed by its canonical name; fallback providers are
	// created on demand from model_list entries.
	providersByName := make(map[string]providers.LLMProvider)
	// Determine primary provider name from model_list.
	if mc, err := cfg.GetModelConfig(model); err == nil && mc != nil {
		ref := providers.ParseModelRef(mc.Model, defaults.Provider)
		if ref != nil {
			providersByName[ref.Provider] = provider
		}
	}
	if len(providersByName) == 0 {
		// Fall back to keying by defaults.Provider
		providersByName[defaults.Provider] = provider
	}
	// Create providers for each fallback model that isn't already in the map.
	for _, fb := range fallbacks {
		ref := providers.ParseModelRef(fb, defaults.Provider)
		if ref == nil {
			continue
		}
		if _, exists := providersByName[ref.Provider]; exists {
			continue
		}
		mc, err := cfg.GetModelConfig(ref.Model)
		if err != nil || mc == nil {
			continue
		}
		fbProvider, _, err := providers.CreateProviderFromConfig(mc)
		if err == nil {
			providersByName[ref.Provider] = fbProvider
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
