package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentModelConfig_UnmarshalString(t *testing.T) {
	var m AgentModelConfig
	if err := json.Unmarshal([]byte(`"gpt-4"`), &m); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	if m.Primary != "gpt-4" {
		t.Errorf("Primary = %q, want 'gpt-4'", m.Primary)
	}
	if m.Fallbacks != nil {
		t.Errorf("Fallbacks = %v, want nil", m.Fallbacks)
	}
}

func TestAgentModelConfig_UnmarshalObject(t *testing.T) {
	var m AgentModelConfig
	data := `{"primary": "claude-opus", "fallbacks": ["gpt-4o-mini", "haiku"]}`
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	if m.Primary != "claude-opus" {
		t.Errorf("Primary = %q, want 'claude-opus'", m.Primary)
	}
	if len(m.Fallbacks) != 2 {
		t.Fatalf("Fallbacks len = %d, want 2", len(m.Fallbacks))
	}
	if m.Fallbacks[0] != "gpt-4o-mini" || m.Fallbacks[1] != "haiku" {
		t.Errorf("Fallbacks = %v", m.Fallbacks)
	}
}

func TestAgentModelConfig_MarshalString(t *testing.T) {
	m := AgentModelConfig{Primary: "gpt-4"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `"gpt-4"` {
		t.Errorf("marshal = %s, want '\"gpt-4\"'", string(data))
	}
}

func TestAgentModelConfig_MarshalObject(t *testing.T) {
	m := AgentModelConfig{Primary: "claude-opus", Fallbacks: []string{"haiku"}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var result map[string]any
	json.Unmarshal(data, &result)
	if result["primary"] != "claude-opus" {
		t.Errorf("primary = %v", result["primary"])
	}
}

func TestAgentConfig_FullParse(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"workspace": "~/.picoclaw/workspace",
				"model": "glm-4.7",
				"max_tokens": 8192,
				"max_tool_iterations": 20
			},
			"list": [
				{
					"id": "sales",
					"default": true,
					"name": "Sales Bot",
					"model": "gpt-4"
				},
				{
					"id": "support",
					"name": "Support Bot",
					"model": {
						"primary": "claude-opus",
						"fallbacks": ["haiku"]
					},
					"subagents": {
						"allow_agents": ["sales"]
					}
				}
			]
		},
		"bindings": [
			{
				"agent_id": "support",
				"match": {
					"channel": "telegram",
					"account_id": "*",
					"peer": {"kind": "direct", "id": "user123"}
				}
			}
		],
		"session": {
			"dm_scope": "per-peer",
			"identity_links": {
				"john": ["telegram:123", "discord:john#1234"]
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(cfg.Agents.List) != 2 {
		t.Fatalf("agents.list len = %d, want 2", len(cfg.Agents.List))
	}

	sales := cfg.Agents.List[0]
	if sales.ID != "sales" || !sales.Default || sales.Name != "Sales Bot" {
		t.Errorf("sales = %+v", sales)
	}
	if sales.Model == nil || sales.Model.Primary != "gpt-4" {
		t.Errorf("sales.Model = %+v", sales.Model)
	}

	support := cfg.Agents.List[1]
	if support.ID != "support" || support.Name != "Support Bot" {
		t.Errorf("support = %+v", support)
	}
	if support.Model == nil || support.Model.Primary != "claude-opus" {
		t.Errorf("support.Model = %+v", support.Model)
	}
	if len(support.Model.Fallbacks) != 1 || support.Model.Fallbacks[0] != "haiku" {
		t.Errorf("support.Model.Fallbacks = %v", support.Model.Fallbacks)
	}
	if support.Subagents == nil || len(support.Subagents.AllowAgents) != 1 {
		t.Errorf("support.Subagents = %+v", support.Subagents)
	}

	if len(cfg.Bindings) != 1 {
		t.Fatalf("bindings len = %d, want 1", len(cfg.Bindings))
	}
	binding := cfg.Bindings[0]
	if binding.AgentID != "support" || binding.Match.Channel != "telegram" {
		t.Errorf("binding = %+v", binding)
	}
	if binding.Match.Peer == nil || binding.Match.Peer.Kind != "direct" || binding.Match.Peer.ID != "user123" {
		t.Errorf("binding.Match.Peer = %+v", binding.Match.Peer)
	}

	if cfg.Session.DMScope != "per-peer" {
		t.Errorf("Session.DMScope = %q", cfg.Session.DMScope)
	}
	if len(cfg.Session.IdentityLinks) != 1 {
		t.Errorf("Session.IdentityLinks = %v", cfg.Session.IdentityLinks)
	}
	links := cfg.Session.IdentityLinks["john"]
	if len(links) != 2 {
		t.Errorf("john links = %v", links)
	}
}

func TestConfig_BackwardCompat_NoAgentsList(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"workspace": "~/.picoclaw/workspace",
				"model": "glm-4.7",
				"max_tokens": 8192,
				"max_tool_iterations": 20
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(cfg.Agents.List) != 0 {
		t.Errorf("agents.list should be empty for backward compat, got %d", len(cfg.Agents.List))
	}
	if len(cfg.Bindings) != 0 {
		t.Errorf("bindings should be empty, got %d", len(cfg.Bindings))
	}
}

// TestDefaultConfig_HeartbeatEnabled verifies heartbeat is enabled by default
func TestDefaultConfig_HeartbeatEnabled(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.Heartbeat.Enabled {
		t.Error("Heartbeat should be enabled by default")
	}
}

// TestDefaultConfig_WorkspacePath verifies workspace path is correctly set
func TestDefaultConfig_WorkspacePath(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Workspace == "" {
		t.Error("Workspace should not be empty")
	}
}

// TestDefaultConfig_Model verifies model is set
func TestDefaultConfig_Model(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Model == "" {
		t.Error("Model should not be empty")
	}
}

// TestDefaultConfig_MaxTokens verifies max tokens has default value
func TestDefaultConfig_MaxTokens(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.MaxTokens == 0 {
		t.Error("MaxTokens should not be zero")
	}
}

// TestDefaultConfig_MaxToolIterations verifies max tool iterations has default value
func TestDefaultConfig_MaxToolIterations(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.MaxToolIterations == 0 {
		t.Error("MaxToolIterations should not be zero")
	}
}

// TestDefaultConfig_Temperature verifies temperature has default value
func TestDefaultConfig_Temperature(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Temperature != nil {
		t.Error("Temperature should be nil when not provided")
	}
}

// TestDefaultConfig_Gateway verifies gateway defaults
func TestDefaultConfig_Gateway(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Gateway.Host != "127.0.0.1" {
		t.Error("Gateway host should have default value")
	}
	if cfg.Gateway.Port == 0 {
		t.Error("Gateway port should have default value")
	}
}

// TestDefaultConfig_Providers verifies provider structure
func TestDefaultConfig_Providers(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Providers.Anthropic.APIKey != "" {
		t.Error("Anthropic API key should be empty by default")
	}
	if cfg.Providers.OpenAI.APIKey != "" {
		t.Error("OpenAI API key should be empty by default")
	}
	if cfg.Providers.OpenRouter.APIKey != "" {
		t.Error("OpenRouter API key should be empty by default")
	}
}

// TestDefaultConfig_Channels verifies channels are disabled by default
func TestDefaultConfig_Channels(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Channels.Telegram.Enabled {
		t.Error("Telegram should be disabled by default")
	}
	if cfg.Channels.Discord.Enabled {
		t.Error("Discord should be disabled by default")
	}
	if cfg.Channels.Slack.Enabled {
		t.Error("Slack should be disabled by default")
	}
}

// TestDefaultConfig_WebTools verifies web tools config
func TestDefaultConfig_WebTools(t *testing.T) {
	cfg := DefaultConfig()

	// Verify web tools defaults
	if cfg.Tools.Web.Brave.MaxResults != 5 {
		t.Error("Expected Brave MaxResults 5, got ", cfg.Tools.Web.Brave.MaxResults)
	}
	if cfg.Tools.Web.Brave.APIKey != "" {
		t.Error("Brave API key should be empty by default")
	}
	if cfg.Tools.Web.DuckDuckGo.MaxResults != 5 {
		t.Error("Expected DuckDuckGo MaxResults 5, got ", cfg.Tools.Web.DuckDuckGo.MaxResults)
	}
}

func TestSaveConfig_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("config file has permission %04o, want 0600", perm)
	}
}

