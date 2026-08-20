package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type Server struct {
	httpServer *http.Server
	agentLoop  *agent.AgentLoop
	msgBus     *bus.MessageBus
	channel    string

	mu           sync.RWMutex
	streams      map[string]*Stream
	chatStreams  map[string]string
	runs         map[string]*chatRun
	resetting    map[string]bool
	startTime    time.Time
	resetTimeout time.Duration
	resetSession func(context.Context, bus.InboundMessage, agent.ResetMode) (*agent.ResetResult, error)

	relayCancel context.CancelFunc
	relayWG     sync.WaitGroup
	wg          sync.WaitGroup
}

type chatRun struct {
	id          string
	chatID      string
	stream      *Stream
	cancel      context.CancelFunc
	done        chan struct{}
	predecessor *chatRun
}

type Stream struct {
	ID        string
	ChatID    string
	Events    chan Event
	CreatedAt time.Time
}

type Event struct {
	Name string
	Data json.RawMessage
}

type messageInput struct {
	ChatID     string `json:"chat_id"`
	User       string `json:"user"`
	Username   string `json:"username"`
	UserID     string `json:"user_id,omitempty"`
	Role       string `json:"role,omitempty"`
	IsGuest    bool   `json:"is_guest,omitempty"`
	Text       string `json:"text"`
	StreamID   string `json:"stream_id"`
	ReceivedAt string `json:"received_at,omitempty"`
}

type cancelInput struct {
	ChatID string `json:"chat_id"`
}

type sessionResetInput struct {
	ChatID string          `json:"chat_id"`
	Mode   agent.ResetMode `json:"mode"`
}

const defaultSessionResetTimeout = 30 * time.Second

func NewServer(agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, channel, host string, port int) *Server {
	s := &Server{
		agentLoop:    agentLoop,
		msgBus:       msgBus,
		channel:      channel,
		streams:      make(map[string]*Stream),
		chatStreams:  make(map[string]string),
		runs:         make(map[string]*chatRun),
		resetting:    make(map[string]bool),
		startTime:    time.Now(),
		resetTimeout: defaultSessionResetTimeout,
	}
	if agentLoop != nil {
		s.resetSession = agentLoop.ResetSession
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/message", s.handleMessage)
	mux.HandleFunc("/v1/stream/", s.handleStream)
	mux.HandleFunc("/v1/cancel", s.handleCancel)
	mux.HandleFunc("/v1/session/reset", s.handleSessionReset)
	mux.HandleFunc("/v1/status", s.handleStatus)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", host, port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	relayCtx, relayCancel := context.WithCancel(context.Background())
	s.relayCancel = relayCancel
	s.relayWG.Add(1)
	go s.runOutboundRelay(relayCtx)

	return s
}

func (s *Server) Start() error {
	logger.InfoCF("transport", "Starting HTTP transport server", map[string]any{"addr": s.httpServer.Addr, "channel": s.channel})
	err := s.httpServer.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		logger.ErrorCF("transport", "HTTP server stopped with error", map[string]any{"error": err.Error()})
	}
	return err
}

