package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/learning"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type learningE2EStore struct {
	mu                sync.Mutex
	points            map[string]memory.QdrantPoint
	upserts           []memory.QdrantPoint
	updates           []learningPayloadUpdate
	duplicateSearches int
	retrievalSearches int
	updateCh          chan struct{}
}

type learningPayloadUpdate struct {
	pointID string
	fields  map[string]any
}

func newLearningE2EStore() *learningE2EStore {
	return &learningE2EStore{
		points:   make(map[string]memory.QdrantPoint),
		updateCh: make(chan struct{}, 16),
	}
}

func (s *learningE2EStore) Search(ctx context.Context, vector []float64, limit int, filter *memory.QdrantFilter) ([]memory.QdrantSearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make([]string, 0, len(s.points))
	for id := range s.points {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, nil
	}

	results := make([]memory.QdrantSearchResult, 0, len(ids))
	for _, id := range ids {
		point := s.points[id]
		results = append(results, memory.QdrantSearchResult{
			ID:      point.ID,
			Score:   0.98,
			Payload: point.Payload,
		})
	}

	if limit == 1 {
		s.duplicateSearches++
		return results[:1], nil
	}
	s.retrievalSearches++
	if limit < len(results) {
		results = results[:limit]
	}
	return results, nil
}

func (s *learningE2EStore) Upsert(ctx context.Context, points []memory.QdrantPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, point := range points {
		cloned := point
		s.points[point.ID] = cloned
		s.upserts = append(s.upserts, cloned)
	}
	return nil
}

func (s *learningE2EStore) UpdatePayload(ctx context.Context, pointID string, fields map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	point, ok := s.points[pointID]
	if ok {
		if accessCount, ok := fields["access_count"].(int); ok {
			point.Payload.AccessCount = accessCount
		}
		if lastAccessed, ok := fields["last_accessed"].(string); ok {
			point.Payload.LastAccessed = lastAccessed
		}
		s.points[pointID] = point
	}
	clonedFields := make(map[string]any, len(fields))
	for k, v := range fields {
		clonedFields[k] = v
	}
	s.updates = append(s.updates, learningPayloadUpdate{pointID: pointID, fields: clonedFields})
	select {
	case s.updateCh <- struct{}{}:
	default:
	}
	return nil
}

func (s *learningE2EStore) waitForUpdates(t *testing.T, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		s.mu.Lock()
		got := len(s.updates)
		s.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-s.updateCh:
		case <-deadline:
			t.Fatalf("timed out waiting for %d updates, got %d", want, got)
		}
	}
}

type learningE2EEmbedder struct{}

func (e *learningE2EEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	return []float64{0.6, 0.4, 0.2}, nil
}

func (e *learningE2EEmbedder) EmbedForIndexing(ctx context.Context, text string) ([]float64, error) {
	if strings.Contains(text, "permission denied") {
		return []float64{0.9, 0.1, 0.0}, nil
	}
	return []float64{0.6, 0.4, 0.2}, nil
}

type promptCapturingProvider struct {
	response string
	mu       sync.Mutex
	messages [][]providers.Message
}

func (p *promptCapturingProvider) Chat(ctx context.Context, messages []providers.Message, tools []providers.ToolDefinition, model string, opts map[string]any) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cloned := make([]providers.Message, len(messages))
	copy(cloned, messages)
	p.messages = append(p.messages, cloned)
	return &providers.LLMResponse{Content: p.response}, nil
}

func (p *promptCapturingProvider) GetDefaultModel() string { return "openai/test-model" }

func testLearningConfig(workspace string) *config.Config {
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         workspace,
				Provider:          "openai",
				Model:             "openai/test-model",
				MaxTokens:         4096,
				MaxToolIterations: 4,
			},
		},
		Learning: config.LearningConfig{
			Enabled:             true,
			MinUserMessageChars: 10,
		},
	}
}

