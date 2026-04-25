package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
)

func getTFInboxDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".picoclaw", "workspace", "state", "tf_inbox")
}

func ReadTeamForgeInbox() string {
	inboxDir := getTFInboxDir()
	if inboxDir == "" {
		return ""
	}

	files, err := filepath.Glob(filepath.Join(inboxDir, "*.json"))
	if err != nil {
		logger.WarnCF("agent", "Failed to glob tf_inbox",
			map[string]any{"error": err.Error(), "dir": inboxDir})
		return ""
	}

	if len(files) == 0 {
		return ""
	}

	sort.Strings(files)

	var messages []string
	var removeFiles []string

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}

		var entry struct {
			Event     string                 `json:"event"`
			Timestamp string                 `json:"timestamp"`
			Data      map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(data, &entry); err != nil {
			logger.WarnCF("agent", "Failed to parse tf_inbox file",
				map[string]any{"error": err.Error(), "file": filepath.Base(f)})
			continue
		}

		switch entry.Event {
		case "message":
			author, _ := entry.Data["author"].(string)
			role, _ := entry.Data["role"].(string)
			content, _ := entry.Data["content"].(string)
			msg := fmt.Sprintf("[TeamForge] %s (%s): %s", author, role, content)
			messages = append(messages, msg)
		case "task_update":
			taskID, _ := entry.Data["task_id"].(string)
			status, _ := entry.Data["status"].(string)
			msg := fmt.Sprintf("[TeamForge Task] Task %s → %s", taskID, status)
			messages = append(messages, msg)
		case "branch_update":
			source, _ := entry.Data["source"].(string)
			target, _ := entry.Data["target"].(string)
			action, _ := entry.Data["action"].(string)
			msg := fmt.Sprintf("[TeamForge Branch] %s → %s (%s)", source, target, action)
			messages = append(messages, msg)
		case "phase_update":
			phase, _ := entry.Data["phase"].(string)
			msg := fmt.Sprintf("[TeamForge Phase] → %s", phase)
			messages = append(messages, msg)
		default:
			logger.DebugCF("agent", "Unknown tf_inbox event type",
				map[string]any{"event": entry.Event, "file": filepath.Base(f)})
		}

		removeFiles = append(removeFiles, f)
	}

	for _, f := range removeFiles {
		if err := os.Remove(f); err != nil {
			logger.WarnCF("agent", "Failed to remove tf_inbox file",
				map[string]any{"error": err.Error(), "file": filepath.Base(f)})
		}
	}

	if len(messages) == 0 {
		return ""
	}

	return "## TeamForge Notifications\n\n" + strings.Join(messages, "\n")
}
