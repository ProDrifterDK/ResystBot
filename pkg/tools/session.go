package tools

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

type SessionState string

const (
	SessionRunning SessionState = "running"
	SessionStopped SessionState = "stopped"
	SessionExited  SessionState = "exited"
)

type ExecSession struct {
	ID         string
	PID        int
	PTY        *os.File
	Cmd        *exec.Cmd
	State      SessionState
	Output     *bytes.Buffer
	CreatedAt  time.Time
	WorkingDir string

	mu         sync.Mutex
	readOffset int
	exitErr    string
	closedPTY  bool
}

func (s *ExecSession) AppendOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Output == nil {
		s.Output = &bytes.Buffer{}
	}
	_, _ = s.Output.Write(data)
}

func (s *ExecSession) FullOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Output == nil {
		return ""
	}
	return s.Output.String()
}

func (s *ExecSession) PollOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Output == nil {
		return ""
	}
	data := s.Output.Bytes()
	if s.readOffset >= len(data) {
		return ""
	}
	out := string(data[s.readOffset:])
	s.readOffset = len(data)
	return out
}

func (s *ExecSession) SetState(state SessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.State = state
}

func (s *ExecSession) GetState() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.State
}

func (s *ExecSession) SetExitError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.exitErr = ""
		return
	}
	s.exitErr = err.Error()
}

func (s *ExecSession) ExitError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitErr
}

func (s *ExecSession) ClosePTY() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.PTY == nil || s.closedPTY {
		return nil
	}
	s.closedPTY = true
	return s.PTY.Close()
}

type SessionManager struct {
	mu          sync.RWMutex
	sessions    map[string]*ExecSession
	maxSessions int
}

func NewSessionManager(maxSessions ...int) *SessionManager {
	limit := 10
	if len(maxSessions) > 0 && maxSessions[0] > 0 {
		limit = maxSessions[0]
	}
	return &SessionManager{
		sessions:    make(map[string]*ExecSession),
		maxSessions: limit,
	}
}

func (m *SessionManager) CreateSession(session *ExecSession) (*ExecSession, error) {
	if session == nil {
		return nil, fmt.Errorf("session is required")
	}
	if session.ID == "" {
		session.ID = uuid.NewString()
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now()
	}
	if session.Output == nil {
		session.Output = &bytes.Buffer{}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.sessions) >= m.maxSessions {
		if err := m.cleanupOldestLocked(); err != nil {
			return nil, err
		}
	}

	m.sessions[session.ID] = session
	return session, nil
}

func (m *SessionManager) GetSession(id string) (*ExecSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

func (m *SessionManager) ListSessions() []*ExecSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ExecSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (m *SessionManager) KillSession(id string) error {
	m.mu.RLock()
	session, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if err := killSessionProcess(session); err != nil {
		return err
	}
	session.SetState(SessionStopped)
	return session.ClosePTY()
}

func (m *SessionManager) Cleanup() error {
	m.mu.Lock()
	sessions := make([]*ExecSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = make(map[string]*ExecSession)
	m.mu.Unlock()

	var firstErr error
	for _, session := range sessions {
		if session.GetState() == SessionRunning {
			if err := killSessionProcess(session); err != nil && firstErr == nil {
				firstErr = err
			}
			session.SetState(SessionStopped)
		}
		if err := session.ClosePTY(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *SessionManager) cleanupOldestLocked() error {
	if len(m.sessions) < m.maxSessions || len(m.sessions) == 0 {
		return nil
	}

	var oldest *ExecSession
	for _, session := range m.sessions {
		if oldest == nil || session.CreatedAt.Before(oldest.CreatedAt) {
			oldest = session
		}
	}
	if oldest == nil {
		return nil
	}
	delete(m.sessions, oldest.ID)
	if oldest.GetState() == SessionRunning {
		if err := killSessionProcess(oldest); err != nil {
			return err
		}
		oldest.SetState(SessionStopped)
	}
	return oldest.ClosePTY()
}
