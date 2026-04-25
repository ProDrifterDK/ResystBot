package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func startSleepProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep process: %v", err)
	}
	return cmd
}

func TestBgTracker_RegisterAndList(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &bgProcessTracker{filePath: filepath.Join(tmpDir, trackerFileName)}

	err := tracker.register(12345, "sleep 60", "/tmp/test.log", "/home")
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	entries := tracker.list()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].PID != 12345 {
		t.Errorf("expected PID 12345, got %d", entries[0].PID)
	}
	if entries[0].Command != "sleep 60" {
		t.Errorf("expected command 'sleep 60', got %s", entries[0].Command)
	}
}

func TestBgTracker_Remove(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &bgProcessTracker{filePath: filepath.Join(tmpDir, trackerFileName)}

	tracker.register(111, "cmd1", "", "")
	tracker.register(222, "cmd2", "", "")
	tracker.register(333, "cmd3", "", "")

	err := tracker.remove(222)
	if err != nil {
		t.Fatalf("remove failed: %v", err)
	}

	entries := tracker.list()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after remove, got %d", len(entries))
	}
	for _, e := range entries {
		if e.PID == 222 {
			t.Error("PID 222 should have been removed")
		}
	}
}

func TestBgTracker_CleanOrphans(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "orphan.log")
	os.WriteFile(logFile, []byte("test"), 0o644)

	tracker := &bgProcessTracker{filePath: filepath.Join(tmpDir, trackerFileName)}

	// Register a PID that doesn't exist (orphan)
	tracker.register(9999999, "dead_cmd", logFile, "")

	killed := tracker.cleanOrphans()
	if len(killed) != 1 {
		t.Fatalf("expected 1 orphan killed, got %d", len(killed))
	}

	// Log file should be cleaned up
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Error("expected orphan log file to be removed")
	}

	// Registry should be empty
	entries := tracker.list()
	if len(entries) != 0 {
		t.Errorf("expected empty registry after cleanup, got %d entries", len(entries))
	}
}

func TestBgTracker_CleanOrphans_KeepsAlive(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &bgProcessTracker{filePath: filepath.Join(tmpDir, trackerFileName)}

	// Start a real process
	cmd := startSleepProcess(t)
	defer syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)

	tracker.register(cmd.Process.Pid, "sleep 60", "", "")

	// Also register an orphan
	tracker.register(9999999, "dead_cmd", "", "")

	killed := tracker.cleanOrphans()
	if len(killed) != 1 {
		t.Fatalf("expected 1 orphan killed, got %d: %v", len(killed), killed)
	}

	entries := tracker.list()
	if len(entries) != 1 {
		t.Fatalf("expected 1 alive entry, got %d", len(entries))
	}
	if entries[0].PID != cmd.Process.Pid {
		t.Errorf("expected alive PID %d, got %d", cmd.Process.Pid, entries[0].PID)
	}
}

func TestBgTracker_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, trackerFileName)

	tracker1 := &bgProcessTracker{filePath: path}
	tracker1.register(42, "persistent_cmd", "/tmp/p.log", "")

	// New tracker instance reading same file
	tracker2 := &bgProcessTracker{filePath: path}
	entries := tracker2.list()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry from persistence, got %d", len(entries))
	}
	if entries[0].PID != 42 {
		t.Errorf("expected PID 42, got %d", entries[0].PID)
	}
}

func TestFormatBgList(t *testing.T) {
	entries := []BgProcess{
		{PID: 100, Command: "python3 server.py", LogFile: "/tmp/test.log"},
	}
	output := FormatBgList(entries)
	if !containsStr(output, "PID 100") {
		t.Errorf("expected PID 100 in output, got: %s", output)
	}
	if !containsStr(output, "python3 server.py") {
		t.Errorf("expected command in output, got: %s", output)
	}
}

func TestFormatBgList_Empty(t *testing.T) {
	output := FormatBgList(nil)
	if !containsStr(output, "No background processes") {
		t.Errorf("expected empty message, got: %s", output)
	}
}

func TestKillBgProcess_InvalidPID(t *testing.T) {
	result := killBgProcess("notanumber")
	if !containsStr(result, "Invalid PID") {
		t.Errorf("expected invalid PID message, got: %s", result)
	}
}

func TestKillBgProcess_AlreadyDead(t *testing.T) {
	tmpDir := t.TempDir()
	defaultTracker = &bgProcessTracker{filePath: filepath.Join(tmpDir, trackerFileName)}

	defaultTracker.register(9999999, "dead", "", "")

	result := killBgProcess(strconv.Itoa(9999999))
	if !containsStr(result, "already dead") {
		t.Errorf("expected 'already dead' message, got: %s", result)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstr(s, sub))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
