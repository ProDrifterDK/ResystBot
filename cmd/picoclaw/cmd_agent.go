// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chzyer/readline"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func agentCmd() {
	message := ""
	sessionKey := "cli:default"
	modelOverride := ""
	channel := "cli"
	chatID := "direct"

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--debug", "-d":
			logger.SetLevel(logger.DEBUG)
			fmt.Println("🔍 Debug mode enabled")
		case "-m", "--message":
			if i+1 < len(args) {
				message = args[i+1]
				i++
			}
		case "-s", "--session":
			if i+1 < len(args) {
				sessionKey = args[i+1]
				i++
			}
		case "--model", "-model":
			if i+1 < len(args) {
				modelOverride = args[i+1]
				i++
			}
		case "--channel":
			if i+1 < len(args) {
				channel = args[i+1]
				i++
			}
		case "--chat-id":
			if i+1 < len(args) {
				chatID = args[i+1]
				i++
			}
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	if modelOverride != "" {
		cfg.Agents.Defaults.ModelName = modelOverride
	}

	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		fmt.Printf("Error creating provider: %v\n", err)
		os.Exit(1)
	}
	// Use the resolved model ID from provider creation
	if modelID != "" {
		cfg.Agents.Defaults.ModelName = modelID
	}

	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider)

	// internalMessages are operational status messages that should not be forwarded to the user.
	isInternalMessage := func(content string) bool {
		internalPrefixes := []string{
			"Context window exceeded",
			"Compressing history",
		}
		for _, prefix := range internalPrefixes {
			if strings.Contains(content, prefix) {
				return true
			}
		}
		return false
	}

	// Start a goroutine to listen for outbound messages (e.g. from the message tool).
	// Each message is flushed immediately with os.Stdout.Sync() so tg_listener.py
	// receives it right away even when picoclaw's stdout is piped (OS pipe buffer).
	go func() {
		ctx := context.Background()
		for {
			msg, ok := msgBus.SubscribeOutbound(ctx)
			if !ok {
				break
			}
			// Skip internal operational status messages — these should not be sent to the user
			if isInternalMessage(msg.Content) {
				logger.DebugCF("agent", "Suppressing internal status message from stdout",
					map[string]any{"content": msg.Content})
				continue
			}
			// Print the outbound message to stdout so tg_listener.py can capture it.
			// Flush immediately so the pipe reader sees it without waiting for more data.
			fmt.Printf("\n%s %s\n\n", logo, msg.Content)
			os.Stdout.Sync() //nolint:errcheck
		}
	}()

	// Print agent startup info (only for interactive mode)
	startupInfo := agentLoop.GetStartupInfo()
	logger.InfoCF("agent", "Agent initialized",
		map[string]any{
			"tools_count":      startupInfo["tools"].(map[string]any)["count"],
			"skills_total":     startupInfo["skills"].(map[string]any)["total"],
			"skills_available": startupInfo["skills"].(map[string]any)["available"],
		})

	if message != "" {
		ctx := context.Background()
		response, err := agentLoop.ProcessDirectWithChannel(ctx, message, sessionKey, channel, chatID)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
		if !agentLoop.HasSentMessageInRound() && response != "" {
			fmt.Printf("\n%s %s\n", logo, response)
		}

		// Wait for any spawned subagent goroutines to finish, then drain
		// the inbound bus so subagent result messages are processed and
		// any outbound replies are printed before the process exits.
		agentLoop.WaitForSubagents()
		agentLoop.DrainInbound(ctx, channel, chatID)
		// Give the outbound-listener goroutine time to print any messages
		// that DrainInbound triggered via bus.PublishOutbound.
		time.Sleep(200 * time.Millisecond)
	} else {
		fmt.Printf("%s Interactive mode (Ctrl+C to exit)\n\n", logo)
		interactiveMode(agentLoop, sessionKey, channel, chatID)
	}
}

func interactiveMode(agentLoop *agent.AgentLoop, sessionKey, channel, chatID string) {
	prompt := fmt.Sprintf("%s You: ", logo)

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          prompt,
		HistoryFile:     filepath.Join(os.TempDir(), ".picoclaw_history"),
		HistoryLimit:    100,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		fmt.Printf("Error initializing readline: %v\n", err)
		fmt.Println("Falling back to simple input mode...")
		simpleInteractiveMode(agentLoop, sessionKey, channel, chatID)
		return
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt || err == io.EOF {
				fmt.Println("\nGoodbye!")
				return
			}
			fmt.Printf("Error reading input: %v\n", err)
			continue
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if input == "exit" || input == "quit" {
			fmt.Println("Goodbye!")
			return
		}

		ctx := context.Background()
		response, err := agentLoop.ProcessDirectWithChannel(ctx, input, sessionKey, channel, chatID)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		if !agentLoop.HasSentMessageInRound() && response != "" {
			fmt.Printf("\n%s %s\n\n", logo, response)
		}

		// Drain subagent results in background so the prompt stays responsive.
		go func(ch, cid string) {
			agentLoop.WaitForSubagents()
			agentLoop.DrainInbound(context.Background(), ch, cid)
		}(channel, chatID)
	}
}

func simpleInteractiveMode(agentLoop *agent.AgentLoop, sessionKey, channel, chatID string) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("%s You: ", logo)
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Println("\nGoodbye!")
				return
			}
			fmt.Printf("Error reading input: %v\n", err)
			continue
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if input == "exit" || input == "quit" {
			fmt.Println("Goodbye!")
			return
		}

		ctx := context.Background()
		response, err := agentLoop.ProcessDirectWithChannel(ctx, input, sessionKey, channel, chatID)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		if !agentLoop.HasSentMessageInRound() && response != "" {
			fmt.Printf("\n%s %s\n\n", logo, response)
		}

		// Drain subagent results in background so the prompt stays responsive.
		go func(ch, cid string) {
			agentLoop.WaitForSubagents()
			agentLoop.DrainInbound(context.Background(), ch, cid)
		}(channel, chatID)
	}
}