// TestConfig_Complete verifies all config fields are set
func TestConfig_Complete(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Workspace == "" {
		t.Error("Workspace should not be empty")
	}
	if cfg.Agents.Defaults.Model == "" {
		t.Error("Model should not be empty")
	}
	if cfg.Agents.Defaults.Temperature != nil {
		t.Error("Temperature should be nil when not provided")
	}
	if cfg.Agents.Defaults.MaxTokens == 0 {
		t.Error("MaxTokens should not be zero")
	}
	if cfg.Agents.Defaults.MaxToolIterations == 0 {
		t.Error("MaxToolIterations should not be zero")
	}
	if cfg.Gateway.Host != "127.0.0.1" {
		t.Error("Gateway host should have default value")
	}
	if cfg.Gateway.Port == 0 {
		t.Error("Gateway port should have default value")
	}
	if !cfg.Heartbeat.Enabled {
		t.Error("Heartbeat should be enabled by default")
	}
}

func TestDefaultConfig_OpenAIWebSearchEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Providers.OpenAI.WebSearch {
		t.Fatal("DefaultConfig().Providers.OpenAI.WebSearch should be true")
	}
}

func TestDefaultConfig_LearningDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Learning.Enabled {
		t.Fatal("Learning should be disabled by default")
	}
	if cfg.Learning.GetCrossSessionClusteringEnabled() {
		t.Fatal("Cross-session clustering should be disabled by default")
	}
	if cfg.Learning.GetCollectionName() != "resystbot_learnings" {
		t.Fatalf("Learning collection = %q, want %q", cfg.Learning.GetCollectionName(), "resystbot_learnings")
	}
	if cfg.Learning.GetMaxRetrievedLessons() != 3 {
		t.Fatalf("Learning max retrieved lessons = %d, want 3", cfg.Learning.GetMaxRetrievedLessons())
	}
	if cfg.Learning.GetCorrectionSessionTTL() != 10 {
		t.Fatalf("Learning correction session TTL = %d, want 10", cfg.Learning.GetCorrectionSessionTTL())
	}
	if cfg.Learning.GetMaxUserMessageChars() == 0 {
		t.Fatal("Learning max user message chars should be non-zero")
	}
	if cfg.Learning.GetMaxFinalResponseChars() == 0 {
		t.Fatal("Learning max final response chars should be non-zero")
	}
	if cfg.Learning.GetMaxToolArgsChars() == 0 {
		t.Fatal("Learning max tool args chars should be non-zero")
	}
	if cfg.Learning.GetMaxToolResultChars() == 0 {
		t.Fatal("Learning max tool result chars should be non-zero")
	}
	if cfg.Learning.GetMaxErrorMessageChars() == 0 {
		t.Fatal("Learning max error message chars should be non-zero")
	}
	if cfg.Learning.GetMaxLessonFieldChars() == 0 {
		t.Fatal("Learning max lesson field chars should be non-zero")
	}
	if cfg.Learning.GetCollectionName() == cfg.Memory.GetCollectionName() {
		t.Fatalf("Learning collection should differ from memory collection, both are %q", cfg.Learning.GetCollectionName())
	}
}

