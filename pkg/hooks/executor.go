package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const defaultTimeout = 5 * time.Second

type HookExecutor struct {
	config  *config.HooksConfig
	timeout time.Duration
}

func NewHookExecutor(cfg *config.HooksConfig) *HookExecutor {
	return &HookExecutor{
		config:  cfg,
		timeout: defaultTimeout,
	}
}

func (e *HookExecutor) SetTimeout(d time.Duration) {
	e.timeout = d
}

func (e *HookExecutor) RunPreToolUse(ctx context.Context, toolName string, toolInput map[string]any, sessionID string) *HookResult {
	if IsEmpty(e.config) || len(e.config.PreToolUse) == 0 {
		return &HookResult{Decision: DecisionAllow}
	}

	input := HookInput{
		Event:     PreToolUse,
		ToolName:  toolName,
		ToolInput: toolInput,
		SessionID: sessionID,
	}

	result := &HookResult{Decision: DecisionAllow}

	for _, matcher := range e.config.PreToolUse {
		if !MatchTool(matcher.Matcher, toolName) {
			continue
		}

		for _, hook := range matcher.Hooks {
			output, err := e.runCommand(ctx, hook.Command, &input)
			if err != nil {
				logger.WarnCF("hooks", "PreToolUse hook failed, allowing tool",
					map[string]any{"tool": toolName, "hook": hook.Command, "error": err.Error()})
				continue
			}

			if output.Decision == DecisionBlock {
				result.Decision = DecisionBlock
				result.Reason = output.Reason
				logger.InfoCF("hooks", "Tool blocked by hook",
					map[string]any{"tool": toolName, "hook": hook.Command, "reason": output.Reason})
				return result
			}

			if output.Decision == DecisionRedirect {
				result.Decision = DecisionRedirect
				result.ReplacementTool = output.ReplacementTool
				result.ReplacementInput = output.ReplacementInput
				result.Reason = output.Reason
				logger.InfoCF("hooks", "Tool redirected by hook",
					map[string]any{"tool": toolName, "redirected_to": output.ReplacementTool, "hook": hook.Command})
				return result
			}
		}
	}

	return result
}

func (e *HookExecutor) RunPostToolUse(ctx context.Context, toolName string, toolInput map[string]any, toolResponse string, sessionID string) {
	if IsEmpty(e.config) || len(e.config.PostToolUse) == 0 {
		return
	}

	input := HookInput{
		Event:        PostToolUse,
		ToolName:     toolName,
		ToolInput:    toolInput,
		ToolResponse: toolResponse,
		SessionID:    sessionID,
	}

	for _, matcher := range e.config.PostToolUse {
		if !MatchTool(matcher.Matcher, toolName) {
			continue
		}
		for _, hook := range matcher.Hooks {
			if _, err := e.runCommand(ctx, hook.Command, &input); err != nil {
				logger.WarnCF("hooks", "PostToolUse hook failed",
					map[string]any{"tool": toolName, "hook": hook.Command, "error": err.Error()})
			}
		}
	}
}

func (e *HookExecutor) RunSessionStart(ctx context.Context, sessionID string) string {
	if IsEmpty(e.config) || len(e.config.SessionStart) == 0 {
		return ""
	}

	input := HookInput{
		Event:     SessionStart,
		SessionID: sessionID,
	}

	var combinedContext string
	for _, matcher := range e.config.SessionStart {
		for _, hook := range matcher.Hooks {
			output, err := e.runCommand(ctx, hook.Command, &input)
			if err != nil {
				logger.WarnCF("hooks", "SessionStart hook failed",
					map[string]any{"hook": hook.Command, "error": err.Error()})
				continue
			}
			if output.InjectedContext != "" {
				if combinedContext != "" {
					combinedContext += "\n\n"
				}
				combinedContext += output.InjectedContext
			}
		}
	}

	return combinedContext
}

func (e *HookExecutor) RunPreCompact(ctx context.Context, sessionID, compactContext string) string {
	if IsEmpty(e.config) || len(e.config.PreCompact) == 0 {
		return ""
	}

	input := HookInput{
		Event:          PreCompact,
		SessionID:      sessionID,
		CompactContext: compactContext,
	}

	var snapshot string
	for _, matcher := range e.config.PreCompact {
		for _, hook := range matcher.Hooks {
			output, err := e.runCommand(ctx, hook.Command, &input)
			if err != nil {
				logger.WarnCF("hooks", "PreCompact hook failed",
					map[string]any{"hook": hook.Command, "error": err.Error()})
				continue
			}
			if output.InjectedContext != "" {
				if snapshot != "" {
					snapshot += "\n\n"
				}
				snapshot += output.InjectedContext
			}
		}
	}

	return snapshot
}

func (e *HookExecutor) RunUserPromptSubmit(ctx context.Context, prompt, sessionID string) string {
	if IsEmpty(e.config) || len(e.config.UserPromptSubmit) == 0 {
		return prompt
	}

	input := HookInput{
		Event:      UserPromptSubmit,
		UserPrompt: prompt,
		SessionID:  sessionID,
	}

	currentPrompt := prompt
	for _, matcher := range e.config.UserPromptSubmit {
		for _, hook := range matcher.Hooks {
			output, err := e.runCommand(ctx, hook.Command, &input)
			if err != nil {
				logger.WarnCF("hooks", "UserPromptSubmit hook failed",
					map[string]any{"hook": hook.Command, "error": err.Error()})
				continue
			}
			if output.ModifiedPrompt != "" {
				currentPrompt = output.ModifiedPrompt
				input.UserPrompt = currentPrompt
			}
		}
	}

	return currentPrompt
}

func (e *HookExecutor) runCommand(ctx context.Context, command string, input *HookInput) (*HookOutput, error) {
	timeout := e.timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, "sh", "-c", command)
	prepareHookCmd(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("hooks: stdin pipe: %w", err)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("hooks: start command: %w", err)
	}

	go func() {
		<-hookCtx.Done()
		killHookProcessTree(cmd)
	}()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("hooks: marshal input: %w", err)
	}

	if _, writeErr := stdin.Write(inputJSON); writeErr != nil {
		return nil, fmt.Errorf("hooks: write stdin: %w", writeErr)
	}
	stdin.Close()

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("hooks: command failed (stderr: %s): %w", stderr.String(), err)
	}

	output, err := ParseHookOutput(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("hooks: parse output: %w", err)
	}

	return output, nil
}
