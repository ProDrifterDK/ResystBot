// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// ProgressLogger is an optional callback invoked at each significant event in the tool loop.
// taskID identifies the subagent task, label is its human-readable name,
// event is one of: TOOL_CALL, TOOL_RESULT, ERROR, and detail is the relevant content.
type ProgressLogger func(taskID, label, event, detail string)

// ToolLoopConfig configures the tool execution loop.
type ToolLoopConfig struct {
	Provider           providers.LLMProvider
	Model              string
	Tools              *ToolRegistry
	MaxIterations      int
	LLMOptions         map[string]any
	ProgressLogger     ProgressLogger                   // optional; called on each tool call/result/error
	TaskID             string                           // optional; passed to ProgressLogger
	TaskLabel          string                           // optional; passed to ProgressLogger
	FallbackCandidates []providers.FallbackCandidate    // optional; fallback chain after primary fails
	FallbackProviders  map[string]providers.LLMProvider // provider-name → provider for fallback routing
}

// ToolLoopResult contains the result of running the tool loop.
type ToolLoopResult struct {
	Content    string
	Iterations int
}

// RunToolLoop executes the LLM + tool call iteration loop.
// This is the core agent logic that can be reused by both main agent and subagents.
func RunToolLoop(
	ctx context.Context,
	config ToolLoopConfig,
	messages []providers.Message,
	channel, chatID string,
) (*ToolLoopResult, error) {
	iteration := 0
	var finalContent string

	for iteration < config.MaxIterations {
		iteration++

		logger.DebugCF("toolloop", "LLM iteration",
			map[string]any{
				"iteration": iteration,
				"max":       config.MaxIterations,
			})

		// 1. Build tool definitions
		var providerToolDefs []providers.ToolDefinition
		if config.Tools != nil {
			providerToolDefs = config.Tools.ToProviderDefs()
		}

		// 2. Set default LLM options
		llmOpts := config.LLMOptions
		if llmOpts == nil {
			llmOpts = map[string]any{}
		}
		// 3. Call LLM with retry + fallback for server errors
		var response *providers.LLMResponse
		var err error
		maxRetries := 2
		for retry := 0; retry <= maxRetries; retry++ {
			response, err = config.Provider.Chat(ctx, messages, providerToolDefs, config.Model, llmOpts)
			if err == nil {
				break
			}

			errMsg := strings.ToLower(err.Error())
			isServerError := strings.Contains(errMsg, "500") ||
				strings.Contains(errMsg, "502") ||
				strings.Contains(errMsg, "503") ||
				strings.Contains(errMsg, "504") ||
				strings.Contains(errMsg, "unavailable") ||
				strings.Contains(errMsg, "internal server error")

			if isServerError && retry < maxRetries {
				logger.WarnCF("toolloop", "Server error detected, retrying", map[string]any{
					"error": err.Error(),
					"retry": retry,
				})
				time.Sleep(time.Duration(retry+1) * 2 * time.Second) // Exponential backoff
				continue
			}
			break
		}

		// If primary failed with a server error and fallbacks are configured, try them.
		if err != nil && len(config.FallbackCandidates) > 1 {
			errMsg := strings.ToLower(err.Error())
			isRetriable := strings.Contains(errMsg, "500") ||
				strings.Contains(errMsg, "502") ||
				strings.Contains(errMsg, "503") ||
				strings.Contains(errMsg, "504") ||
				strings.Contains(errMsg, "internal server error") ||
				strings.Contains(errMsg, "rate limit") ||
				strings.Contains(errMsg, "429") ||
				strings.Contains(errMsg, "overloaded") ||
				strings.Contains(errMsg, "unavailable")
			if isRetriable {
				// Skip the first candidate (primary) and try the rest.
				for _, candidate := range config.FallbackCandidates[1:] {
					p, ok := config.FallbackProviders[candidate.Provider]
					if !ok {
						continue
					}
					logger.WarnCF("toolloop", fmt.Sprintf("Primary failed, trying fallback %s/%s",
						candidate.Provider, candidate.Model),
						map[string]any{"task_id": config.TaskID, "primary_err": err.Error()})
					fbResp, fbErr := p.Chat(ctx, messages, providerToolDefs, candidate.Model, llmOpts)
					if fbErr == nil {
						response = fbResp
						err = nil
						logger.InfoCF("toolloop", fmt.Sprintf("Fallback succeeded with %s/%s",
							candidate.Provider, candidate.Model),
							map[string]any{"task_id": config.TaskID})
						break
					}
					logger.WarnCF("toolloop", fmt.Sprintf("Fallback %s/%s also failed: %v",
						candidate.Provider, candidate.Model, fbErr),
						map[string]any{"task_id": config.TaskID})
				}
			}
		}

		if err != nil {
			logger.ErrorCF("toolloop", "LLM call failed",
				map[string]any{
					"iteration": iteration,
					"error":     err.Error(),
				})
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}

		// 4. If no tool calls, we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			if finalContent == "" {
				logger.WarnCF("toolloop", "LLM returned empty response with no tool calls, retrying",
					map[string]any{
						"iteration": iteration,
					})

				// Add a system message to prompt the LLM to respond, but only if we haven't already
				if len(messages) > 0 && messages[len(messages)-1].Content != "[System Note: Your previous response was empty. Please provide a valid response or use a tool.]" {
					messages = append(messages, providers.Message{
						Role:    "user",
						Content: "[System Note: Your previous response was empty. Please provide a valid response or use a tool.]",
					})
				}
				continue
			}

			// Clean up any system notes from the final content if the LLM echoed them
			finalContent = strings.TrimPrefix(finalContent, "[System Note: Your previous response was empty. Please provide a valid response or use a tool.]\n\n")
			finalContent = strings.TrimPrefix(finalContent, "[System Note: Your previous response was empty. Please provide a valid response or use a tool.]\n")
			finalContent = strings.TrimPrefix(finalContent, "[System Note: Your previous response was empty. Please provide a valid response or use a tool.]")
			finalContent = strings.TrimSpace(finalContent)

			logger.InfoCF("toolloop", "LLM response without tool calls (direct answer)",
				map[string]any{
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		// 5. Log tool calls
		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("toolloop", "LLM requested tool calls",
			map[string]any{
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// 6. Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:    "assistant",
			Content: response.Content,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:        tc.ID,
				Type:      "function",
				Name:      tc.Name,
				Arguments: tc.Arguments,
				Function: &providers.FunctionCall{
					Name:      tc.Name,
					Arguments: string(argumentsJSON),
				},
			})
		}
		messages = append(messages, assistantMsg)

		// 7. Execute tool calls
		for _, tc := range normalizedToolCalls {
			argsJSON, _ := json.Marshal(tc.Arguments)
			argsPreview := utils.Truncate(string(argsJSON), 200)
			logger.InfoCF("toolloop", fmt.Sprintf("Tool call: %s(%s)", tc.Name, argsPreview),
				map[string]any{
					"tool":      tc.Name,
					"iteration": iteration,
				})

			if config.ProgressLogger != nil {
				config.ProgressLogger(config.TaskID, config.TaskLabel, "TOOL_CALL",
					fmt.Sprintf("[iter %d] %s(%s)", iteration, tc.Name, argsPreview))
			}

			// Execute tool (no async callback for subagents - they run independently)
			var toolResult *ToolResult
			if config.Tools != nil {
				toolResult = config.Tools.ExecuteWithContext(ctx, tc.Name, tc.Arguments, channel, chatID, nil)
			} else {
				toolResult = ErrorResult("No tools available")
			}

			// Determine content for LLM
			contentForLLM := toolResult.ForLLM
			if contentForLLM == "" && toolResult.Err != nil {
				contentForLLM = toolResult.Err.Error()
			}

			if config.ProgressLogger != nil {
				resultPreview := utils.Truncate(contentForLLM, 300)
				event := "TOOL_RESULT"
				if toolResult.IsError {
					event = "TOOL_ERROR"
				}
				config.ProgressLogger(config.TaskID, config.TaskLabel, event,
					fmt.Sprintf("[iter %d] %s → %s", iteration, tc.Name, resultPreview))
			}

			// Add tool result message
			toolResultMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolResultMsg)
		}
	}

	return &ToolLoopResult{
		Content:    finalContent,
		Iterations: iteration,
	}, nil
}
