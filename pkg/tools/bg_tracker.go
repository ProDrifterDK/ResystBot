package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const trackerFileName = "bg_processes.json"

type BgProcess struct {
	PID       int       `json:"pid"`
	Command   string    `json:"command"`
	LogFile   string    `json:"log_file"`
	StartedAt time.Time `json:"started_at"`
	WorkDir   string    `json:"work_dir,omitempty"`
}

type bgProcessTracker struct {
	mu       sync.Mutex
	filePath string
}

var defaultTracker *bgProcessTracker

func init() {
	home, _ := os.UserHomeDir()
	if home != "" {
		defaultTracker = &bgProcessTracker{
			filePath: filepath.Join(home, ".picoclaw", "workspace", trackerFileName),
		}
	}
}

func (t *bgProcessTracker) register(pid int, command, logFile, workDir string) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	entries := t.load()
	entries = append(entries, BgProcess{
		PID:       pid,
		Command:   command,
		LogFile:   logFile,
		StartedAt: time.Now(),
		WorkDir:   workDir,
	})
	return t.save(entries)
}

func (t *bgProcessTracker) remove(pid int) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	entries := t.load()
	filtered := make([]BgProcess, 0, len(entries))
	for _, e := range entries {
		if e.PID != pid {
			filtered = append(filtered, e)
		}
	}
	return t.save(filtered)
}

func (t *bgProcessTracker) list() []BgProcess {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.load()
}

func (t *bgProcessTracker) cleanOrphans() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	entries := t.load()
	var alive []BgProcess
	var killed []string

	for _, e := range entries {
		proc, _ := os.FindProcess(e.PID)
		if proc != nil && proc.Signal(syscall.Signal(0)) == nil {
			alive = append(alive, e)
		} else {
			killed = append(killed, fmt.Sprintf("PID %d (%s) — dead, removing from registry", e.PID, e.Command))
			if e.LogFile != "" {
				os.Remove(e.LogFile)
			}
		}
	}

	t.save(alive)
	return killed
}

func (t *bgProcessTracker) load() []BgProcess {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return nil
	}
	var entries []BgProcess
	json.Unmarshal(data, &entries)
	return entries
}

func (t *bgProcessTracker) save(entries []BgProcess) error {
	dir := filepath.Dir(t.filePath)
	os.MkdirAll(dir, 0o755)

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0o644)
}

func BgRegister(pid int, command, logFile, workDir string) {
	defaultTracker.register(pid, command, logFile, workDir)
}

func BgRemove(pid int) {
	defaultTracker.remove(pid)
}

func BgList() []BgProcess {
	return defaultTracker.list()
}

func BgCleanOrphans() []string {
	return defaultTracker.cleanOrphans()
}

func FormatBgList(entries []BgProcess) string {
	if len(entries) == 0 {
		return "No background processes registered."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Background processes (%d):\n", len(entries))
	for _, e := range entries {
		proc, _ := os.FindProcess(e.PID)
		status := "dead"
		if proc != nil && proc.Signal(syscall.Signal(0)) == nil {
			status = "running"
		}
		age := time.Since(e.StartedAt).Truncate(time.Second)
		fmt.Fprintf(&b, "  PID %-6d  [%s]  %s  (age: %s)\n", e.PID, status, e.Command, age)
		if e.LogFile != "" {
			fmt.Fprintf(&b, "    log: %s\n", e.LogFile)
		}
	}
	return b.String()
}

func killBgProcess(pidStr string) string {
	pid, err := strconv.Atoi(strings.TrimSpace(pidStr))
	if err != nil {
		return fmt.Sprintf("Invalid PID: %s", pidStr)
	}

	proc, _ := os.FindProcess(pid)
	if proc == nil {
		return fmt.Sprintf("PID %d not found", pid)
	}

	if err := proc.Signal(syscall.Signal(0)); err != nil {
		BgRemove(pid)
		return fmt.Sprintf("PID %d is already dead, removed from registry", pid)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		proc.Signal(syscall.SIGKILL)
	}

	BgRemove(pid)
	return fmt.Sprintf("PID %d killed and removed from registry", pid)
}
