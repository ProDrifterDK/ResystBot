package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// daemonInput is a JSON-line message received from tg_listener on stdin.
type daemonInput struct {
	Type     string `json:"type"` // "message", "cancel", "shutdown"
	ChatID   string `json:"chat_id,omitempty"`
	User     string `json:"user,omitempty"`
	Username string `json:"username,omitempty"`
	Text     string `json:"text,omitempty"`
}

// daemonEvent is a JSON-line event emitted to tg_listener on stdout.
type daemonEvent struct {
	Type     string `json:"type"` // "ready", "status", "response", "error"
	ChatID   string `json:"chat_id,omitempty"`
	Text     string `json:"text,omitempty"`
	FilePath string `json:"file_path,omitempty"` // Path to file for sending photos/documents
}

// emitMu serializes stdout writes from concurrent goroutines.
var emitMu sync.Mutex

// emitEvent marshals a daemonEvent to JSON and writes it to stdout with a
// trailing newline, then flushes. Marshal errors are logged to stderr.
func emitEvent(eventType, chatID, text string, filePath ...string) {
	e := daemonEvent{Type: eventType, ChatID: chatID, Text: text}
	if len(filePath) > 0 {
		e.FilePath = filePath[0]
	}
	b, err := json.Marshal(e)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: emitEvent marshal error: %v\n", err)
		return
	}
	emitMu.Lock()
	defer emitMu.Unlock()
	fmt.Fprintf(os.Stdout, "%s\n", b)
	os.Stdout.Sync() //nolint:errcheck
}

// daemonState tracks per-chat cancellation functions.
type daemonState struct {
	mu        sync.Mutex
	cancelMap map[string]context.CancelFunc
}

// newDaemonState returns an initialised daemonState.
func newDaemonState() *daemonState {
	return &daemonState{
		cancelMap: make(map[string]context.CancelFunc),
	}
}

// registerChat stores the cancel function for chatID. If there is already an
// in-flight request for that chat, the previous cancel is called first.
func (ds *daemonState) registerChat(chatID string, cancel context.CancelFunc) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if prev, ok := ds.cancelMap[chatID]; ok {
		prev()
	}
	ds.cancelMap[chatID] = cancel
}

// cancelChat calls and removes the cancel function for chatID. Returns true if
// there was an in-flight request to cancel, false otherwise.
func (ds *daemonState) cancelChat(chatID string) bool {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	cancel, ok := ds.cancelMap[chatID]
	if !ok {
		return false
	}
	cancel()
	delete(ds.cancelMap, chatID)
	return true
}

// removeChat removes the cancel entry for chatID without calling the cancel
// function. Call this after a request completes normally.
func (ds *daemonState) removeChat(chatID string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	delete(ds.cancelMap, chatID)
}

// cancelAll cancels every in-flight request and clears the map.
func (ds *daemonState) cancelAll() {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	for id, cancel := range ds.cancelMap {
		cancel()
		delete(ds.cancelMap, id)
	}
}

