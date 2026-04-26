package tools

import (
	"context"
	"strings"
	"testing"
)

func TestValidateToolArgs(t *testing.T) {
	tests := []struct {
		name    string
		schema  map[string]any
		args    map[string]any
		wantErr string
	}{
		{
			name: "valid args",
			schema: map[string]any{
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"age":  map[string]any{"type": "integer"},
				},
				"required": []string{"name", "age"},
			},
			args: map[string]any{"name": "alice", "age": float64(30)},
		},
		{
			name: "missing required",
			schema: map[string]any{
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
				"required": []string{"name"},
			},
			args:    map[string]any{},
			wantErr: `missing required property "name"`,
		},
		{
			name: "wrong type",
			schema: map[string]any{
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
			args:    map[string]any{"name": 123},
			wantErr: `property "name": expected string, got int`,
		},
		{
			name: "nil args with required",
			schema: map[string]any{
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
				"required": []string{"query"},
			},
			args:    nil,
			wantErr: `missing required property "query"`,
		},
		{
			name: "nil args no required",
			schema: map[string]any{
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
			},
			args: nil,
		},
		{
			name:   "empty args",
			schema: map[string]any{"properties": map[string]any{}},
			args:   map[string]any{},
		},
		{
			name: "optional field correct type",
			schema: map[string]any{
				"properties": map[string]any{
					"optional": map[string]any{"type": "string"},
				},
			},
			args: map[string]any{"optional": "yes"},
		},
		{
			name: "optional field wrong type",
			schema: map[string]any{
				"properties": map[string]any{
					"optional": map[string]any{"type": "string"},
				},
			},
			args:    map[string]any{"optional": false},
			wantErr: `property "optional": expected string, got bool`,
		},
		{
			name: "integer as float64",
			schema: map[string]any{
				"properties": map[string]any{
					"count": map[string]any{"type": "integer"},
				},
			},
			args: map[string]any{"count": float64(5)},
		},
		{
			name: "actual float for integer",
			schema: map[string]any{
				"properties": map[string]any{
					"count": map[string]any{"type": "integer"},
				},
			},
			args:    map[string]any{"count": 5.5},
			wantErr: `property "count": expected integer, got float64 with fractional part`,
		},
		{
			name: "number accepts float",
			schema: map[string]any{
				"properties": map[string]any{
					"value": map[string]any{"type": "number"},
				},
			},
			args: map[string]any{"value": 1.25},
		},
		{
			name: "number accepts integer",
			schema: map[string]any{
				"properties": map[string]any{
					"value": map[string]any{"type": "number"},
				},
			},
			args: map[string]any{"value": 2},
		},
		{
			name: "boolean valid",
			schema: map[string]any{
				"properties": map[string]any{
					"deliver": map[string]any{"type": "boolean"},
				},
			},
			args: map[string]any{"deliver": true},
		},
		{
			name: "boolean wrong",
			schema: map[string]any{
				"properties": map[string]any{
					"deliver": map[string]any{"type": "boolean"},
				},
			},
			args:    map[string]any{"deliver": "true"},
			wantErr: `property "deliver": expected boolean, got string`,
		},
		{
			name: "required as []any",
			schema: map[string]any{
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
				"required": []any{"query"},
			},
			args: map[string]any{"query": "golang"},
		},
		{
			name: "enum valid []any",
			schema: map[string]any{
				"properties": map[string]any{
					"mode": map[string]any{"type": "string", "enum": []any{"fast", "slow"}},
				},
			},
			args: map[string]any{"mode": "fast"},
		},
		{
			name: "enum invalid []any",
			schema: map[string]any{
				"properties": map[string]any{
					"mode": map[string]any{"type": "string", "enum": []any{"fast", "slow"}},
				},
			},
			args:    map[string]any{"mode": "turbo"},
			wantErr: `property "mode": value turbo is not in enum`,
		},
		{
			name: "enum valid []string",
			schema: map[string]any{
				"properties": map[string]any{
					"action": map[string]any{"type": "string", "enum": []string{"run", "list"}},
				},
			},
			args: map[string]any{"action": "run"},
		},
		{
			name: "enum invalid []string",
			schema: map[string]any{
				"properties": map[string]any{
					"action": map[string]any{"type": "string", "enum": []string{"run", "list"}},
				},
			},
			args:    map[string]any{"action": "kill"},
			wantErr: `property "action": value kill is not in enum`,
		},
		{
			name: "extra unexpected property rejected",
			schema: map[string]any{
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
			args:    map[string]any{"name": "alice", "extra": true},
			wantErr: `unexpected property "extra"`,
		},
		{
			name: "extra property allowed with additionalProperties",
			schema: map[string]any{
				"properties":           map[string]any{"name": map[string]any{"type": "string"}},
				"additionalProperties": true,
			},
			args: map[string]any{"name": "alice", "extra": true},
		},
		{
			name: "nested object valid",
			schema: map[string]any{
				"properties": map[string]any{
					"config": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"enabled": map[string]any{"type": "boolean"},
						},
						"required": []string{"enabled"},
					},
				},
			},
			args: map[string]any{"config": map[string]any{"enabled": true}},
		},
		{
			name: "nested object wrong type",
			schema: map[string]any{
				"properties": map[string]any{
					"config": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"enabled": map[string]any{"type": "boolean"},
						},
					},
				},
			},
			args:    map[string]any{"config": map[string]any{"enabled": "yes"}},
			wantErr: `property "config": property "enabled": expected boolean, got string`,
		},
		{
			name: "array valid",
			schema: map[string]any{
				"properties": map[string]any{
					"items": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
			},
			args: map[string]any{"items": []any{"a", "b"}},
		},
		{
			name: "array wrong element types",
			schema: map[string]any{
				"properties": map[string]any{
					"items": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
			},
			args:    map[string]any{"items": []any{"a", 2}},
			wantErr: `property "items[1]": expected string, got int`,
		},
		{
			name:   "schema with no properties key",
			schema: map[string]any{"type": "object"},
			args:   map[string]any{"anything": true},
		},
		{
			name:   "empty schema",
			schema: map[string]any{},
			args:   map[string]any{"anything": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateToolArgs(tt.schema, tt.args)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("expected error %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestValidateToolArgs_RegistryIntegration(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockRegistryTool{
		name: "validate_demo",
		desc: "validates arguments",
		params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
				"count": map[string]any{"type": "integer"},
			},
			"required": []string{"query"},
		},
		result: SilentResult("ok"),
	})

	result := r.ExecuteWithContext(context.Background(), "validate_demo", map[string]any{
		"query": "golang",
		"count": float64(3),
	}, "", "", nil)
	if result.IsError {
		t.Fatalf("expected valid args to succeed, got %q", result.ForLLM)
	}
	if result.ForLLM != "ok" {
		t.Fatalf("expected tool result 'ok', got %q", result.ForLLM)
	}

	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{
			name:    "missing required",
			args:    map[string]any{"count": float64(2)},
			wantErr: `invalid arguments for tool "validate_demo": missing required property "query"`,
		},
		{
			name:    "wrong type",
			args:    map[string]any{"query": 42},
			wantErr: `invalid arguments for tool "validate_demo": property "query": expected string, got int`,
		},
		{
			name:    "extra property",
			args:    map[string]any{"query": "ok", "extra": true},
			wantErr: `invalid arguments for tool "validate_demo": unexpected property "extra"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := r.ExecuteWithContext(context.Background(), "validate_demo", tt.args, "", "", nil)
			if !result.IsError {
				t.Fatalf("expected error result for %s", tt.name)
			}
			if result.ForLLM != tt.wantErr {
				t.Fatalf("expected error %q, got %q", tt.wantErr, result.ForLLM)
			}
			if result.Err == nil {
				t.Fatal("expected underlying error to be attached")
			}
			if !strings.Contains(result.Err.Error(), "argument validation failed") {
				t.Fatalf("expected wrapped validation error, got %v", result.Err)
			}
		})
	}
}

func TestValidateToolArgs_RealSchemas(t *testing.T) {
	execSchema := NewExecSessionTool(nil).Parameters()
	if err := validateToolArgs(execSchema, map[string]any{
		"action":  "run",
		"command": "echo hi",
		"timeout": float64(5),
	}); err != nil {
		t.Fatalf("exec_session schema should accept valid args: %v", err)
	}
	if err := validateToolArgs(execSchema, map[string]any{"action": "invalid"}); err == nil || err.Error() != `property "action": value invalid is not in enum` {
		t.Fatalf("exec_session schema should reject invalid action enum, got %v", err)
	}

	cronSchema := (&CronTool{}).Parameters()
	if err := validateToolArgs(cronSchema, map[string]any{
		"action":     "add",
		"message":    "remind me",
		"at_seconds": float64(60),
		"deliver":    true,
	}); err != nil {
		t.Fatalf("cron schema should accept valid args: %v", err)
	}
	if err := validateToolArgs(cronSchema, map[string]any{
		"action":     "add",
		"message":    "remind me",
		"at_seconds": float64(60),
		"deliver":    "yes",
	}); err == nil || err.Error() != `property "deliver": expected boolean, got string` {
		t.Fatalf("cron schema should reject invalid boolean type, got %v", err)
	}

	webSearchSchema := (&WebSearchTool{}).Parameters()
	if err := validateToolArgs(webSearchSchema, map[string]any{
		"query": "golang",
		"count": float64(5),
	}); err != nil {
		t.Fatalf("web_search schema should accept valid args: %v", err)
	}
	if err := validateToolArgs(webSearchSchema, map[string]any{
		"query": "golang",
		"count": "five",
	}); err == nil || err.Error() != `property "count": expected integer, got string` {
		t.Fatalf("web_search schema should reject invalid count type, got %v", err)
	}
}
