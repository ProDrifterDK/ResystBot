package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// UserIdentity describes the human or external actor for the current turn.
// It is optional; zero values preserve legacy CLI/cron behavior.
type UserIdentity struct {
	SenderID    string
	UserID      string
	Username    string
	DisplayName string
	Role        string
	IsGuest     bool
}

// AssembleRequest is the input to Assemble — everything needed to build the message list.
type AssembleRequest struct {
	SessionKey   string
	History      []providers.Message
	Summary      string
	UserMessage  string
	Media        []string
	Channel      string
	ChatID       string
	UserIdentity UserIdentity
}

// AssembleResponse is the output — the assembled messages ready for the LLM.
type AssembleResponse struct {
	Messages []providers.Message
}

// CompactRequest specifies how/why to compact context.
type CompactRequest struct {
	SessionKey string
	Reason     CompactReason
}

type CompactReason string

const (
	CompactReasonProactive CompactReason = "proactive" // budget exceeded, compress before request
	CompactReasonRetry     CompactReason = "retry"     // context-length error, force compress
	CompactReasonSummarize CompactReason = "summarize" // optional async summarization
)

// IngestRequest carries a conversation turn for indexing.
type IngestRequest struct {
	SessionKey string
	UserMsg    string
	AgentReply string
	Source     string
}

// ContextManager is the pluggable interface for context assembly, compaction, and ingestion.
type ContextManager interface {
	Assemble(ctx context.Context, req *AssembleRequest) (*AssembleResponse, error)
	Compact(ctx context.Context, req *CompactRequest) error
	Ingest(ctx context.Context, req *IngestRequest) error
	Clear(ctx context.Context, sessionKey string) error
}

// Factory registry
type ContextManagerFactory func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error)

var (
	cmRegistryMu sync.RWMutex
	cmRegistry   = map[string]ContextManagerFactory{}
)

func RegisterContextManager(name string, factory ContextManagerFactory) error {
	cmRegistryMu.Lock()
	defer cmRegistryMu.Unlock()
	if _, exists := cmRegistry[name]; exists {
		return fmt.Errorf("context manager %q already registered", name)
	}
	cmRegistry[name] = factory
	return nil
}

func lookupContextManager(name string) (ContextManagerFactory, bool) {
	cmRegistryMu.RLock()
	defer cmRegistryMu.RUnlock()
	f, ok := cmRegistry[name]
	return f, ok
}