// findLMStudioCLI locates the lms CLI binary on the system.
func findLMStudioCLI() string {
	lmsPath, err := exec.LookPath("lms")
	if err == nil {
		return lmsPath
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		home + "/.lmstudio/bin/lms",
		"/usr/local/bin/lms",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// ensureLMStudioServer checks if the LM Studio server is running and starts
// it if necessary. Returns the lms CLI path, or empty string on failure.
func ensureLMStudioServer(lmsPath string) string {
	if lmsPath == "" {
		lmsPath = findLMStudioCLI()
	}
	if lmsPath == "" {
		logger.WarnCF("daemon", "lms CLI not found", nil)
		return ""
	}

	// Check if server is already running
	out, err := exec.Command(lmsPath, "status").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ON") {
		logger.InfoCF("daemon", "Starting LM Studio server", nil)
		if err := exec.Command(lmsPath, "server", "start").Run(); err != nil {
			logger.WarnCF("daemon", "Failed to start LM Studio server", map[string]any{"error": err.Error()})
			return ""
		}
		time.Sleep(3 * time.Second)
	}

	return lmsPath
}

// ensureLocalModel checks if the primary model or the memory embedding model
// require a local LM Studio server and ensures both are loaded.
func ensureLocalModel(cfg *config.Config) {
	lmsPath := ""

	// --- Bootstrap LM Studio for the default agent LLM if needed ---
	if len(cfg.ModelList) > 0 {
		modelName := cfg.Agents.Defaults.GetModelName()
		var modelCfg *config.ModelConfig
		for i := range cfg.ModelList {
			if cfg.ModelList[i].ModelName == modelName {
				modelCfg = &cfg.ModelList[i]
				break
			}
		}
		if modelCfg != nil && strings.Contains(modelCfg.APIBase, "127.0.0.1:1234") {
			lmsPath = ensureLMStudioServer(lmsPath)
			if lmsPath != "" {
				loadLLMModel(lmsPath, modelCfg.Model, cfg)
			}
		}
	}

	// --- Bootstrap LM Studio for the memory embedding model if needed ---
	embedURL := cfg.Memory.GetEmbeddingURL()
	embedModel := cfg.Memory.GetEmbeddingModel()
	if embedURL != "" && strings.Contains(embedURL, "127.0.0.1:1234") && embedModel != "" {
		lmsPath = ensureLMStudioServer(lmsPath)
		if lmsPath != "" {
			loadEmbeddingModel(lmsPath, embedModel)
		}
	}
}

// loadLLMModel loads a local LLM model via lms with the agent's context window.
func loadLLMModel(lmsPath, model string, cfg *config.Config) {
	_, modelID, found := strings.Cut(model, "/")
	if !found {
		modelID = model
	}

	// Check if model is already loaded
	out, err := exec.Command(lmsPath, "ps").CombinedOutput()
	if err == nil && strings.Contains(string(out), modelID) {
		logger.InfoCF("daemon", "Local LLM already loaded", map[string]any{"model": modelID})
		return
	}

	// Find context window from the default agent config
	ctxWindow := 32768
	for _, a := range cfg.Agents.List {
		if a.Default && a.ContextWindow > 0 {
			ctxWindow = a.ContextWindow
			break
		}
	}
	ctxLen := fmt.Sprintf("%d", ctxWindow)

	logger.InfoCF("daemon", "Loading local LLM", map[string]any{
		"model":          modelID,
		"context_length": ctxLen,
	})

	cmd := exec.Command(lmsPath, "load", modelID, "--context-length", ctxLen, "--ttl", "86400")
	if out, err := cmd.CombinedOutput(); err != nil {
		logger.WarnCF("daemon", "Failed to load local LLM", map[string]any{
			"model":  modelID,
			"error":  err.Error(),
			"output": string(out),
		})
	} else {
		logger.InfoCF("daemon", "Local LLM loaded successfully", map[string]any{"model": modelID})
	}
}

// loadEmbeddingModel loads the memory embedding model via lms.
func loadEmbeddingModel(lmsPath, embedModel string) {
	// Check if model is already loaded
	out, err := exec.Command(lmsPath, "ps").CombinedOutput()
	if err == nil && strings.Contains(string(out), embedModel) {
		logger.InfoCF("daemon", "Embedding model already loaded", map[string]any{"model": embedModel})
		return
	}

	logger.InfoCF("daemon", "Loading embedding model", map[string]any{"model": embedModel})

	cmd := exec.Command(lmsPath, "load", embedModel, "--ttl", "86400")
	if out, err := cmd.CombinedOutput(); err != nil {
		logger.WarnCF("daemon", "Failed to load embedding model", map[string]any{
			"model":  embedModel,
			"error":  err.Error(),
			"output": string(out),
		})
	} else {
		logger.InfoCF("daemon", "Embedding model loaded successfully", map[string]any{"model": embedModel})
	}
}

// daemonMode is the main daemon entry point. It reads JSON-line commands from
// stdin and dispatches chat processing in goroutines, supporting concurrent
// chats with per-chat cancellation.
func daemonMode(cfg *config.Config, agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, channel string) {
	ensureLocalModel(cfg)
	state := newDaemonState()

	// Forward outbound bus messages as {"type":"status"} events so
	// tg_listener can relay intermediate tool output to the user.
	go func() {
		ctx := context.Background()
		for {
			msg, ok := msgBus.SubscribeOutbound(ctx)
			if !ok {
				return
			}
			if msg.FilePath != "" {
				emitEvent("file", msg.ChatID, msg.Content, msg.FilePath)
			} else {
				emitEvent("status", msg.ChatID, msg.Content)
			}
		}
	}()

	// Signal readiness to the parent process.
	emitEvent("ready", "", "")

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		var input daemonInput
		if err := json.Unmarshal(line, &input); err != nil {
			logger.WarnCF("daemon", "Failed to parse stdin line", map[string]any{
				"error": err.Error(),
				"raw":   string(line),
			})
			continue
		}

		switch input.Type {
		case "message":
			// Implicit-cancel: if there is an in-flight request for this
			// chat, cancel it before starting a new one.
			state.cancelChat(input.ChatID)

			ctx, cancel := context.WithCancel(context.Background())
			state.registerChat(input.ChatID, cancel)
			go processChat(ctx, state, agentLoop, channel, input)

		case "cancel":
			if state.cancelChat(input.ChatID) {
				logger.InfoCF("daemon", "Cancelled chat", map[string]any{"chat_id": input.ChatID})
			}

		case "shutdown":
			state.cancelAll()
			return
		}
	}

	// stdin EOF — parent went away.
	state.cancelAll()
}

// processChat handles a single chat request in its own goroutine. It builds
// an InboundMessage, calls the agent loop, emits the response, then drains
// any pending subagent work.
func processChat(ctx context.Context, state *daemonState, agentLoop *agent.AgentLoop, channel string, input daemonInput) {
	defer state.removeChat(input.ChatID)

	sessionKey := channel + ":" + input.ChatID
	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   input.Username,
		ChatID:     input.ChatID,
		Content:    input.Text,
		SessionKey: sessionKey,
		Metadata: map[string]string{
			"user":     input.User,
			"username": input.Username,
		},
	}

	response, err := agentLoop.ProcessMessage(ctx, msg)
	if ctx.Err() == context.Canceled {
		// Request was superseded or explicitly cancelled — exit silently.
		return
	}
	if err != nil {
		emitEvent("error", input.ChatID, err.Error())
		return
	}
	if response != "" {
		if u := agentLoop.LastUsage; u != nil {
			response += fmt.Sprintf("\n\n`in:%d out:%d ctx:%d`", u.PromptTokens, u.CompletionTokens, agentLoop.LastContextEstimate)
		}
		emitEvent("response", input.ChatID, response)
	}

	// Wait for any spawned subagents, then drain pending inbound messages
	// that they may have produced.
	agentLoop.WaitForSubagents()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer drainCancel()
	agentLoop.DrainInbound(drainCtx, channel, input.ChatID)
}