func withLearningRuntime(t *testing.T, runtime *learning.Runtime, err error) {
	t.Helper()
	prev := initializeLearningRuntime
	initializeLearningRuntime = func(ctx context.Context, cfg *config.LearningConfig) (*learning.Runtime, error) {
		return runtime, err
	}
	t.Cleanup(func() { initializeLearningRuntime = prev })
}

func TestAgentLoop_LearningEndToEnd_PersistsRetrievesAndUpdatesDuplicate(t *testing.T) {
	workspace := t.TempDir()
	store := newLearningE2EStore()
	embedder := &learningE2EEmbedder{}
	cfg := testLearningConfig(workspace)
	encoder := learning.NewEncoder(store, embedder, &cfg.Learning)
	retriever := learning.NewLearningRetriever(store, embedder, &cfg.Learning)
	runtime := &learning.Runtime{
		Encoder:          encoder,
		Retriever:        retriever,
		OutcomeExtractor: learning.NewOutcomeExtractor(encoder, &cfg.Learning),
		VectorSize:       3,
	}
	withLearningRuntime(t, runtime, nil)

	firstProvider := &traceSequenceProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{{
			ID:        "call_learning_fail_1",
			Name:      "trace_fail",
			Arguments: map[string]any{"command": "pip install foo"},
		}},
	}, {
		Content: "first answer after failed tool",
	}}}
	firstLoop := NewAgentLoop(cfg, bus.NewMessageBus(), firstProvider)
	firstLoop.RegisterTool(&failingTraceTool{})

	response, err := firstLoop.ProcessDirectWithChannel(context.Background(), "install this python package in the current environment", "learning-session-1", "test", "chat-1")
	if err != nil {
		t.Fatalf("first ProcessDirectWithChannel() error = %v", err)
	}
	if response != "first answer after failed tool" {
		t.Fatalf("first response = %q", response)
	}

	traceRecords := readTraceRecords(t, workspace+"/mind/traces")
	if len(traceRecords) != 1 {
		t.Fatalf("trace records after first run = %d, want 1", len(traceRecords))
	}
	if got := traceRecords[0].InjectedLearningIDs; len(got) != 0 {
		t.Fatalf("first trace injected lessons = %v, want none", got)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("lesson upserts after first run = %d, want 1", len(store.upserts))
	}
	lessonPoint := store.upserts[0]
	var storedLesson learning.LessonRecord
	if err := json.Unmarshal([]byte(lessonPoint.Payload.Text), &storedLesson); err != nil {
		t.Fatalf("unmarshal stored lesson: %v", err)
	}
	if storedLesson.TraceID != traceRecords[0].ID {
		t.Fatalf("stored lesson trace_id = %q, want %q", storedLesson.TraceID, traceRecords[0].ID)
	}
	if storedLesson.Source != "tool_error" {
		t.Fatalf("stored lesson source = %q, want tool_error", storedLesson.Source)
	}

	secondProvider := &promptCapturingProvider{response: "second answer with learning"}
	secondLoop := NewAgentLoop(cfg, bus.NewMessageBus(), secondProvider)

	response, err = secondLoop.ProcessDirectWithChannel(context.Background(), "install this package safely on Pop!_OS", "learning-session-2", "test", "chat-2")
	if err != nil {
		t.Fatalf("second ProcessDirectWithChannel() error = %v", err)
	}
	if response != "second answer with learning" {
		t.Fatalf("second response = %q", response)
	}
	if len(secondProvider.messages) == 0 || len(secondProvider.messages[0]) == 0 {
		t.Fatal("expected captured prompt messages")
	}
	prompt := secondProvider.messages[0][0].Content
	if !strings.Contains(prompt, "## Past Learnings (use these to avoid repeating mistakes)") {
		t.Fatalf("prompt missing past learnings section: %q", prompt)
	}
	if !strings.Contains(prompt, "Lesson ID: "+lessonPoint.ID) {
		t.Fatalf("prompt missing injected lesson id %q", lessonPoint.ID)
	}

	store.waitForUpdates(t, 1)
	traceRecords = readTraceRecords(t, workspace+"/mind/traces")
	if len(traceRecords) != 2 {
		t.Fatalf("trace records after second run = %d, want 2", len(traceRecords))
	}
	if got := traceRecords[1].InjectedLearningIDs; len(got) != 1 || got[0] != lessonPoint.ID {
		t.Fatalf("second trace injected lessons = %v, want [%s]", got, lessonPoint.ID)
	}

	thirdProvider := &traceSequenceProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{{
			ID:        "call_learning_fail_2",
			Name:      "trace_fail",
			Arguments: map[string]any{"command": "pip install foo"},
		}},
	}, {
		Content: "third answer after duplicate lesson",
	}}}
	thirdLoop := NewAgentLoop(cfg, bus.NewMessageBus(), thirdProvider)
	thirdLoop.RegisterTool(&failingTraceTool{})

	response, err = thirdLoop.ProcessDirectWithChannel(context.Background(), "install this python package in the current environment", "learning-session-3", "test", "chat-3")
	if err != nil {
		t.Fatalf("third ProcessDirectWithChannel() error = %v", err)
	}
	if response != "third answer after duplicate lesson" {
		t.Fatalf("third response = %q", response)
	}
	store.waitForUpdates(t, 2)
	if len(store.upserts) != 1 {
		t.Fatalf("lesson upserts after duplicate run = %d, want 1", len(store.upserts))
	}
	if store.duplicateSearches == 0 {
		t.Fatal("expected duplicate search path to run")
	}
	if len(store.updates) < 2 {
		t.Fatalf("payload updates = %d, want at least 2", len(store.updates))
	}
	duplicateUpdate := store.updates[len(store.updates)-1]
	if duplicateUpdate.pointID != lessonPoint.ID {
		t.Fatalf("duplicate update point_id = %q, want %q", duplicateUpdate.pointID, lessonPoint.ID)
	}
	if got, ok := duplicateUpdate.fields["access_count"].(int); !ok || got < 2 {
		t.Fatalf("duplicate update access_count = %#v, want >= 2", duplicateUpdate.fields["access_count"])
	}
	if _, ok := duplicateUpdate.fields["last_accessed"].(string); !ok {
		t.Fatalf("duplicate update missing last_accessed: %#v", duplicateUpdate.fields)
	}
	traceRecords = readTraceRecords(t, workspace+"/mind/traces")
	if len(traceRecords) != 3 {
		t.Fatalf("trace records after third run = %d, want 3", len(traceRecords))
	}
}

