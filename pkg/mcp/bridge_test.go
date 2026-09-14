package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
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

// TestMCPToolBridgeParametersResolvesRefs guards the MCP -> LLM tool payload boundary.
//
// Real failure this catches: tools whose schema uses $ref/$defs (several MCP servers do,
// e.g. cookie and file-upload params) previously came out of Parameters() with the $ref
// intact but the definitions gone, because the schema was rebuilt field by field and $defs
// was never copied. Lenient providers ignore it; strict consumers reject the entire tool
// list, so the agent turn dies before its first iteration. The invariant asserted here is
// the JSON Schema resolution rule itself, not a copy of the implementation: every in-document
// $ref in the emitted schema must resolve inside that same schema.
func TestMCPToolBridgeParametersResolvesRefs(t *testing.T) {
	mcpTool := mcpgo.Tool{
		Name: "browser_set_cookies",
		InputSchema: mcpgo.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"cookies": map[string]any{
					"type":  "array",
					"items": map[string]any{"$ref": "#/$defs/SetCookieParam"},
				},
			},
			Defs: map[string]any{
				"SetCookieParam": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":  map[string]any{"type": "string"},
						"value": map[string]any{"type": "string"},
					},
					"required": []string{"name", "value"},
				},
			},
		},
	}
	bridge := NewMCPToolBridge("playwright", mcpTool, &Manager{connections: make(map[string]*ServerConnection)}, time.Second*30)

	// Serialize the way the provider payload does: the failure is a wire-format failure.
	raw, err := json.Marshal(bridge.Parameters())
	if err != nil {
		t.Fatalf("Parameters() is not JSON-serializable: %v", err)
	}
	var wire any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("Parameters() round-trip failed: %v", err)
	}

	found, err := checkRefsResolve(wire)
	if err != nil {
		t.Errorf("emitted schema has a dangling $ref, strict consumers reject the tool list: %v", err)
	}
	if found == 0 {
		t.Fatal("emitted schema carried no $ref, so this guard exercised nothing")
	}
}

// TestMCPToolBridgeParametersPreservesAdditionalProperties covers the sibling field that the
// same field-by-field rebuild dropped. It matters locally, not only on the wire: validateToolArgs
// reads additionalProperties to decide whether unknown argument keys are allowed, so losing it
// silently imposed deny-by-default on every MCP tool.
func TestMCPToolBridgeParametersPreservesAdditionalProperties(t *testing.T) {
	mcpTool := mcpgo.Tool{
		Name: "search",
		InputSchema: mcpgo.ToolInputSchema{
			Type:                 "object",
			Properties:           map[string]any{"q": map[string]any{"type": "string"}},
			AdditionalProperties: true,
		},
	}
	bridge := NewMCPToolBridge("scrapling", mcpTool, &Manager{connections: make(map[string]*ServerConnection)}, time.Second*30)

	got, ok := bridge.Parameters()["additionalProperties"]
	if !ok {
		t.Fatal("additionalProperties dropped from the tool schema; validateToolArgs then denies unknown args by default")
	}
	if allowed, isBool := got.(bool); !isBool || !allowed {
		t.Errorf("additionalProperties = %#v, want true", got)
	}
}

// checkRefsResolve walks a decoded schema and fails on any in-document $ref that does not
// resolve within that schema. External and relative refs are out of scope by design.
func checkRefsResolve(schema any) (int, error) {
	var found int
	var walk func(node any) error
	walk = func(node any) error {
		switch v := node.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
				found++
				if _, err := resolveJSONPointer(schema, ref); err != nil {
					return fmt.Errorf("$ref %q: %w", ref, err)
				}
			}
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return found, walk(schema)
}

// resolveJSONPointer resolves a "#/a/b" JSON Pointer against a decoded document.
func resolveJSONPointer(root any, ref string) (any, error) {
	cur := root
	for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("segment %q: parent is %T, not an object", seg, cur)
		}
		cur, ok = obj[seg]
		if !ok {
			return nil, fmt.Errorf("segment %q not present", seg)
		}
	}
	return cur, nil
}
