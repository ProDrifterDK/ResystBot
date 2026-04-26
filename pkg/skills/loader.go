package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`)

const (
	MaxNameLength        = 64
	MaxDescriptionLength = 1024
)

type SkillTriggers struct {
	Keywords []string `json:"keywords" yaml:"keywords"`
	Tools    []string `json:"tools" yaml:"tools"`
	Agents   []string `json:"agents" yaml:"agents"`
}

type SkillInjectConfig struct {
	Method      string `json:"method" yaml:"method"`
	Priority    string `json:"priority" yaml:"priority"`
	TokenBudget int    `json:"token_budget" yaml:"token_budget"`
}

type SkillMetadata struct {
	Name        string            `json:"name" yaml:"name"`
	Description string            `json:"description" yaml:"description"`
	Version     string            `json:"version" yaml:"version"`
	Triggers    SkillTriggers     `json:"triggers" yaml:"triggers"`
	Inject      SkillInjectConfig `json:"inject" yaml:"inject"`
	Author      string            `json:"author" yaml:"author"`
	Tags        []string          `json:"tags" yaml:"tags"`
}

type SkillInfo struct {
	Name        string         `json:"name"`
	Path        string         `json:"path"`
	Source      string         `json:"source"`
	Description string         `json:"description"`
	Version     string         `json:"version"`
	Metadata    *SkillMetadata `json:"metadata"`
	Loaded      bool           `json:"loaded"`
	BodyHash    string         `json:"body_hash"`
}

func truncateToTokenBudget(content string, budget int) string {
	if budget <= 0 {
		return content
	}
	maxChars := budget * 4
	if len(content) <= maxChars {
		return content
	}
	if maxChars <= len(truncationNotice) {
		return truncationNotice[:maxChars]
	}
	contentBudget := maxChars - len(truncationNotice)
	truncated := content[:contentBudget]
	if lastNL := strings.LastIndex(truncated, "\n\n"); lastNL > contentBudget/2 {
		truncated = truncated[:lastNL]
	}
	return strings.TrimRight(truncated, "\n") + truncationNotice
}

func (info SkillInfo) validate() error {
	var errs error
	if info.Name == "" {
		errs = errors.Join(errs, errors.New("name is required"))
	} else {
		if len(info.Name) > MaxNameLength {
			errs = errors.Join(errs, fmt.Errorf("name exceeds %d characters", MaxNameLength))
		}
		if !namePattern.MatchString(info.Name) {
			errs = errors.Join(errs, errors.New("name must be alphanumeric with hyphens"))
		}
	}

	if info.Description == "" {
		errs = errors.Join(errs, errors.New("description is required"))
	} else if len(info.Description) > MaxDescriptionLength {
		errs = errors.Join(errs, fmt.Errorf("description exceeds %d character", MaxDescriptionLength))
	}
	return errs
}

type SkillsLoader struct {
	workspace       string
	workspaceSkills string // workspace skills (project-level)
	globalSkills    string // global skills (~/.picoclaw/skills)
	builtinSkills   string // builtin skills
}

func NewSkillsLoader(workspace string, globalSkills string, builtinSkills string) *SkillsLoader {
	return &SkillsLoader{
		workspace:       workspace,
		workspaceSkills: filepath.Join(workspace, "skills"),
		globalSkills:    globalSkills, // ~/.picoclaw/skills
		builtinSkills:   builtinSkills,
	}
}

func (sl *SkillsLoader) ListSkills() []SkillInfo {
	skills := make([]SkillInfo, 0)
	seen := make(map[string]bool)

	addSkills := func(dir, source string) {
		if dir == "" {
			return
		}
		dirs, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			skillFile := filepath.Join(dir, d.Name(), "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				continue
			}
			info := SkillInfo{
				Name:   d.Name(),
				Path:   skillFile,
				Source: source,
				Loaded: true,
			}
			metadata := sl.getSkillMetadata(skillFile)
			if metadata != nil {
				info.Description = metadata.Description
				info.Name = metadata.Name
				info.Version = metadata.Version
				info.Metadata = metadata
			}
			if content, err := os.ReadFile(skillFile); err == nil {
				info.BodyHash = computeBodyHash(sl.stripFrontmatter(string(content)))
			}
			if err := info.validate(); err != nil {
				slog.Warn("invalid skill from "+source, "name", info.Name, "error", err)
				continue
			}
			if seen[info.Name] {
				continue
			}
			seen[info.Name] = true
			skills = append(skills, info)
		}
	}

	// Priority: workspace > global > builtin
	addSkills(sl.workspaceSkills, "workspace")
	addSkills(sl.globalSkills, "global")
	addSkills(sl.builtinSkills, "builtin")

	return skills
}

func (sl *SkillsLoader) LoadSkill(name string) (string, bool) {
	// 1. load from workspace skills first (project-level)
	if sl.workspaceSkills != "" {
		skillFile := filepath.Join(sl.workspaceSkills, name, "SKILL.md")
		if content, err := os.ReadFile(skillFile); err == nil {
			return sl.stripFrontmatter(string(content)), true
		}
	}

	// 2. then load from global skills (~/.picoclaw/skills)
	if sl.globalSkills != "" {
		skillFile := filepath.Join(sl.globalSkills, name, "SKILL.md")
		if content, err := os.ReadFile(skillFile); err == nil {
			return sl.stripFrontmatter(string(content)), true
		}
	}

	// 3. finally load from builtin skills
	if sl.builtinSkills != "" {
		skillFile := filepath.Join(sl.builtinSkills, name, "SKILL.md")
		if content, err := os.ReadFile(skillFile); err == nil {
			return sl.stripFrontmatter(string(content)), true
		}
	}

	return "", false
}

func (sl *SkillsLoader) LoadSkillsForContext(skillNames []string) string {
	if len(skillNames) == 0 {
		return ""
	}

	var parts []string
	for _, name := range skillNames {
		content, ok := sl.LoadSkill(name)
		if ok {
			parts = append(parts, fmt.Sprintf("### Skill: %s\n\n%s", name, content))
		}
	}

	return strings.Join(parts, "\n\n---\n\n")
}

func (sl *SkillsLoader) BuildSkillsSummary() string {
	allSkills := sl.ListSkills()
	if len(allSkills) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "<skills>")
	for _, s := range allSkills {
		escapedName := escapeXML(s.Name)
		escapedDesc := escapeXML(s.Description)
		escapedPath := escapeXML(s.Path)

		lines = append(lines, fmt.Sprintf("  <skill>"))
		lines = append(lines, fmt.Sprintf("    <name>%s</name>", escapedName))
		lines = append(lines, fmt.Sprintf("    <description>%s</description>", escapedDesc))
		lines = append(lines, fmt.Sprintf("    <location>%s</location>", escapedPath))
		lines = append(lines, fmt.Sprintf("    <source>%s</source>", s.Source))
		lines = append(lines, "  </skill>")
	}
	lines = append(lines, "</skills>")

	return strings.Join(lines, "\n")
}

func (sl *SkillsLoader) BuildSkillsIndex() string {
	allSkills := sl.ListSkills()
	if len(allSkills) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "# Skills\n")
	lines = append(lines, "The following skills extend your capabilities. Use the `skill` tool to load full instructions.")
	lines = append(lines, "<available_skills>")

	for _, s := range allSkills {
		lines = append(lines, fmt.Sprintf("  <skill>"))
		lines = append(lines, fmt.Sprintf("    <name>%s</name>", escapeXML(s.Name)))
		lines = append(lines, fmt.Sprintf("    <description>%s</description>", escapeXML(s.Description)))
		if s.Metadata != nil && len(s.Metadata.Tags) > 0 {
			lines = append(lines, fmt.Sprintf("    <tags>%s</tags>", escapeXML(strings.Join(s.Metadata.Tags, ", "))))
		}
		lines = append(lines, "  </skill>")
	}

	lines = append(lines, "</available_skills>")
	lines = append(lines, "\nTo activate a skill, call: skill(name=\"skill-name\")")

	return strings.Join(lines, "\n")
}

func (sl *SkillsLoader) getSkillMetadata(skillPath string) *SkillMetadata {
	content, err := os.ReadFile(skillPath)
	if err != nil {
		logger.WarnCF("skills", "Failed to read skill metadata",
			map[string]any{
				"skill_path": skillPath,
				"error":      err.Error(),
			})
		return nil
	}

	frontmatter := sl.extractFrontmatter(string(content))
	if frontmatter == "" {
		return defaultSkillMetadata(filepath.Base(filepath.Dir(skillPath)))
	}

	// Try JSON first (for backward compatibility)
	var jsonMeta SkillMetadata
	if err := json.Unmarshal([]byte(frontmatter), &jsonMeta); err == nil {
		metadata := defaultSkillMetadata(filepath.Base(filepath.Dir(skillPath)))
		if jsonMeta.Name != "" {
			metadata.Name = jsonMeta.Name
		}
		if jsonMeta.Description != "" {
			metadata.Description = jsonMeta.Description
		}
		if jsonMeta.Version != "" {
			metadata.Version = jsonMeta.Version
		}
		if len(jsonMeta.Triggers.Keywords) > 0 {
			metadata.Triggers.Keywords = jsonMeta.Triggers.Keywords
		}
		if len(jsonMeta.Triggers.Tools) > 0 {
			metadata.Triggers.Tools = jsonMeta.Triggers.Tools
		}
		if len(jsonMeta.Triggers.Agents) > 0 {
			metadata.Triggers.Agents = jsonMeta.Triggers.Agents
		}
		if jsonMeta.Inject.Method != "" {
			metadata.Inject.Method = jsonMeta.Inject.Method
		}
		if jsonMeta.Inject.Priority != "" {
			metadata.Inject.Priority = jsonMeta.Inject.Priority
		}
		if jsonMeta.Inject.TokenBudget != 0 {
			metadata.Inject.TokenBudget = jsonMeta.Inject.TokenBudget
		}
		if jsonMeta.Author != "" {
			metadata.Author = jsonMeta.Author
		}
		if len(jsonMeta.Tags) > 0 {
			metadata.Tags = jsonMeta.Tags
		}
		return metadata
	}

	parsedMeta := sl.parseEnhancedYAML(frontmatter)
	metadata := defaultSkillMetadata(filepath.Base(filepath.Dir(skillPath)))
	if name, ok := parsedMeta["name"].(string); ok && name != "" {
		metadata.Name = name
	}
	if description, ok := parsedMeta["description"].(string); ok {
		metadata.Description = description
	}
	if version, ok := parsedMeta["version"].(string); ok && version != "" {
		metadata.Version = version
	}
	if author, ok := parsedMeta["author"].(string); ok {
		metadata.Author = author
	}
	metadata.Tags = stringSliceValue(parsedMeta["tags"])
	if triggers, ok := parsedMeta["triggers"].(map[string]any); ok {
		if keywords := stringSliceValue(triggers["keywords"]); len(keywords) > 0 {
			metadata.Triggers.Keywords = keywords
		}
		if tools := stringSliceValue(triggers["tools"]); len(tools) > 0 {
			metadata.Triggers.Tools = tools
		}
		if agents := stringSliceValue(triggers["agents"]); len(agents) > 0 {
			metadata.Triggers.Agents = agents
		}
	}
	if inject, ok := parsedMeta["inject"].(map[string]any); ok {
		if method, ok := inject["method"].(string); ok && method != "" {
			metadata.Inject.Method = method
		}
		if priority, ok := inject["priority"].(string); ok && priority != "" {
			metadata.Inject.Priority = priority
		}
		if tokenBudget, ok := intValue(inject["token_budget"]); ok {
			metadata.Inject.TokenBudget = tokenBudget
		}
	}
	return metadata
}

// parseEnhancedYAML parses a limited YAML subset used by SKILL.md frontmatter.
// Supports simple key/value pairs, bracket arrays, and nested objects via indentation.
func (sl *SkillsLoader) parseEnhancedYAML(content string) map[string]any {
	type frame struct {
		indent int
		values map[string]any
	}

	result := make(map[string]any)
	stack := []frame{{indent: -1, values: result}}

	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	for _, rawLine := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := len(rawLine) - len(strings.TrimLeft(rawLine, " "))
		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}

		key, value, ok := splitYAMLLine(trimmed)
		if !ok || key == "" {
			continue
		}

		current := stack[len(stack)-1].values
		if value == "" {
			child := make(map[string]any)
			current[key] = child
			stack = append(stack, frame{indent: indent, values: child})
			continue
		}

		current[key] = parseYAMLValue(value)
	}

	return result
}

// parseSimpleYAML is retained for backward compatibility with existing callers/tests.
func (sl *SkillsLoader) parseSimpleYAML(content string) map[string]string {
	enhanced := sl.parseEnhancedYAML(content)
	result := make(map[string]string)
	for key, value := range enhanced {
		if str, ok := value.(string); ok {
			result[key] = str
		}
	}
	return result
}

func (sl *SkillsLoader) extractFrontmatter(content string) string {
	// Support \n (Unix), \r\n (Windows), and \r (classic Mac) line endings for frontmatter blocks
	// (?s) enables DOTALL so . matches newlines;
	// ^--- at start, then ... --- at start of line, honoring all three line ending types
	re := regexp.MustCompile(`(?s)^---(?:\r\n|\n|\r)(.*?)(?:\r\n|\n|\r)---`)
	match := re.FindStringSubmatch(content)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

func (sl *SkillsLoader) stripFrontmatter(content string) string {
	// Support \n (Unix), \r\n (Windows), and \r (classic Mac) line endings for frontmatter blocks
	// (?s) enables DOTALL so . matches newlines;
	// ^--- at start, then ... --- at start of line, honoring all three line ending types
	// Match zero or more trailing line endings after closing --- (handles both with and without blank lines)
	re := regexp.MustCompile(`(?s)^---(?:\r\n|\n|\r)(.*?)(?:\r\n|\n|\r)---(?:\r\n|\n|\r)*`)
	return re.ReplaceAllString(content, "")
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func defaultSkillMetadata(name string) *SkillMetadata {
	return &SkillMetadata{
		Name:    name,
		Version: "0.1.0",
		Triggers: SkillTriggers{
			Agents: []string{"default"},
		},
		Inject: SkillInjectConfig{
			Method:   "system_prompt",
			Priority: "normal",
		},
	}
}

func splitYAMLLine(line string) (string, string, bool) {
	inSingle := false
	inDouble := false
	for i, r := range line {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ':':
			if !inSingle && !inDouble {
				return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
			}
		}
	}
	return "", "", false
}

func parseYAMLValue(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return parseStringArray(trimmed)
	}
	if unquoted, ok := unquoteYAMLString(trimmed); ok {
		return unquoted
	}
	if intVal, ok := intValue(trimmed); ok {
		return intVal
	}
	return trimmed
}

func parseStringArray(value string) []string {
	var arr []string
	if err := json.Unmarshal([]byte(value), &arr); err == nil {
		return arr
	}

	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
	if inner == "" {
		return []string{}
	}

	parts := strings.Split(inner, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if unquoted, ok := unquoteYAMLString(part); ok {
			part = unquoted
		}
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func unquoteYAMLString(value string) (string, bool) {
	if len(value) < 2 {
		return "", false
	}
	if (strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) ||
		(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
		return value[1 : len(value)-1], true
	}
	return "", false
}

func stringSliceValue(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		return result
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	default:
		return nil
	}
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func computeBodyHash(content string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
