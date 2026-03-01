package mcp

import (
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestParseMCPToolName(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantServer string
		wantTool   string
		wantOK     bool
	}{
		{
			name:       "valid name",
			input:      "mcp__server__tool",
			wantServer: "server",
			wantTool:   "tool",
			wantOK:     true,
		},
		{
			name:       "tool name with underscores",
			input:      "mcp__server__tool__with__underscores",
			wantServer: "server",
			wantTool:   "tool__with__underscores",
			wantOK:     true,
		},
		{
			name:       "missing mcp prefix",
			input:      "foo__server__tool",
			wantServer: "",
			wantTool:   "",
			wantOK:     false,
		},
		{
			name:       "only one separator segment",
			input:      "mcp__server",
			wantServer: "",
			wantTool:   "",
			wantOK:     false,
		},
		{
			name:       "empty string",
			input:      "",
			wantServer: "",
			wantTool:   "",
			wantOK:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, tool, ok := ParseMCPToolName(tc.input)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if server != tc.wantServer {
				t.Errorf("server = %q, want %q", server, tc.wantServer)
			}
			if tool != tc.wantTool {
				t.Errorf("tool = %q, want %q", tool, tc.wantTool)
			}
		})
	}
}

func TestMCPToolBridgeName(t *testing.T) {
	mcpTool := mcpgo.Tool{
		Name: "read_file",
	}
	bridge := NewMCPToolBridge("myserver", mcpTool, &Manager{connections: make(map[string]*ServerConnection)}, 0)
	want := "mcp__myserver__read_file"
	if got := bridge.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestMCPToolBridgeDescription(t *testing.T) {
	mcpTool := mcpgo.Tool{
		Name:        "read_file",
		Description: "Reads a file from disk",
	}
	bridge := NewMCPToolBridge("myserver", mcpTool, &Manager{connections: make(map[string]*ServerConnection)}, 0)
	want := "[MCP:myserver] Reads a file from disk"
	if got := bridge.Description(); got != want {
		t.Errorf("Description() = %q, want %q", got, want)
	}
}

func TestMCPToolBridgeDescriptionEmpty(t *testing.T) {
	mcpTool := mcpgo.Tool{
		Name:        "do_thing",
		Description: "",
	}
	bridge := NewMCPToolBridge("myserver", mcpTool, &Manager{connections: make(map[string]*ServerConnection)}, 0)
	got := bridge.Description()
	wantPrefix := "[MCP:myserver] "
	if len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Errorf("Description() = %q, want prefix %q", got, wantPrefix)
	}
}

func TestMCPToolBridgeParameters(t *testing.T) {
	props := map[string]any{
		"path": map[string]any{"type": "string", "description": "file path"},
	}
	mcpTool := mcpgo.Tool{
		Name:        "read_file",
		Description: "Reads a file",
		InputSchema: mcpgo.ToolInputSchema{
			Type:       "object",
			Properties: props,
			Required:   []string{"path"},
		},
	}
	bridge := NewMCPToolBridge("myserver", mcpTool, &Manager{connections: make(map[string]*ServerConnection)}, time.Second*30)

	params := bridge.Parameters()

	if params["type"] != "object" {
		t.Errorf("type = %v, want %q", params["type"], "object")
	}

	gotProps, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties not a map[string]any, got %T", params["properties"])
	}
	if _, exists := gotProps["path"]; !exists {
		t.Errorf("expected 'path' in properties")
	}

	gotRequired, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("required not []string, got %T", params["required"])
	}
	if len(gotRequired) != 1 || gotRequired[0] != "path" {
		t.Errorf("required = %v, want [path]", gotRequired)
	}
}

func TestBridgeToolsEmpty(t *testing.T) {
	m := &Manager{connections: make(map[string]*ServerConnection)}
	result := BridgeTools(m, nil)
	if len(result) != 0 {
		t.Errorf("BridgeTools with empty manager returned %d tools, want 0", len(result))
	}
}