func TestAgentLoop_LearningRuntimeUnavailableDoesNotBreakChat(t *testing.T) {
	workspace := t.TempDir()
	cfg := testLearningConfig(workspace)
	withLearningRuntime(t, nil, errors.New("embedding/qdrant unavailable"))

	provider := &promptCapturingProvider{response: "chat still succeeds"}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)

	response, err := al.ProcessDirectWithChannel(context.Background(), "explain the package failure without crashing chat", "learning-resilience-session", "test", "chat-resilience")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel() error = %v", err)
	}
	if response != "chat still succeeds" {
		t.Fatalf("response = %q", response)
	}
	if al.outcomeExtractor != nil {
		t.Fatal("expected outcome extractor to stay disabled when runtime bootstrap fails")
	}
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}
	if got := defaultAgent.ContextBuilder.GetInjectedLessons(); len(got) != 0 {
		t.Fatalf("injected lessons = %v, want none", got)
	}
	if len(provider.messages) == 0 || len(provider.messages[0]) == 0 {
		t.Fatal("expected captured prompt messages")
	}
	if strings.Contains(provider.messages[0][0].Content, "## Past Learnings (use these to avoid repeating mistakes)") {
		t.Fatal("prompt unexpectedly contained injected learnings")
	}
	traceRecords := readTraceRecords(t, workspace+"/mind/traces")
	if len(traceRecords) != 1 {
		t.Fatalf("trace records = %d, want 1", len(traceRecords))
	}
	if got := traceRecords[0].InjectedLearningIDs; len(got) != 0 {
		t.Fatalf("resilience trace injected lessons = %v, want none", got)
	}
}