func (s *Server) Stop(ctx context.Context) error {
	logger.InfoCF("transport", "Stopping HTTP transport server", nil)

	for _, cancel := range s.snapshotCancels() {
		cancel()
	}

	shutdownErr := s.httpServer.Shutdown(ctx)
	if s.relayCancel != nil {
		s.relayCancel()
	}

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		s.wg.Wait()
		s.relayWG.Wait()
	}()

	select {
	case <-waitDone:
	case <-ctx.Done():
		if shutdownErr == nil {
			shutdownErr = ctx.Err()
		}
	}

	for _, streamID := range s.snapshotStreamIDs() {
		s.removeStream(streamID)
	}

	return shutdownErr
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	defer r.Body.Close()

	var input messageInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		logger.WarnCF("transport", "Failed to decode message request", map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	input.ChatID = strings.TrimSpace(input.ChatID)
	input.Text = strings.TrimSpace(input.Text)
	if input.ChatID == "" || input.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chat_id and text are required"})
		return
	}

	streamID := strings.TrimSpace(input.StreamID)
	if streamID == "" {
		streamID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	stream := &Stream{ID: streamID, ChatID: input.ChatID, Events: make(chan Event, 64), CreatedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	run := &chatRun{id: streamID, chatID: input.ChatID, stream: stream, cancel: cancel, done: make(chan struct{})}

	s.mu.Lock()
	if s.resetting[input.ChatID] {
		s.mu.Unlock()
		cancel()
		writeJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"code":   "session_resetting",
			"error":  "session reset is in progress",
		})
		return
	}
	run.predecessor = s.runs[input.ChatID]
	if run.predecessor != nil {
		run.predecessor.cancel()
	}
	if previousID := s.chatStreams[input.ChatID]; previousID != "" {
		s.removeStreamLocked(previousID)
	}
	if existing := s.streams[streamID]; existing != nil {
		s.removeStreamLocked(streamID)
	}
	s.streams[streamID] = stream
	s.chatStreams[input.ChatID] = streamID
	s.runs[input.ChatID] = run
	s.mu.Unlock()

	s.wg.Add(1)
	logger.InfoCF("transport", "Message accepted", map[string]any{"stream_id": streamID, "chat_id": input.ChatID})
	writeJSON(w, http.StatusAccepted, map[string]string{"stream_id": streamID, "status": "processing"})
	go s.processMessage(ctx, run, input)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	streamID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/stream/"))
	if streamID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream not found"})
		return
	}

	stream := s.getStream(streamID)
	if stream == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream not found"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.ErrorCF("transport", "SSE not supported by response writer", nil)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case event, ok := <-stream.Events:
			if !ok {
				s.removeStreamIfCurrent(stream)
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Name, event.Data); err != nil {
				logger.WarnCF("transport", "Failed to write SSE event", map[string]any{"stream_id": stream.ID, "error": err.Error()})
				s.removeStreamIfCurrent(stream)
				return
			}
			flusher.Flush()
			if event.Name == "response" || event.Name == "error" {
				s.removeStreamIfCurrent(stream)
				return
			}
		case <-pingTicker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				logger.WarnCF("transport", "Failed to write SSE ping", map[string]any{"stream_id": stream.ID, "error": err.Error()})
				s.removeStreamIfCurrent(stream)
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			s.removeStreamIfCurrent(stream)
			return
		}
	}
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	defer r.Body.Close()

	var input cancelInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		logger.WarnCF("transport", "Failed to decode cancel request", map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	input.ChatID = strings.TrimSpace(input.ChatID)
	if input.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chat_id is required"})
		return
	}

	_, stream, run := s.lookupChatState(input.ChatID)
	if run != nil {
		run.cancel()
	}
	if stream != nil {
		payload, err := json.Marshal(map[string]string{"chat_id": input.ChatID, "text": "cancelled"})
		if err != nil {
			logger.ErrorCF("transport", "Failed to marshal cancel event", map[string]any{"error": err.Error(), "chat_id": input.ChatID})
		} else {
			s.sendStreamEvent(stream, Event{Name: "error", Data: payload})
		}
	}
	if stream != nil {
		s.removeStreamIfCurrent(stream)
	}

	logger.InfoCF("transport", "Cancelled chat", map[string]any{"chat_id": input.ChatID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (s *Server) handleSessionReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeResetError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.resetSession == nil {
		writeResetError(w, http.StatusServiceUnavailable, "agent_loop_unavailable", "agent loop is unavailable")
		return
	}
	defer r.Body.Close()

	var input sessionResetInput
	if err := decodeStrictJSON(r, &input); err != nil {
		writeResetError(w, http.StatusBadRequest, "invalid_request", "invalid reset request")
		return
	}
	input.ChatID = strings.TrimSpace(input.ChatID)
	if input.ChatID == "" || !input.Mode.Valid() {
		writeResetError(w, http.StatusBadRequest, "invalid_request", "chat_id and mode soft|hard are required")
		return
	}

	s.mu.Lock()
	if s.resetting[input.ChatID] {
		s.mu.Unlock()
		writeResetError(w, http.StatusConflict, "session_busy", "session reset is already in progress")
		return
	}
	s.resetting[input.ChatID] = true
	run := s.runs[input.ChatID]
	cancelledInFlight := false
	if run != nil {
		select {
		case <-run.done:
		default:
			cancelledInFlight = true
			run.cancel()
		}
	}
	if streamID := s.chatStreams[input.ChatID]; streamID != "" {
		s.removeStreamLocked(streamID)
	}
	s.mu.Unlock()
	defer s.finishReset(input.ChatID)

	ctx, cancel := context.WithTimeout(r.Context(), s.resetTimeout)
	defer cancel()
	if run != nil {
		select {
		case <-run.done:
		case <-ctx.Done():
			writeResetError(w, http.StatusConflict, "session_busy", "active session work did not stop before the reset deadline")
			return
		}
	}

	result, err := s.resetSession(ctx, s.inboundMessage(messageInput{ChatID: input.ChatID}), input.Mode)
	if err != nil {
		logger.ErrorCF("transport", "Session reset failed", map[string]any{
			"chat_id": input.ChatID,
			"mode":    input.Mode,
			"error":   err.Error(),
		})
		var unsupported *agent.SessionResetUnsupportedError
		switch {
		case errors.As(err, &unsupported):
			writeResetError(w, http.StatusNotImplemented, "session_reset_unsupported", "session reset is unsupported")
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			writeResetError(w, http.StatusConflict, "session_busy", "session work did not quiesce before the reset deadline")
		default:
			writeResetError(w, http.StatusInternalServerError, "session_reset_failed", "session reset failed")
		}
		return
	}

	expectedAction := "preserved"
	if input.Mode == agent.ResetModeHard {
		expectedAction = "cleared"
	}
	if result == nil || result.SessionKey == "" || result.ClearedMessages < 0 || result.SummaryAction != expectedAction {
		logger.ErrorCF("transport", "Session reset returned an invalid result", map[string]any{"chat_id": input.ChatID, "mode": input.Mode})
		writeResetError(w, http.StatusInternalServerError, "session_reset_failed", "session reset failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "reset",
		"chat_id":             input.ChatID,
		"session_key":         result.SessionKey,
		"mode":                input.Mode,
		"cleared_messages":    result.ClearedMessages,
		"summary_action":      result.SummaryAction,
		"cancelled_in_flight": cancelledInFlight,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	s.mu.RLock()
	activeChats := make([]string, 0, len(s.runs))
	for chatID := range s.runs {
		activeChats = append(activeChats, chatID)
	}
	activeStreams := make([]string, 0, len(s.streams))
	for streamID := range s.streams {
		activeStreams = append(activeStreams, streamID)
	}
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"active_chats":   activeChats,
		"active_streams": activeStreams,
		"uptime":         time.Since(s.startTime).String(),
	})
}

func (s *Server) processMessage(ctx context.Context, run *chatRun, input messageInput) {
	defer s.wg.Done()
	defer s.finishRun(run)

	if run.predecessor != nil {
		select {
		case <-run.predecessor.done:
		case <-ctx.Done():
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	if s.agentLoop == nil {
		s.sendJSONEvent(run.stream, "error", map[string]string{"chat_id": input.ChatID, "text": "agent loop unavailable"})
		return
	}

	response, err := s.agentLoop.ProcessMessage(ctx, s.inboundMessage(input))
	if ctx.Err() == context.Canceled {
		return
	}
	if err != nil {
		logger.ErrorCF("transport", "Process error", map[string]any{"chat_id": input.ChatID, "error": err.Error()})
		s.sendJSONEvent(run.stream, "error", map[string]string{"chat_id": input.ChatID, "text": err.Error()})
		return
	}

	if response != "" {
		if al := s.agentLoop; al.LastUsage != nil {
			response += fmt.Sprintf("\n\n`in:%d out:%d ctx:%d`", al.LastUsage.PromptTokens, al.LastUsage.CompletionTokens, al.LastContextEstimate)
		}
		s.sendJSONEvent(run.stream, "response", map[string]string{"chat_id": input.ChatID, "text": response})
	}

	s.agentLoop.WaitForSubagents()
	drainCtx, drainCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer drainCancel()
	s.agentLoop.DrainInbound(drainCtx, s.channel, input.ChatID)
}

func (s *Server) inboundMessage(input messageInput) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:    s.channel,
		SenderID:   input.Username,
		ChatID:     input.ChatID,
		Content:    input.Text,
		SessionKey: s.channel + ":" + input.ChatID,
		Metadata: map[string]string{
			"user":        input.User,
			"username":    input.Username,
			"user_id":     input.UserID,
			"role":        input.Role,
			"is_guest":    fmt.Sprintf("%t", input.IsGuest),
			"received_at": input.ReceivedAt,
		},
	}
}

func (s *Server) runOutboundRelay(ctx context.Context) {
	defer s.relayWG.Done()

	for {
		msg, ok := s.msgBus.SubscribeOutbound(ctx)
		if !ok {
			return
		}

		streamID := s.getStreamIDByChat(msg.ChatID)
		if streamID == "" {
			continue
		}
		stream := s.getStream(streamID)
		if stream == nil {
			continue
		}

		if msg.FilePath != "" {
			s.sendJSONEvent(stream, "file", map[string]string{
				"chat_id":      msg.ChatID,
				"text":         msg.Content,
				"file_path":    msg.FilePath,
				"file_caption": msg.FileCaption,
			})
			continue
		}

		s.sendJSONEvent(stream, "status", map[string]string{"chat_id": msg.ChatID, "text": msg.Content})
	}
}

func (s *Server) finishRun(run *chatRun) {
	run.cancel()
	close(run.done)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[run.chatID] == run {
		delete(s.runs, run.chatID)
	}
	s.removeStreamIfCurrentLocked(run.stream)
}

func (s *Server) finishReset(chatID string) {
	s.mu.Lock()
	delete(s.resetting, chatID)
	s.mu.Unlock()
}

func (s *Server) getStream(streamID string) *Stream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.streams[streamID]
}