func TestConfigMarshalJSON_OmitsDefaultDisabledLearning(t *testing.T) {
	cfg := DefaultConfig()

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if strings.Contains(string(data), `"learning"`) {
		t.Fatalf("default disabled learning config should be omitted from JSON: %s", string(data))
	}
}

func TestLoadConfig_LearningDefaultsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{
  "agents": {"defaults": {"workspace": "./workspace", "model": "gpt4", "max_tokens": 8192, "max_tool_iterations": 20}},
  "model_list": [{"model_name": "gpt4", "model": "openai/gpt-5.2", "api_key": "x"}]
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Learning.Enabled {
		t.Fatal("Learning should remain disabled when learning section is absent")
	}
	if cfg.Learning.GetCrossSessionClusteringEnabled() {
		t.Fatal("Cross-session clustering should remain disabled when learning section is absent")
	}
	if cfg.Learning.GetCollectionName() != "resystbot_learnings" {
		t.Fatalf("Learning collection = %q, want %q", cfg.Learning.GetCollectionName(), "resystbot_learnings")
	}
	if cfg.Learning.GetCorrectionSessionTTL() != 10 {
		t.Fatalf("Learning correction session TTL = %d, want 10", cfg.Learning.GetCorrectionSessionTTL())
	}
	if cfg.Learning.GetMaxLessonFieldChars() == 0 {
		t.Fatal("Learning max lesson field chars should remain non-zero when section is absent")
	}
}

