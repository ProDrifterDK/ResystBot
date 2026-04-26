package agent

import (
	"context"
	"fmt"
)

type identityContributor struct {
	builder *ContextBuilder
}

func (c *identityContributor) contributePrompt(context.Context) ([]promptPart, error) {
	if c == nil || c.builder == nil {
		return []promptPart{}, nil
	}

	content := c.builder.getIdentity()
	if content == "" {
		return []promptPart{}, nil
	}

	return []promptPart{{
		ID:      "identity",
		Layer:   promptLayerSystem,
		Slot:    slotIdentity,
		Title:   "Identity",
		Content: content,
	}}, nil
}

type bootstrapContributor struct {
	builder *ContextBuilder
}

func (c *bootstrapContributor) contributePrompt(context.Context) ([]promptPart, error) {
	if c == nil || c.builder == nil {
		return []promptPart{}, nil
	}

	content := c.builder.LoadBootstrapFiles()
	if content == "" {
		return []promptPart{}, nil
	}

	return []promptPart{{
		ID:      "bootstrap",
		Layer:   promptLayerSystem,
		Slot:    slotBootstrap,
		Title:   "Bootstrap",
		Content: content,
	}}, nil
}

type skillsIndexContributor struct {
	builder *ContextBuilder
}

func (c *skillsIndexContributor) contributePrompt(context.Context) ([]promptPart, error) {
	if c == nil || c.builder == nil || c.builder.skillsLoader == nil {
		return []promptPart{}, nil
	}

	content := c.builder.skillsLoader.BuildSkillsIndex()
	if content == "" {
		return []promptPart{}, nil
	}

	return []promptPart{{
		ID:      "skills-index",
		Layer:   promptLayerSystem,
		Slot:    slotSkillsIndex,
		Title:   "Skills",
		Content: content,
	}}, nil
}

type autoSkillsContributor struct {
	builder *ContextBuilder
}

func (c *autoSkillsContributor) contributePrompt(context.Context) ([]promptPart, error) {
	if c == nil || c.builder == nil || c.builder.triggerEngine == nil || c.builder.skillsLoader == nil {
		return []promptPart{}, nil
	}

	matches := c.builder.triggerEngine.MatchSkills(c.builder.lastTriggerContext)
	if len(matches) == 0 {
		return []promptPart{}, nil
	}

	parts := make([]promptPart, 0, len(matches))
	for _, match := range matches {
		content, _ := c.builder.skillsLoader.LoadSkill(match.Skill.Name)
		if content == "" {
			continue
		}

		parts = append(parts, promptPart{
			ID:    fmt.Sprintf("auto-skill-%s", match.Skill.Name),
			Layer: promptLayerSystem,
			Slot:  slotAutoSkills,
			Title: fmt.Sprintf("Skill: %s", match.Skill.Name),
			Content: fmt.Sprintf(
				"<skill_content name=%q reason=%q>\n%s\n</skill_content>",
				match.Skill.Name,
				match.Reason,
				content,
			),
		})
	}

	return parts, nil
}

type memoryFallbackContributor struct {
	builder *ContextBuilder
}

func (c *memoryFallbackContributor) contributePrompt(context.Context) ([]promptPart, error) {
	if c == nil || c.builder == nil || c.builder.retriever != nil || c.builder.memory == nil {
		return []promptPart{}, nil
	}

	content := c.builder.memory.GetMemoryIndex()
	if content == "" {
		return []promptPart{}, nil
	}

	return []promptPart{{
		ID:      "memory-fallback",
		Layer:   promptLayerSystem,
		Slot:    slotMemory,
		Title:   "Memory",
		Content: "# Memory\n\n" + content,
	}}, nil
}