func (s *Server) getStreamIDByChat(chatID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chatStreams[chatID]
}

func (s *Server) lookupChatState(chatID string) (string, *Stream, *chatRun) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	streamID := s.chatStreams[chatID]
	return streamID, s.streams[streamID], s.runs[chatID]
}

// removeStream removes the current entry by ID for deliberate control operations
// such as shutdown. Lifecycle cleanup with a retained *Stream must use
// removeStreamIfCurrent so reused client IDs cannot remove a successor.
func (s *Server) removeStream(streamID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeStreamLocked(streamID)
}

func (s *Server) removeStreamIfCurrent(stream *Stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeStreamIfCurrentLocked(stream)
}

func (s *Server) removeStreamIfCurrentLocked(stream *Stream) {
	if stream == nil || s.streams[stream.ID] != stream {
		return
	}
	s.removeStreamLocked(stream.ID)
}

func (s *Server) removeStreamLocked(streamID string) {
	stream, ok := s.streams[streamID]
	if !ok {
		return
	}
	delete(s.streams, streamID)
	if currentID, ok := s.chatStreams[stream.ChatID]; ok && currentID == streamID {
		delete(s.chatStreams, stream.ChatID)
	}
	close(stream.Events)
}

func (s *Server) snapshotCancels() []context.CancelFunc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cancels := make([]context.CancelFunc, 0, len(s.runs))
	for _, run := range s.runs {
		cancels = append(cancels, run.cancel)
	}
	return cancels
}

func (s *Server) snapshotStreamIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.streams))
	for streamID := range s.streams {
		ids = append(ids, streamID)
	}
	return ids
}

func (s *Server) sendJSONEvent(stream *Stream, name string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		logger.ErrorCF("transport", "Failed to marshal event", map[string]any{"name": name, "error": err.Error(), "stream_id": stream.ID})
		return
	}
	s.sendStreamEvent(stream, Event{Name: name, Data: data})
}

func (s *Server) sendStreamEvent(stream *Stream, event Event) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.WarnCF("transport", "Dropped event for closed stream", map[string]any{"stream_id": stream.ID, "event": event.Name})
		}
	}()

	select {
	case stream.Events <- event:
	default:
		logger.WarnCF("transport", "Stream buffer full", map[string]any{"stream_id": stream.ID, "event": event.Name})
	}
}

func decodeStrictJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeResetError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"status": "error",
		"code":   code,
		"error":  message,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.ErrorCF("transport", "Failed to write JSON response", map[string]any{"error": err.Error()})
	}
}