func TestLoadConfig_LearningRoundTrip(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	roundTripPath := filepath.Join(dir, "roundtrip.json")
	configJSON := `{
  "agents": {"defaults": {"workspace": "./workspace", "model": "gpt4", "max_tokens": 8192, "max_tool_iterations": 20}},
  "model_list": [{"model_name": "gpt4", "model": "openai/gpt-5.2", "api_key": "x"}],
  "learning": {
    "enabled": true,
	    "cross_session_clustering_enabled": true,
    "qdrant_url": "http://localhost:7333",
    "collection_name": "custom_learnings",
    "embedding_url": "http://localhost:2234/v1",
    "embedding_model": "custom-embed",
    "max_retrieved_lessons": 7,
    "min_confidence_threshold": 0.55,
	    "min_user_message_chars": 42,
	    "dup_similarity_threshold": 0.97,
	    "decay_rate": 0.02,
	    "correction_session_ttl_minutes": 25,
	    "max_user_message_chars": 1111,
	    "max_final_response_chars": 2222,
	    "max_tool_args_chars": 3333,
	    "max_tool_result_chars": 4444,
	    "max_error_message_chars": 5555,
	    "max_lesson_field_chars": 6666
	  }
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if err := SaveConfig(roundTripPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error: %v", err)
	}

	reloaded, err := LoadConfig(roundTripPath)
	if err != nil {
		t.Fatalf("LoadConfig(roundTripPath) error: %v", err)
	}

	if !reloaded.Learning.Enabled {
		t.Fatal("Learning should remain enabled after round-trip")
	}
	if !reloaded.Learning.CrossSessionClustering {
		t.Fatal("Cross-session clustering should remain enabled after round-trip")
	}
	if reloaded.Learning.QdrantURL != "http://localhost:7333" {
		t.Fatalf("Learning.QdrantURL = %q", reloaded.Learning.QdrantURL)
	}
	if reloaded.Learning.CollectionName != "custom_learnings" {
		t.Fatalf("Learning.CollectionName = %q", reloaded.Learning.CollectionName)
	}
	if reloaded.Learning.EmbeddingURL != "http://localhost:2234/v1" {
		t.Fatalf("Learning.EmbeddingURL = %q", reloaded.Learning.EmbeddingURL)
	}
	if reloaded.Learning.EmbeddingModel != "custom-embed" {
		t.Fatalf("Learning.EmbeddingModel = %q", reloaded.Learning.EmbeddingModel)
	}
	if reloaded.Learning.MaxRetrievedLessons != 7 {
		t.Fatalf("Learning.MaxRetrievedLessons = %d", reloaded.Learning.MaxRetrievedLessons)
	}
	if reloaded.Learning.MinConfidenceThreshold != 0.55 {
		t.Fatalf("Learning.MinConfidenceThreshold = %v", reloaded.Learning.MinConfidenceThreshold)
	}
	if reloaded.Learning.MinUserMessageChars != 42 {
		t.Fatalf("Learning.MinUserMessageChars = %d", reloaded.Learning.MinUserMessageChars)
	}
	if reloaded.Learning.DupSimilarityThreshold != 0.97 {
		t.Fatalf("Learning.DupSimilarityThreshold = %v", reloaded.Learning.DupSimilarityThreshold)
	}
	if reloaded.Learning.DecayRate != 0.02 {
		t.Fatalf("Learning.DecayRate = %v", reloaded.Learning.DecayRate)
	}
	if reloaded.Learning.CorrectionSessionTTL != 25 {
		t.Fatalf("Learning.CorrectionSessionTTL = %d", reloaded.Learning.CorrectionSessionTTL)
	}
	if reloaded.Learning.MaxUserMessageChars != 1111 {
		t.Fatalf("Learning.MaxUserMessageChars = %d", reloaded.Learning.MaxUserMessageChars)
	}
	if reloaded.Learning.MaxFinalResponseChars != 2222 {
		t.Fatalf("Learning.MaxFinalResponseChars = %d", reloaded.Learning.MaxFinalResponseChars)
	}
	if reloaded.Learning.MaxToolArgsChars != 3333 {
		t.Fatalf("Learning.MaxToolArgsChars = %d", reloaded.Learning.MaxToolArgsChars)
	}
	if reloaded.Learning.MaxToolResultChars != 4444 {
		t.Fatalf("Learning.MaxToolResultChars = %d", reloaded.Learning.MaxToolResultChars)
	}
	if reloaded.Learning.MaxErrorMessageChars != 5555 {
		t.Fatalf("Learning.MaxErrorMessageChars = %d", reloaded.Learning.MaxErrorMessageChars)
	}
	if reloaded.Learning.MaxLessonFieldChars != 6666 {
		t.Fatalf("Learning.MaxLessonFieldChars = %d", reloaded.Learning.MaxLessonFieldChars)
	}
}

func TestLoadConfig_OpenAIWebSearchDefaultsTrueWhenUnset(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"providers":{"openai":{"api_base":""}}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Providers.OpenAI.WebSearch {
		t.Fatal("OpenAI codex web search should remain true when unset in config file")
	}
}

func TestLoadConfig_OpenAIWebSearchCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"providers":{"openai":{"web_search":false}}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Providers.OpenAI.WebSearch {
		t.Fatal("OpenAI codex web search should be false when disabled in config file")
	}
}

func TestLoadConfig_WebToolsProxy(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	configJSON := `{
  "agents": {"defaults":{"workspace":"./workspace","model":"gpt4","max_tokens":8192,"max_tool_iterations":20}},
  "model_list": [{"model_name":"gpt4","model":"openai/gpt-5.2","api_key":"x"}],
  "tools": {"web":{"proxy":"http://127.0.0.1:7890"}}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Tools.Web.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("Tools.Web.Proxy = %q, want %q", cfg.Tools.Web.Proxy, "http://127.0.0.1:7890")
	}
}
