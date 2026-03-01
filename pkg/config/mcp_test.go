package config

import (
	"strings"
	"testing"
)

func boolPtr(b bool) *bool {
	return &b
}

func TestMCPServerConfigValidate_StdioValid(t *testing.T) {
	cfg := MCPServerConfig{
		Transport: "stdio",
		Command:   "/usr/bin/myserver",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestMCPServerConfigValidate_StdioMissingCommand(t *testing.T) {
	cfg := MCPServerConfig{
		Transport: "stdio",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("error should mention 'command', got: %v", err)
	}
}

func TestMCPServerConfigValidate_SSEValid(t *testing.T) {
	cfg := MCPServerConfig{
		Transport: "sse",
		URL:       "http://localhost:8080/sse",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestMCPServerConfigValidate_SSEMissingURL(t *testing.T) {
	cfg := MCPServerConfig{
		Transport: "sse",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("error should mention 'url', got: %v", err)
	}
}

func TestMCPServerConfigValidate_InvalidTransport(t *testing.T) {
	cfg := MCPServerConfig{
		Transport: "websocket",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "websocket") {
		t.Errorf("error should mention 'websocket', got: %v", err)
	}
}

func TestMCPServerConfigIsEnabled_Default(t *testing.T) {
	cfg := MCPServerConfig{
		Transport: "stdio",
		Command:   "/usr/bin/myserver",
	}
	if !cfg.IsEnabled() {
		t.Error("expected IsEnabled() = true when Enabled is nil")
	}
}

func TestMCPServerConfigIsEnabled_ExplicitTrue(t *testing.T) {
	cfg := MCPServerConfig{
		Transport: "stdio",
		Command:   "/usr/bin/myserver",
		Enabled:   boolPtr(true),
	}
	if !cfg.IsEnabled() {
		t.Error("expected IsEnabled() = true when Enabled = true")
	}
}

func TestMCPServerConfigIsEnabled_ExplicitFalse(t *testing.T) {
	cfg := MCPServerConfig{
		Transport: "stdio",
		Command:   "/usr/bin/myserver",
		Enabled:   boolPtr(false),
	}
	if cfg.IsEnabled() {
		t.Error("expected IsEnabled() = false when Enabled = false")
	}
}
