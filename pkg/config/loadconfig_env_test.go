package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression suite for the LoadConfig early-return bug: when the config file
// did not exist, LoadConfig returned DefaultConfig before reaching env.Parse,
// so no PICOCLAW_* environment variable had any effect in that scenario.

func TestLoadConfig_MissingFileStillAppliesEnv(t *testing.T) {
	t.Setenv("PICOCLAW_AGENTS_DEFAULTS_WORKSPACE", "/tmp/from-env")

	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	cfg, err := LoadConfig(missing)
	if err != nil {
		t.Fatalf("LoadConfig(missing) error = %v", err)
	}
	if got := cfg.Agents.Defaults.Workspace; got != "/tmp/from-env" {
		t.Errorf("Workspace = %q, want env override %q", got, "/tmp/from-env")
	}
}

func TestLoadConfig_MissingFileAppliesEnvAcrossTypes(t *testing.T) {
	t.Setenv("PICOCLAW_AGENTS_DEFAULTS_WORKSPACE", "/tmp/ws-env")
	t.Setenv("PICOCLAW_AGENTS_DEFAULTS_MAX_TOKENS", "12345")
	t.Setenv("PICOCLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE", "false")
	t.Setenv("PICOCLAW_TOOLS_MCP_MAX_INLINE_TEXT_CHARS", "4096")

	missing := filepath.Join(t.TempDir(), "nope.json")
	cfg, err := LoadConfig(missing)
	if err != nil {
		t.Fatalf("LoadConfig(missing) error = %v", err)
	}

	if got := cfg.Agents.Defaults.Workspace; got != "/tmp/ws-env" {
		t.Errorf("Workspace = %q, want %q", got, "/tmp/ws-env")
	}
	if got := cfg.Agents.Defaults.MaxTokens; got != 12345 {
		t.Errorf("MaxTokens = %d, want 12345", got)
	}
	if cfg.Agents.Defaults.RestrictToWorkspace {
		t.Error("RestrictToWorkspace = true, want false from env")
	}
	if got := cfg.Tools.MCP.MaxInlineTextChars; got != 4096 {
		t.Errorf("Tools.MCP.MaxInlineTextChars = %d, want 4096", got)
	}
}

func TestLoadConfig_MissingFileWithoutEnvKeepsDefaults(t *testing.T) {
	// Guard against the fix accidentally zeroing defaults.
	missing := filepath.Join(t.TempDir(), "absent.json")
	cfg, err := LoadConfig(missing)
	if err != nil {
		t.Fatalf("LoadConfig(missing) error = %v", err)
	}

	def := DefaultConfig()
	if got, want := cfg.Agents.Defaults.Workspace, def.Agents.Defaults.Workspace; got != want {
		t.Errorf("Workspace = %q, want default %q", got, want)
	}
	if got, want := cfg.Agents.Defaults.Model, def.Agents.Defaults.Model; got != want {
		t.Errorf("Model = %q, want default %q", got, want)
	}
	// The built-in model list must survive a missing file.
	if len(cfg.ModelList) != len(def.ModelList) {
		t.Errorf("ModelList len = %d, want %d (defaults preserved)", len(cfg.ModelList), len(def.ModelList))
	}
}

func TestLoadConfig_EnvOverridesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agents":{"defaults":{"workspace":"/from-file"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICOCLAW_AGENTS_DEFAULTS_WORKSPACE", "/from-env")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error = %v", err)
	}
	if got := cfg.Agents.Defaults.Workspace; got != "/from-env" {
		t.Errorf("Workspace = %q, want env to win (%q)", got, "/from-env")
	}
}

func TestLoadConfig_ExistingFileStillAppliesEnv(t *testing.T) {
	// Symmetry check: the file path must keep honouring env overrides too.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICOCLAW_AGENTS_DEFAULTS_MAX_TOKENS", "777")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error = %v", err)
	}
	if got := cfg.Agents.Defaults.MaxTokens; got != 777 {
		t.Errorf("MaxTokens = %d, want 777", got)
	}
}

func TestLoadConfig_MissingFileStillValidates(t *testing.T) {
	// Validation must not be skipped just because the file is absent.
	t.Setenv("PICOCLAW_AGENTS_DEFAULTS_MAX_TOKENS", "not-an-int")

	missing := filepath.Join(t.TempDir(), "absent.json")
	if _, err := LoadConfig(missing); err == nil {
		t.Fatal("LoadConfig(missing) = nil error, want env parse failure for bad int")
	}
}

func TestLoadConfig_NonNotExistErrorStillFails(t *testing.T) {
	// A path that exists but cannot be read as a file (a directory) must error
	// out, not silently fall back to defaults.
	dir := t.TempDir()
	if _, err := LoadConfig(dir); err == nil {
		t.Fatal("LoadConfig(directory) = nil error, want read failure")
	}
}
