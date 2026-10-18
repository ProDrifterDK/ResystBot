package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSubagentsConfig_AllowRecursiveDelegationJSON pins the safe default: an
// absent field must mean "off" so existing configs keep subagent runs stripped
// of spawn/delegate.
func TestSubagentsConfig_AllowRecursiveDelegationJSON(t *testing.T) {
	var zero SubagentsConfig
	if zero.AllowRecursiveDelegation {
		t.Fatal("AllowRecursiveDelegation must default to false")
	}

	raw := `{"allow_agents":["helper"],"allow_recursive_delegation":true}`
	var parsed SubagentsConfig
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !parsed.AllowRecursiveDelegation {
		t.Error("allow_recursive_delegation:true was not parsed")
	}
	if len(parsed.AllowAgents) != 1 || parsed.AllowAgents[0] != "helper" {
		t.Errorf("allow_agents not parsed alongside the new field: %v", parsed.AllowAgents)
	}
}

// TestSubagentsConfig_AllowRecursiveDelegationOmittedOnMarshal guards the
// omitempty tag so saving a config does not churn files that never used it.
func TestSubagentsConfig_AllowRecursiveDelegationOmittedOnMarshal(t *testing.T) {
	out, err := json.Marshal(SubagentsConfig{AllowAgents: []string{"helper"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "allow_recursive_delegation") {
		t.Errorf("false value should be omitted, got %s", out)
	}

	out, err = json.Marshal(SubagentsConfig{AllowRecursiveDelegation: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"allow_recursive_delegation":true`) {
		t.Errorf("true value should be emitted, got %s", out)
	}
}

// TestSubagentsConfig_AllowPrivilegedToolsJSON pins the safe default: an absent
// field must mean "off", so existing configs keep subagent runs stripped of the
// capability-expanding tools.
func TestSubagentsConfig_AllowPrivilegedToolsJSON(t *testing.T) {
	var zero SubagentsConfig
	if zero.AllowPrivilegedTools {
		t.Fatal("AllowPrivilegedTools must default to false")
	}

	raw := `{"allow_agents":["helper"],"allow_privileged_tools":true}`
	var parsed SubagentsConfig
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !parsed.AllowPrivilegedTools {
		t.Error("allow_privileged_tools:true was not parsed")
	}
	// The two opt-ins are independent: parsing one must not imply the other.
	if parsed.AllowRecursiveDelegation {
		t.Error("allow_privileged_tools must not imply allow_recursive_delegation")
	}
}

// TestSubagentsConfig_AllowPrivilegedToolsOmittedOnMarshal guards the omitempty
// tag so saving a config does not churn files that never used it.
func TestSubagentsConfig_AllowPrivilegedToolsOmittedOnMarshal(t *testing.T) {
	out, err := json.Marshal(SubagentsConfig{AllowAgents: []string{"helper"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "allow_privileged_tools") {
		t.Errorf("false value should be omitted, got %s", out)
	}

	out, err = json.Marshal(SubagentsConfig{AllowPrivilegedTools: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"allow_privileged_tools":true`) {
		t.Errorf("true value should be emitted, got %s", out)
	}
}
