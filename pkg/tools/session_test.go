package tools

import (
	"bytes"
	"os/exec"
	"testing"
	"time"
)

func TestSessionManager_CreateAndGet(t *testing.T) {
	mgr := NewSessionManager()
	session, err := mgr.CreateSession(&ExecSession{Output: &bytes.Buffer{}, State: SessionRunning})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if session.ID == "" {
		t.Fatal("expected session ID to be generated")
	}
	got, ok := mgr.GetSession(session.ID)
	if !ok {
		t.Fatalf("expected session %q to exist", session.ID)
	}
	if got != session {
		t.Fatal("expected GetSession to return the same session pointer")
	}
}

func TestSessionManager_ListSessions(t *testing.T) {
	mgr := NewSessionManager()
	first, err := mgr.CreateSession(&ExecSession{Output: &bytes.Buffer{}, CreatedAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("CreateSession first: %v", err)
	}
	second, err := mgr.CreateSession(&ExecSession{Output: &bytes.Buffer{}, CreatedAt: time.Now()})
	if err != nil {
		t.Fatalf("CreateSession second: %v", err)
	}
	list := mgr.ListSessions()
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	if list[0] != first || list[1] != second {
		t.Fatalf("expected sessions in creation order")
	}
}

func TestSessionManager_KillSession(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30")
	setSysProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start command: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	mgr := NewSessionManager()
	session, err := mgr.CreateSession(&ExecSession{Cmd: cmd, PID: cmd.Process.Pid, Output: &bytes.Buffer{}, State: SessionRunning})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if err := mgr.KillSession(session.ID); err != nil {
		t.Fatalf("KillSession returned error: %v", err)
	}
	if session.GetState() != SessionStopped {
		t.Fatalf("expected session state stopped, got %s", session.GetState())
	}
	_, err = cmd.Process.Wait()
	if err == nil {
		return
	}
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("expected exit error or nil after kill, got %v", err)
	}
}

func TestSessionManager_CleanupOldest(t *testing.T) {
	mgr := NewSessionManager(1)
	oldest, err := mgr.CreateSession(&ExecSession{Output: &bytes.Buffer{}, CreatedAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatalf("CreateSession oldest: %v", err)
	}
	newest, err := mgr.CreateSession(&ExecSession{Output: &bytes.Buffer{}, CreatedAt: time.Now()})
	if err != nil {
		t.Fatalf("CreateSession newest: %v", err)
	}
	if _, ok := mgr.GetSession(oldest.ID); ok {
		t.Fatal("expected oldest session to be evicted")
	}
	if _, ok := mgr.GetSession(newest.ID); !ok {
		t.Fatal("expected newest session to remain")
	}
}

func TestSessionManager_MaxSessions(t *testing.T) {
	mgr := NewSessionManager(2)
	for i := 0; i < 3; i++ {
		_, err := mgr.CreateSession(&ExecSession{Output: &bytes.Buffer{}, CreatedAt: time.Now().Add(time.Duration(i) * time.Second)})
		if err != nil {
			t.Fatalf("CreateSession #%d returned error: %v", i, err)
		}
	}
	if got := len(mgr.ListSessions()); got != 2 {
		t.Fatalf("expected 2 sessions after eviction, got %d", got)
	}
}
