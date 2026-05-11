package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/learning"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type learningRetriever interface {
	Search(ctx context.Context, query string, topK int) ([]learning.LessonRecord, error)
}

type ContextBuilder struct {
	workspace           string
	skillsLoader        *skills.SkillsLoader
	triggerEngine       *skills.TriggerEngine
	lastTriggerContext  skills.TriggerContext
	memory              *MemoryStore
	tools               *tools.ToolRegistry    // Direct reference to tool registry
	retriever           memory.MemoryRetriever // nil = use fallback
	lastInjectedChunks  []memory.MemoryChunk
	learningRetriever   learningRetriever
	learningConfig      *config.LearningConfig
	lastInjectedLessons []learning.LessonRecord
	lastReceivedAt      string // ISO timestamp of when the current message was received
}

func getGlobalConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".picoclaw")
}

func NewContextBuilder(workspace string) *ContextBuilder {
	// builtin skills: skills directory in current project
	// Use the skills/ directory under the current working directory
	wd, _ := os.Getwd()
	builtinSkillsDir := filepath.Join(wd, "skills")
	globalSkillsDir := filepath.Join(getGlobalConfigDir(), "skills")

	return &ContextBuilder{
		workspace:    workspace,
		skillsLoader: skills.NewSkillsLoader(workspace, globalSkillsDir, builtinSkillsDir),
		memory:       NewMemoryStore(workspace),
	}
}

// SetToolsRegistry sets the tools registry for dynamic tool summary generation.
func (cb *ContextBuilder) SetToolsRegistry(registry *tools.ToolRegistry) {
	cb.tools = registry
}

// SetRetriever sets the memory retriever for auto-injection of relevant memories.
// When nil, BuildMessages falls back to the static memory index.
func (cb *ContextBuilder) SetRetriever(r memory.MemoryRetriever) {
	cb.retriever = r
}

// SetLearningRetriever sets the lesson retriever and config for prompt injection.
func (cb *ContextBuilder) SetLearningRetriever(lr learningRetriever, cfg *config.LearningConfig) {
	cb.learningRetriever = lr
	cb.learningConfig = cfg
}

// GetInjectedChunks returns the memory chunks injected in the last BuildMessages call.
func (cb *ContextBuilder) GetInjectedChunks() []memory.MemoryChunk {
	return cb.lastInjectedChunks
}

// GetInjectedLessons returns a copy of the lessons injected in the last BuildMessages call.
func (cb *ContextBuilder) GetInjectedLessons() []learning.LessonRecord {
	if len(cb.lastInjectedLessons) == 0 {
		return nil
	}
	lessons := make([]learning.LessonRecord, len(cb.lastInjectedLessons))
	copy(lessons, cb.lastInjectedLessons)
	return lessons
}

// GetSkillsLoader returns the skills loader for tool registration.
func (cb *ContextBuilder) GetSkillsLoader() *skills.SkillsLoader {
	return cb.skillsLoader
}

// SetTriggerEngine sets the trigger engine for auto-injection.
func (cb *ContextBuilder) SetTriggerEngine(engine *skills.TriggerEngine) {
	cb.triggerEngine = engine
}

// UpdateTriggerContext updates the context used for trigger matching.
func (cb *ContextBuilder) UpdateTriggerContext(msg skills.TriggerContext) {
	cb.lastTriggerContext = msg
}

// SetReceivedAt sets the ISO timestamp of when the current message was received.
// This is injected into the system prompt so the agent has natural time awareness
// without needing to execute a shell command.
func (cb *ContextBuilder) SetReceivedAt(iso string) {
	cb.lastReceivedAt = iso
}

// RecordToolCall records a tool call for trigger matching.
func (cb *ContextBuilder) RecordToolCall(toolName string, sessionKey string) {
	if cb.triggerEngine != nil {
		cb.triggerEngine.RecordToolCall(toolName, sessionKey)
	}
}

