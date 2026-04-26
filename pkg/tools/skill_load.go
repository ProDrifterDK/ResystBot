package tools

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/skills"
)

type SkillLoadTool struct {
	skillsLoader *skills.SkillsLoader
}

func NewSkillLoadTool(loader *skills.SkillsLoader) *SkillLoadTool {
	return &SkillLoadTool{skillsLoader: loader}
}

func (t *SkillLoadTool) Name() string { return "skill" }

func (t *SkillLoadTool) Description() string {
	return "Load a skill by name. Returns the full skill instructions wrapped in <skill_content> tags. Use this to activate a skill from the available_skills list."
}

func (t *SkillLoadTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Name of the skill to load (from available_skills list)",
			},
		},
		"required": []string{"name"},
	}
}

func (t *SkillLoadTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	name, ok := args["name"].(string)
	if !ok || name == "" {
		return ErrorResult("skill name is required")
	}
	content, found := t.skillsLoader.LoadSkill(name)
	if !found {
		return ErrorResult(fmt.Sprintf("skill %q not found", name))
	}
	return SilentResult(fmt.Sprintf("<skill_content name=%q>\n%s\n</skill_content>", name, content))
}
