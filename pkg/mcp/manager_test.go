package mcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/sipeed/picoclaw/pkg/config"
)

type mockTransport struct{}

func (m *mockTransport) Start(context.Context) error { return nil }

func (m *mockTransport) SendRequest(context.Context, mcptransport.JSONRPCRequest) (*mcptransport.JSONRPCResponse, error) {
	return nil, nil
}

func (m *mockTransport) SendNotification(context.Context, mcpgo.JSONRPCNotification) error {
	return nil
}

func (m *mockTransport) SetNotificationHandler(func(mcpgo.JSONRPCNotification)) {}

func (m *mockTransport) Close() error { return nil }

func (m *mockTransport) GetSessionId() string { return "" }

func TestCallTool_RetriesOnLostSession(t *testing.T) {
	ctx := context.Background()
	var reconnectCalls atomic.Int32
	var initialCalls atomic.Int32
	retriedArgs := make(chan map[string]any, 1)

	manager := &Manager{
		connections: map[string]*ServerConnection{},
		connectFn: func(context.Context, string, config.MCPServerConfig) (*ServerConnection, error) {
			reconnectCalls.Add(1)
			return &ServerConnection{
				Client: mcpclient.NewClient(&mockTransport{}),
				callTool: func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
					gotArgs, ok := req.Params.Arguments.(map[string]any)
					if !ok {
						return nil, errors.New("retry arguments were not map[string]any")
					}
					retriedArgs <- gotArgs
					return mcpgo.NewToolResultText("retried"), nil
				},
				connected: true,
			}, nil
		},
	}

	manager.connections["demo"] = &ServerConnection{
		Name:   "demo",
		Config: config.MCPServerConfig{MaxRetries: 1},
		callTool: func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			initialCalls.Add(1)
			gotArgs, ok := req.Params.Arguments.(map[string]any)
			if !ok {
				return nil, errors.New("initial arguments were not map[string]any")
			}
			gotArgs["mutated"] = true
			return nil, mcptransport.ErrSessionTerminated
		},
		connected: true,
	}

	args := map[string]any{"nested": map[string]any{"count": 1}}
	result, err := manager.CallTool(ctx, "demo", "tool", args)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if got := initialCalls.Load(); got != 1 {
		t.Fatalf("initial call count = %d, want 1", got)
	}
	if got := reconnectCalls.Load(); got != 1 {
		t.Fatalf("reconnect count = %d, want 1", got)
	}
	if len(result.Content) != 1 {
		t.Fatalf("result content length = %d, want 1", len(result.Content))
	}

	select {
	case gotArgs := <-retriedArgs:
		if _, mutated := gotArgs["mutated"]; mutated {
			t.Fatalf("retried args unexpectedly contained mutation: %#v", gotArgs)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retried call args")
	}

	if _, mutated := args["mutated"]; mutated {
		t.Fatalf("original args were mutated: %#v", args)
	}
}

func TestCloneStringAnyMap(t *testing.T) {
	t.Run("nil input returns empty map", func(t *testing.T) {
		got := cloneStringAnyMap(nil)
		if got == nil {
			t.Fatal("cloneStringAnyMap(nil) returned nil")
		}
		if len(got) != 0 {
			t.Fatalf("cloneStringAnyMap(nil) len = %d, want 0", len(got))
		}
	})

	t.Run("empty input returns empty map", func(t *testing.T) {
		got := cloneStringAnyMap(map[string]any{})
		if got == nil {
			t.Fatal("cloneStringAnyMap(empty) returned nil")
		}
		if len(got) != 0 {
			t.Fatalf("cloneStringAnyMap(empty) len = %d, want 0", len(got))
		}
	})

	t.Run("deep copies nested values", func(t *testing.T) {
		original := map[string]any{
			"nested": map[string]any{"count": 1},
			"items":  []any{map[string]any{"name": "first"}},
		}

		clone := cloneStringAnyMap(original)
		if len(clone) != len(original) {
			t.Fatalf("clone len = %d, want %d", len(clone), len(original))
		}

		originalNested := original["nested"].(map[string]any)
		cloneNested := clone["nested"].(map[string]any)
		originalItems := original["items"].([]any)
		cloneItems := clone["items"].([]any)

		originalNested["count"] = 2
		originalItems[0].(map[string]any)["name"] = "changed"

		if cloneNested["count"] != 1 {
			t.Fatalf("clone nested count = %v, want 1", cloneNested["count"])
		}
		if cloneItems[0].(map[string]any)["name"] != "first" {
			t.Fatalf("clone nested slice map = %v, want first", cloneItems[0])
		}
	})
}

func TestReconnect_ConcurrentSafety(t *testing.T) {
	ctx := context.Background()
	var reconnectCalls atomic.Int32
	started := make(chan struct{}, 1)
	allowReconnect := make(chan struct{})
	startGate := make(chan struct{})

	manager := &Manager{
		connections: map[string]*ServerConnection{},
		connectFn: func(context.Context, string, config.MCPServerConfig) (*ServerConnection, error) {
			reconnectCalls.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			<-allowReconnect
			return &ServerConnection{
				Client:    mcpclient.NewClient(&mockTransport{}),
				connected: true,
			}, nil
		},
	}

	manager.connections["demo"] = &ServerConnection{
		Name:      "demo",
		Config:    config.MCPServerConfig{MaxRetries: 1},
		connected: false,
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			errCh <- manager.Reconnect(ctx, "demo")
		}()
	}
	close(startGate)

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect to start")
	}
	close(allowReconnect)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("Reconnect() error = %v", err)
		}
	}

	if got := reconnectCalls.Load(); got != 1 {
		t.Fatalf("reconnect count = %d, want 1", got)
	}
}

func TestIsSessionLostError(t *testing.T) {
	if !isSessionLostError(mcptransport.ErrSessionTerminated) {
		t.Fatal("expected ErrSessionTerminated to be treated as lost session")
	}
	if !isSessionLostError(errors.New("session not found")) {
		t.Fatal("expected session substring to be treated as lost session")
	}
	if isSessionLostError(errors.New("network timeout")) {
		t.Fatal("unexpected lost session classification")
	}
}
