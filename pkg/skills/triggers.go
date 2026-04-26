package skills

import (
	"strings"
	"sync"
)

const truncationNotice = "\n\n[... skill content truncated due to token budget]"

// TriggerConfig holds per-agent skill trigger configuration.
// This mirrors config.AgentSkillsConfig to avoid circular imports.
type TriggerConfig struct {
	Enabled       bool
	AutoInject    bool
	MaxAutoInject int
	AllowList     []string
	BlockList     []string
	TokenBudget   int
}

type TriggerEngine struct {
	loader              *SkillsLoader
	config              TriggerConfig
	pendingToolTriggers map[string][]TriggerMatch
	mu                  sync.Mutex
}

type TriggerMatch struct {
	Skill    SkillInfo
	Reason   string
	Priority string
}

type TriggerContext struct {
	UserMessage string
	AgentType   string
	SessionKey  string
}

func NewTriggerEngine(loader *SkillsLoader, config TriggerConfig) *TriggerEngine {
	return &TriggerEngine{
		loader:              loader,
		config:              config,
		pendingToolTriggers: make(map[string][]TriggerMatch),
	}
}

func (te *TriggerEngine) MatchSkills(ctx TriggerContext) []TriggerMatch {
	if !te.config.Enabled {
		return nil
	}
	if !te.config.AutoInject {
		return nil
	}

	var allSkills []SkillInfo
	if te.loader != nil {
		allSkills = te.loader.ListSkills()
	}
	filteredSkills := te.applyListFilters(allSkills)

	pending := te.consumePendingToolTriggers(ctx.SessionKey)
	message := strings.ToLower(ctx.UserMessage)
	agentType := ctx.AgentType
	if agentType == "" {
		agentType = "default"
	}

	matches := make([]TriggerMatch, 0)
	seen := make(map[string]bool)

	for _, skill := range filteredSkills {
		metadata := metadataForTrigger(skill)
		if !containsString(metadata.Triggers.Agents, agentType) {
			continue
		}

		priority := metadata.Inject.Priority
		if priority == "" {
			priority = "normal"
		}

		matched := false
		for _, keyword := range metadata.Triggers.Keywords {
			if keyword == "" {
				continue
			}
			if strings.Contains(message, strings.ToLower(keyword)) {
				matches = append(matches, TriggerMatch{
					Skill:    skill,
					Reason:   "keyword:" + strings.ToLower(keyword),
					Priority: priority,
				})
				seen[strings.ToLower(skill.Name)] = true
				matched = true
				break
			}
		}

		if matched {
			continue
		}

		if len(metadata.Triggers.Keywords) == 0 && len(metadata.Triggers.Tools) == 0 {
			matches = append(matches, TriggerMatch{
				Skill:    skill,
				Reason:   "agent:" + strings.ToLower(agentType),
				Priority: priority,
			})
			seen[strings.ToLower(skill.Name)] = true
		}
	}

	for _, pendingMatch := range pending {
		name := strings.ToLower(pendingMatch.Skill.Name)
		if seen[name] {
			continue
		}
		matches = append(matches, pendingMatch)
		seen[name] = true
	}

	sortMatches(matches)
	if te.config.MaxAutoInject > 0 && len(matches) > te.config.MaxAutoInject {
		matches = matches[:te.config.MaxAutoInject]
	}

	return matches
}

func (te *TriggerEngine) RecordToolCall(toolName, sessionKey string) {
	if te == nil || te.loader == nil || toolName == "" || sessionKey == "" {
		return
	}

	toolName = strings.ToLower(toolName)
	queued := make([]TriggerMatch, 0)
	for _, skill := range te.loader.ListSkills() {
		metadata := metadataForTrigger(skill)
		if !containsString(metadata.Triggers.Tools, toolName) {
			continue
		}

		priority := metadata.Inject.Priority
		if priority == "" {
			priority = "normal"
		}
		queued = append(queued, TriggerMatch{
			Skill:    skill,
			Reason:   "tool:" + toolName,
			Priority: priority,
		})
	}

	if len(queued) == 0 {
		return
	}

	te.mu.Lock()
	te.pendingToolTriggers[sessionKey] = append(te.pendingToolTriggers[sessionKey], queued...)
	te.mu.Unlock()
}

func (te *TriggerEngine) applyListFilters(skills []SkillInfo) []SkillInfo {
	filtered := make([]SkillInfo, 0, len(skills))
	for _, skill := range skills {
		if len(te.config.AllowList) > 0 && !containsString(te.config.AllowList, skill.Name) {
			continue
		}
		if containsString(te.config.BlockList, skill.Name) {
			continue
		}
		filtered = append(filtered, skill)
	}
	return filtered
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func sortMatches(matches []TriggerMatch) {
	for i := 1; i < len(matches); i++ {
		current := matches[i]
		j := i - 1
		for j >= 0 && priorityRank(matches[j].Priority) > priorityRank(current.Priority) {
			matches[j+1] = matches[j]
			j--
		}
		matches[j+1] = current
	}
}

func priorityRank(priority string) int {
	switch strings.ToLower(priority) {
	case "high":
		return 0
	case "low":
		return 2
	default:
		return 1
	}
}

func metadataForTrigger(skill SkillInfo) *SkillMetadata {
	if skill.Metadata != nil {
		return skill.Metadata
	}
	return defaultSkillMetadata(skill.Name)
}

func (te *TriggerEngine) consumePendingToolTriggers(sessionKey string) []TriggerMatch {
	if sessionKey == "" {
		return nil
	}

	te.mu.Lock()
	defer te.mu.Unlock()

	pending := append([]TriggerMatch(nil), te.pendingToolTriggers[sessionKey]...)
	delete(te.pendingToolTriggers, sessionKey)
	return pending
}
