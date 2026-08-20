package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/routing"
)

type resolvedMessageTarget struct {
	agent      *AgentInstance
	sessionKey string
	matchedBy  string
}

func (al *AgentLoop) resolveMessageTarget(msg bus.InboundMessage) (*resolvedMessageTarget, error) {
	if al == nil || al.registry == nil {
		return nil, fmt.Errorf("agent registry is unavailable")
	}

	route := al.registry.ResolveRoute(routing.RouteInput{
		Channel:    msg.Channel,
		AccountID:  msg.Metadata["account_id"],
		Peer:       extractPeer(msg),
		ParentPeer: extractParentPeer(msg),
		GuildID:    msg.Metadata["guild_id"],
		TeamID:     msg.Metadata["team_id"],
	})
	agent, ok := al.registry.GetAgent(route.AgentID)
	if !ok {
		agent = al.registry.GetDefaultAgent()
	}
	if agent == nil {
		return nil, fmt.Errorf("no agent is available for session")
	}

	sessionKey := route.SessionKey
	if msg.SessionKey != "" && (strings.HasPrefix(msg.SessionKey, "agent:") || msg.Channel == "telegram") {
		sessionKey = msg.SessionKey
	}
	return &resolvedMessageTarget{agent: agent, sessionKey: sessionKey, matchedBy: route.MatchedBy}, nil
}

func (al *AgentLoop) ResetSession(ctx context.Context, msg bus.InboundMessage, mode ResetMode) (*ResetResult, error) {
	if !mode.Valid() {
		return nil, fmt.Errorf("invalid session reset mode %q", mode)
	}
	target, err := al.resolveMessageTarget(msg)
	if err != nil {
		return nil, err
	}

	release, err := al.sessionOps.acquire(ctx, target.agent.ID+"\x00"+target.sessionKey)
	if err != nil {
		return nil, err
	}
	defer release()

	resetter, ok := al.ContextManager().(SessionResetter)
	if !ok {
		return nil, &SessionResetUnsupportedError{Manager: fmt.Sprintf("%T", al.ContextManager())}
	}
	result, err := resetter.Reset(withLegacyContextAgent(ctx, target.agent), &ResetRequest{
		SessionKey: target.sessionKey,
		Mode:       mode,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("context manager returned an empty reset result")
	}
	result.SessionKey = target.sessionKey
	return result, nil
}

type sessionOperationGate struct {
	token chan struct{}
	refs  int
}

type sessionOperationCoordinator struct {
	mu    sync.Mutex
	gates map[string]*sessionOperationGate
}

func newSessionOperationCoordinator() *sessionOperationCoordinator {
	return &sessionOperationCoordinator{gates: make(map[string]*sessionOperationGate)}
}

func (c *sessionOperationCoordinator) acquire(ctx context.Context, key string) (func(), error) {
	c.mu.Lock()
	gate := c.gates[key]
	if gate == nil {
		gate = &sessionOperationGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		c.gates[key] = gate
	}
	gate.refs++
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.releaseRef(key, gate)
		return nil, ctx.Err()
	case <-gate.token:
		if err := ctx.Err(); err != nil {
			gate.token <- struct{}{}
			c.releaseRef(key, gate)
			return nil, err
		}
		var once sync.Once
		return func() {
			once.Do(func() {
				gate.token <- struct{}{}
				c.releaseRef(key, gate)
			})
		}, nil
	}
}

func (c *sessionOperationCoordinator) releaseRef(key string, gate *sessionOperationGate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	gate.refs--
	if gate.refs == 0 && c.gates[key] == gate {
		delete(c.gates, key)
	}
}