func (cb *ContextBuilder) getIdentity() string {
	now := time.Now().Format("2006-01-02 15:04 (Monday)")

	receivedSection := ""
	if cb.lastReceivedAt != "" {
		receivedSection = fmt.Sprintf("\n\n## Message Received At\n%s", cb.lastReceivedAt)
	}
	workspacePath, _ := filepath.Abs(filepath.Join(cb.workspace))
	runtime := fmt.Sprintf("%s %s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version())

	// Build tools section dynamically
	toolsSection := cb.buildToolsSection()

	return fmt.Sprintf(`# picoclaw 🦞

You are picoclaw, a helpful AI assistant.

## Current Time
%s%s

## Runtime
%s

## Workspace
Your workspace is at: %s
- Memory: %s/memory/MEMORY.md
- Daily Notes: %s/memory/YYYYMM/YYYYMMDD.md
- Skills: %s/skills/{skill-name}/SKILL.md

%s

## Important Rules

1. **ALWAYS use tools** - When you need to perform an action (schedule reminders, send messages, execute commands, etc.), you MUST call the appropriate tool. Do NOT just say you'll do it or pretend to do it.

2. **Be helpful and accurate** - When using tools, briefly explain what you're doing.

3. **Memory** - When interacting with me if something seems memorable, update %s/memory/MEMORY.md`,
		now, receivedSection, runtime, workspacePath, workspacePath, workspacePath, workspacePath, toolsSection, workspacePath)
}

func (cb *ContextBuilder) buildToolsSection() string {
	if cb.tools == nil {
		return ""
	}

	summaries := cb.tools.GetSummaries()
	if len(summaries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Tools\n\n")
	sb.WriteString(
		"**CRITICAL**: You MUST use tools to perform actions. Do NOT pretend to execute commands or schedule tasks.\n\n",
	)
	sb.WriteString("You have access to the following tools:\n\n")
	for _, s := range summaries {
		sb.WriteString(s)
		sb.WriteString("\n")
	}

	return sb.String()
}

func (cb *ContextBuilder) BuildSystemPrompt() string {
	registry := newPromptRegistry()
	registry.register(&identityContributor{builder: cb})
	registry.register(&bootstrapContributor{builder: cb})
	registry.register(&skillsIndexContributor{builder: cb})
	registry.register(&autoSkillsContributor{builder: cb})
	registry.register(&memoryFallbackContributor{builder: cb})

	parts, err := registry.collect(context.Background())
	if err != nil {
		return cb.buildSystemPromptLegacy()
	}

	return renderPromptParts(parts, "\n\n---\n\n")
}

func (cb *ContextBuilder) buildSystemPromptLegacy() string {
	parts := []string{}

	// Core identity section
	parts = append(parts, cb.getIdentity())

	// Bootstrap files
	bootstrapContent := cb.LoadBootstrapFiles()
	if bootstrapContent != "" {
		parts = append(parts, bootstrapContent)
	}

	// Skills - v2: compact index only, full body loaded on demand
	skillsIndex := cb.skillsLoader.BuildSkillsIndex()
	if skillsIndex != "" {
		parts = append(parts, skillsIndex)
	}

	// Auto-injected skills (from trigger matching)
	if cb.triggerEngine != nil {
		autoSkills := cb.triggerEngine.MatchSkills(cb.lastTriggerContext)
		for _, match := range autoSkills {
			content, _ := cb.skillsLoader.LoadSkill(match.Skill.Name)
			if content != "" {
				parts = append(parts, fmt.Sprintf(
					"<skill_content name=%q reason=%q>\n%s\n</skill_content>",
					match.Skill.Name, match.Reason, content))
			}
		}
	}

	// Memory context — when retriever is available, auto-injection happens in BuildMessages instead
	if cb.retriever == nil {
		memoryContext := cb.memory.GetMemoryIndex()
		if memoryContext != "" {
			parts = append(parts, "# Memory\n\n"+memoryContext)
		}
	}

	// Join with "---" separator
	return strings.Join(parts, "\n\n---\n\n")
}

func (cb *ContextBuilder) LoadBootstrapFiles() string {
	bootstrapFiles := []string{
		"AGENTS.md",
		"SOUL.md",
		"USER.md",
		"IDENTITY.md",
	}

	var sb strings.Builder
	for _, filename := range bootstrapFiles {
		filePath := filepath.Join(cb.workspace, filename)
		if data, err := os.ReadFile(filePath); err == nil {
			fmt.Fprintf(&sb, "## %s\n\n%s\n\n", filename, data)
		}
	}

	return sb.String()
}

func (cb *ContextBuilder) BuildMessages(
	ctx context.Context,
	history []providers.Message,
	summary string,
	currentMessage string,
	media []string,
	channel, chatID string,
	identities ...UserIdentity,
) []providers.Message {
	messages := []providers.Message{}
	var identity UserIdentity
	if len(identities) > 0 {
		identity = identities[0]
	}
	cb.lastInjectedChunks = nil
	cb.lastInjectedLessons = nil

	lessonSection := cb.buildLearningSection(ctx, currentMessage)

	systemPrompt := cb.BuildSystemPrompt()
	if lessonSection != "" {
		systemPrompt = injectPromptSectionBeforeMemory(systemPrompt, lessonSection)
	}

	// Auto-inject relevant memories if retriever is available
	if cb.retriever != nil && strings.TrimSpace(currentMessage) != "" {
		retrievalCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		chunks, err := cb.retriever.Search(retrievalCtx, currentMessage, 5)
		cancel()
		if err != nil {
			logger.WarnCF("agent", "Memory auto-injection failed, using fallback",
				map[string]any{"error": err.Error()})
			memoryContext := cb.memory.GetMemoryIndex()
			if memoryContext != "" {
				systemPrompt += "\n\n# Memory\n\n" + memoryContext
			}
		} else if len(chunks) > 0 {
			cb.lastInjectedChunks = chunks
			var memSection strings.Builder
			memSection.WriteString("\n\n# Relevant Memory\nThe following memories were retrieved based on the current conversation. Use them as context.\n\n")
			for _, chunk := range chunks {
				date := chunk.CreatedAt.Format("2006-01-02")
				text := chunk.Text
				if len(text) > 1200 {
					text = text[:1200] + "..."
				}
				memSection.WriteString(fmt.Sprintf("[%s] (%s) %s\n\n", date, chunk.Source, text))
			}
			memSection.WriteString("Use the search_memory tool if you need information not shown above.")
			systemPrompt += memSection.String()
		}
	}

	// Add Current Session info if provided
	if channel != "" && chatID != "" {
		systemPrompt += fmt.Sprintf("\n\n## Current Session\nChannel: %s\nChat ID: %s", channel, chatID)
	}

	// Inject pending TeamForge inbox notifications
	if tfInbox := ReadTeamForgeInbox(); tfInbox != "" {
		systemPrompt += "\n\n" + tfInbox
	}

	// Log system prompt summary for debugging (debug mode only)
	logger.DebugCF("agent", "System prompt built",
		map[string]any{
			"total_chars":   len(systemPrompt),
			"total_lines":   strings.Count(systemPrompt, "\n") + 1,
			"section_count": strings.Count(systemPrompt, "\n\n---\n\n") + 1,
		})

	// Log preview of system prompt (avoid logging huge content)
	preview := systemPrompt
	if len(preview) > 500 {
		preview = preview[:500] + "... (truncated)"
	}
	logger.DebugCF("agent", "System prompt preview",
		map[string]any{
			"preview": preview,
		})

	if summary != "" {
		systemPrompt += "\n\n## Summary of Previous Conversation\n\n" + summary
	}
	if section := buildInterlocutorSection(identity); section != "" {
		// Keep current-turn identity last so it wins over static bootstrap files
		// (AGENTS.md/USER.md) and over summaries from previous conversations.
		systemPrompt += section
	}

	history = sanitizeHistoryForProvider(history)

	messages = append(messages, providers.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	messages = append(messages, history...)

	if strings.TrimSpace(currentMessage) != "" {
		messages = append(messages, providers.Message{
			Role:    "user",
			Content: currentMessage,
		})
	}

	return messages
}

func buildInterlocutorSection(identity UserIdentity) string {
	displayName := sanitizeIdentityField(identity.DisplayName)
	username := sanitizeIdentityField(strings.TrimPrefix(identity.Username, "@"))
	userID := sanitizeIdentityField(identity.UserID)
	senderID := sanitizeIdentityField(identity.SenderID)
	role := sanitizeIdentityField(identity.Role)

	if role == "" && identity.IsGuest {
		role = "guest"
	}
	if displayName == "" && username != "" {
		displayName = "@" + username
	}
	if senderID == "" && username != "" {
		senderID = username
	}
	if displayName == "" && username == "" && userID == "" && senderID == "" && role == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Current Interlocutor\n")
	sb.WriteString("AUTHORITATIVE CURRENT-TURN IDENTITY. This section supersedes static bootstrap text (AGENTS.md/USER.md), memories, and previous conversation summaries that assume the user is Alan. Answer identity questions from this section, not from old history. Do not call the interlocutor Alan unless this section says they are Alan/ProDrifterDK.\n")
	if displayName != "" {
		fmt.Fprintf(&sb, "Display name: %s\n", displayName)
	}
	if username != "" {
		fmt.Fprintf(&sb, "Telegram username: @%s\n", username)
	}
	if userID != "" {
		fmt.Fprintf(&sb, "Telegram user ID: %s\n", userID)
	}
	if senderID != "" {
		fmt.Fprintf(&sb, "Sender ID: %s\n", senderID)
	}
	if role != "" {
		fmt.Fprintf(&sb, "Role/trust level: %s\n", role)
	}
	if identity.IsGuest || role == "guest" {
		sb.WriteString("Guest safety: this is not Alan/ProDrifterDK. Treat requests as guest-level and do not use privileged local tools unless policy explicitly allows it.\n")
	}
	return sb.String()
}

func sanitizeIdentityField(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.Join(strings.Fields(value), " ")
}

func (cb *ContextBuilder) buildLearningSection(ctx context.Context, currentMessage string) string {
	if cb.learningRetriever == nil || cb.learningConfig == nil || !cb.learningConfig.Enabled {
		return ""
	}
	query := strings.TrimSpace(currentMessage)
	if query == "" {
		return ""
	}

	retrievalCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	lessons, err := cb.learningRetriever.Search(retrievalCtx, query, cb.learningConfig.GetMaxRetrievedLessons())
	cancel()
	if err != nil {
		logger.WarnCF("agent", "Learning auto-injection failed, continuing without lessons",
			map[string]any{"error": err.Error()})
		return ""
	}
	if len(lessons) == 0 {
		return ""
	}

	cb.lastInjectedLessons = make([]learning.LessonRecord, len(lessons))
	copy(cb.lastInjectedLessons, lessons)

	var section strings.Builder
	section.WriteString("## Past Learnings (use these to avoid repeating mistakes)\n\n")
	for i, lesson := range lessons {
		section.WriteString(fmt.Sprintf("%d. %s\n", i+1, formatLearningLesson(lesson)))
		if lesson.ID != "" {
			section.WriteString(fmt.Sprintf("   - Lesson ID: %s\n", lesson.ID))
		}
		if tags := strings.Join(lesson.Tags, ", "); tags != "" {
			section.WriteString(fmt.Sprintf("   - Tags: %s\n", tags))
		}
		section.WriteString("\n")
	}
	return strings.TrimSpace(section.String())
}

func formatLearningLesson(lesson learning.LessonRecord) string {
	parts := make([]string, 0, 5)
	if situation := strings.TrimSpace(lesson.Situation); situation != "" {
		parts = append(parts, "Situation: "+situation)
	}
	if errorMessage := strings.TrimSpace(lesson.ErrorMessage); errorMessage != "" {
		parts = append(parts, "Mistake/Error: "+errorMessage)
	}
	if correction := strings.TrimSpace(lesson.Correction); correction != "" {
		parts = append(parts, "Correction: "+correction)
	}
	if betterApproach := strings.TrimSpace(lesson.BetterApproach); betterApproach != "" {
		parts = append(parts, "Better approach: "+betterApproach)
	} else if approach := strings.TrimSpace(lesson.Approach); approach != "" {
		parts = append(parts, "Approach: "+approach)
	}
	if outcome := strings.TrimSpace(lesson.Outcome); outcome != "" {
		parts = append(parts, "Outcome: "+outcome)
	}
	if len(parts) == 0 {
		if lesson.ID != "" {
			return "Lesson " + lesson.ID
		}
		return "Retrieved lesson"
	}
	return strings.Join(parts, " | ")
}

func injectPromptSectionBeforeMemory(systemPrompt, section string) string {
	section = strings.TrimSpace(section)
	if section == "" {
		return systemPrompt
	}
	const separator = "\n\n---\n\n"
	memoryMarker := separator + "# Memory\n\n"
	if idx := strings.Index(systemPrompt, memoryMarker); idx >= 0 {
		return systemPrompt[:idx] + separator + section + systemPrompt[idx:]
	}
	if systemPrompt == "" {
		return section
	}
	return systemPrompt + separator + section
}

func sanitizeHistoryForProvider(history []providers.Message) []providers.Message {
	if len(history) == 0 {
		return history
	}

	sanitized := make([]providers.Message, 0, len(history))
	for _, msg := range history {
		switch msg.Role {
		case "tool":
			if len(sanitized) == 0 {
				logger.DebugCF("agent", "Dropping orphaned leading tool message", map[string]any{})
				continue
			}
			last := sanitized[len(sanitized)-1]
			if last.Role != "assistant" || len(last.ToolCalls) == 0 {
				logger.DebugCF("agent", "Dropping orphaned tool message", map[string]any{})
				continue
			}
			sanitized = append(sanitized, msg)

		case "assistant":
			if len(msg.ToolCalls) > 0 {
				if len(sanitized) == 0 {
					logger.DebugCF("agent", "Dropping assistant tool-call turn at history start", map[string]any{})
					continue
				}
				prev := sanitized[len(sanitized)-1]
				if prev.Role != "user" && prev.Role != "tool" {
					logger.DebugCF(
						"agent",
						"Dropping assistant tool-call turn with invalid predecessor",
						map[string]any{"prev_role": prev.Role},
					)
					continue
				}
			}
			sanitized = append(sanitized, msg)

		default:
			sanitized = append(sanitized, msg)
		}
	}

	return sanitized
}

func (cb *ContextBuilder) AddToolResult(
	messages []providers.Message,
	toolCallID, toolName, result string,
) []providers.Message {
	messages = append(messages, providers.Message{
		Role:       "tool",
		Content:    result,
		ToolCallID: toolCallID,
	})
	return messages
}

func (cb *ContextBuilder) AddAssistantMessage(
	messages []providers.Message,
	content string,
	toolCalls []map[string]any,
) []providers.Message {
	msg := providers.Message{
		Role:    "assistant",
		Content: content,
	}
	// Always add assistant message, whether or not it has tool calls
	messages = append(messages, msg)
	return messages
}

// GetSkillsInfo returns information about loaded skills.
func (cb *ContextBuilder) GetSkillsInfo() map[string]any {
	allSkills := cb.skillsLoader.ListSkills()
	skillNames := make([]string, 0, len(allSkills))
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}
	return map[string]any{
		"total":     len(allSkills),
		"available": len(allSkills),
		"names":     skillNames,
	}
}
