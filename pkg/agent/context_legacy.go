package agent

import (
	"context"
	"fmt"
	"log"
)

type legacyContextAgentKey struct{}

// legacyContextManager wraps our existing ContextBuilder behind the ContextManager interface.
// This is the default implementation — it preserves all existing behavior exactly.
type legacyContextManager struct {
	al *AgentLoop
}

func newLegacyContextManager(al *AgentLoop) *legacyContextManager {
	return &legacyContextManager{al: al}
}

func withLegacyContextAgent(ctx context.Context, agent *AgentInstance) context.Context {
	return context.WithValue(ctx, legacyContextAgentKey{}, agent)
}

func legacyContextAgentFromContext(ctx context.Context) (*AgentInstance, error) {
	agent, ok := ctx.Value(legacyContextAgentKey{}).(*AgentInstance)
	if !ok || agent == nil {
		return nil, fmt.Errorf("context manager requires agent in context")
	}
	return agent, nil
}

func (l *legacyContextManager) Assemble(ctx context.Context, req *AssembleRequest) (*AssembleResponse, error) {
	agent, err := legacyContextAgentFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// Call our existing ContextBuilder.BuildMessages() with the exact same parameters.
	messages := agent.ContextBuilder.BuildMessages(
		ctx,
		req.History,
		req.Summary,
		req.UserMessage,
		req.Media,
		req.Channel,
		req.ChatID,
		req.UserIdentity,
	)
	return &AssembleResponse{Messages: messages}, nil
}

func (l *legacyContextManager) Compact(ctx context.Context, req *CompactRequest) error {
	agent, err := legacyContextAgentFromContext(ctx)
	if err != nil {
		return err
	}

	switch req.Reason {
	case CompactReasonProactive, CompactReasonRetry:
		l.al.forceCompression(agent, req.SessionKey)
	case CompactReasonSummarize:
		// maybeSummarize needs session data — get it.
		// This is called from loop.go where we have access to the agent.
		// We delegate back to the loop's existing maybeSummarize logic.
		log.Printf("[ContextManager] summarize compact requested for session %s", req.SessionKey)
	default:
		return fmt.Errorf("unknown compact reason: %s", req.Reason)
	}
	return nil
}

func (l *legacyContextManager) Ingest(ctx context.Context, req *IngestRequest) error {
	// Delegate to our memory writer.
	if l.al.memoryWriter == nil {
		return nil
	}
	l.al.memoryWriter.IndexConversationTurn(req.UserMsg, req.AgentReply, req.Source)
	return nil
}

func (l *legacyContextManager) Clear(ctx context.Context, sessionKey string) error {
	agent, err := legacyContextAgentFromContext(ctx)
	if err != nil {
		return err
	}

	agent.Sessions.TruncateHistory(sessionKey, 0)
	agent.Sessions.SetSummary(sessionKey, "")
	return agent.Sessions.Save(sessionKey)
}
