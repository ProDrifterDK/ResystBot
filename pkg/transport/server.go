package transport

import (
	"context"
	"encoding/json"
	"fmt"
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

	mu          sync.RWMutex
	streams     map[string]*Stream
	chatStreams map[string]string
	cancelMap   map[string]context.CancelFunc
	startTime   time.Time

	relayCancel context.CancelFunc
	relayWG     sync.WaitGroup
	wg          sync.WaitGroup
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

func NewServer(agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, channel, host string, port int) *Server {
	s := &Server{
		agentLoop:   agentLoop,
		msgBus:      msgBus,
		channel:     channel,
		streams:     make(map[string]*Stream),
		chatStreams: make(map[string]string),
		cancelMap:   make(map[string]context.CancelFunc),
		startTime:   time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/message", s.handleMessage)
	mux.HandleFunc("/v1/stream/", s.handleStream)
	mux.HandleFunc("/v1/cancel", s.handleCancel)
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
	prevCancel, prevStreamID := s.registerStream(stream)
	if prevCancel != nil {
		prevCancel()
	}
	if prevStreamID != "" && prevStreamID != streamID {
		s.removeStream(prevStreamID)
	}

	logger.InfoCF("transport", "Message accepted", map[string]any{"stream_id": streamID, "chat_id": input.ChatID})
	writeJSON(w, http.StatusAccepted, map[string]string{"stream_id": streamID, "status": "processing"})

	s.wg.Add(1)
	go s.processMessage(stream, input)
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
				s.removeStream(stream.ID)
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Name, event.Data); err != nil {
				logger.WarnCF("transport", "Failed to write SSE event", map[string]any{"stream_id": stream.ID, "error": err.Error()})
				s.removeStream(stream.ID)
				return
			}
			flusher.Flush()
			if event.Name == "response" || event.Name == "error" {
				s.removeStream(stream.ID)
				return
			}
		case <-pingTicker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				logger.WarnCF("transport", "Failed to write SSE ping", map[string]any{"stream_id": stream.ID, "error": err.Error()})
				s.removeStream(stream.ID)
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			s.removeStream(stream.ID)
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

	streamID, stream, cancel := s.lookupChatState(input.ChatID)
	if cancel != nil {
		cancel()
	}
	if stream != nil {
		payload, err := json.Marshal(map[string]string{"chat_id": input.ChatID, "text": "cancelled"})
		if err != nil {
			logger.ErrorCF("transport", "Failed to marshal cancel event", map[string]any{"error": err.Error(), "chat_id": input.ChatID})
		} else {
			s.sendStreamEvent(stream, Event{Name: "error", Data: payload})
		}
	}
	if streamID != "" {
		s.removeStream(streamID)
	}

	logger.InfoCF("transport", "Cancelled chat", map[string]any{"chat_id": input.ChatID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	s.mu.RLock()
	activeChats := make([]string, 0, len(s.chatStreams))
	for chatID := range s.chatStreams {
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

func (s *Server) processMessage(stream *Stream, input messageInput) {
	defer s.wg.Done()
	defer s.removeCancel(input.ChatID)
	defer s.removeStream(stream.ID)

	msg := bus.InboundMessage{
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.registerCancel(input.ChatID, cancel)

	response, err := s.agentLoop.ProcessMessage(ctx, msg)
	if ctx.Err() == context.Canceled {
		return
	}
	if err != nil {
		logger.ErrorCF("transport", "Process error", map[string]any{"chat_id": input.ChatID, "error": err.Error()})
		s.sendJSONEvent(stream, "error", map[string]string{"chat_id": input.ChatID, "text": err.Error()})
		return
	}

	if response != "" {
		if al := s.agentLoop; al.LastUsage != nil {
			response += fmt.Sprintf("\n\n`in:%d out:%d ctx:%d`", al.LastUsage.PromptTokens, al.LastUsage.CompletionTokens, al.LastContextEstimate)
		}
		s.sendJSONEvent(stream, "response", map[string]string{"chat_id": input.ChatID, "text": response})
	}

	s.agentLoop.WaitForSubagents()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer drainCancel()
	s.agentLoop.DrainInbound(drainCtx, s.channel, input.ChatID)
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

func (s *Server) registerStream(stream *Stream) (context.CancelFunc, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var prevCancel context.CancelFunc
	var prevStreamID string
	if existingID, ok := s.chatStreams[stream.ChatID]; ok {
		prevStreamID = existingID
		prevCancel = s.cancelMap[stream.ChatID]
	}
	if existing, ok := s.streams[stream.ID]; ok {
		delete(s.chatStreams, existing.ChatID)
		close(existing.Events)
	}
	s.streams[stream.ID] = stream
	s.chatStreams[stream.ChatID] = stream.ID
	return prevCancel, prevStreamID
}

func (s *Server) registerCancel(chatID string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.cancelMap[chatID]; ok {
		prev()
	}
	s.cancelMap[chatID] = cancel
}

func (s *Server) removeCancel(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancelMap, chatID)
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

func (s *Server) lookupChatState(chatID string) (string, *Stream, context.CancelFunc) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	streamID := s.chatStreams[chatID]
	return streamID, s.streams[streamID], s.cancelMap[chatID]
}

func (s *Server) removeStream(streamID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	cancels := make([]context.CancelFunc, 0, len(s.cancelMap))
	for _, cancel := range s.cancelMap {
		cancels = append(cancels, cancel)
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

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.ErrorCF("transport", "Failed to write JSON response", map[string]any{"error": err.Error()})
	}
}
