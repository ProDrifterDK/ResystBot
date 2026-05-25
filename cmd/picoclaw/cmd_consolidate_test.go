package main

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBootstrapLLMStripsOpenAIProtocolPrefix(t *testing.T) {
	cfg := &config.Config{
		Memory: config.MemoryConfig{
			ConsolidationModel: "ollama-cloud/deepseek-v4-flash",
		},
		ModelList: []config.ModelConfig{
			{
				ModelName: "ollama-cloud/deepseek-v4-flash",
				Model:     "openai/deepseek-v4-flash",
				APIBase:   "https://ollama.com/v1",
				APIKey:    "test-key",
			},
		},
	}

	baseURL, model, apiKey := bootstrapLLM(cfg)

	if baseURL != "https://ollama.com/v1" {
		t.Fatalf("baseURL = %q, want %q", baseURL, "https://ollama.com/v1")
	}
	if model != "deepseek-v4-flash" {
		t.Fatalf("model = %q, want %q", model, "deepseek-v4-flash")
	}
	if apiKey != "test-key" {
		t.Fatalf("apiKey = %q, want %q", apiKey, "test-key")
	}
}

func TestBootstrapLLMStripsOpenRouterProtocolPrefix(t *testing.T) {
	cfg := &config.Config{
		Memory: config.MemoryConfig{
			ConsolidationModel: "openrouter/gemini",
		},
		ModelList: []config.ModelConfig{
			{
				ModelName: "openrouter/gemini",
				Model:     "openrouter/google/gemini-3.1-pro-preview",
				APIKey:    "test-key",
			},
		},
	}

	baseURL, model, _ := bootstrapLLM(cfg)

	if baseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("baseURL = %q, want %q", baseURL, "https://openrouter.ai/api/v1")
	}
	if model != "google/gemini-3.1-pro-preview" {
		t.Fatalf("model = %q, want %q", model, "google/gemini-3.1-pro-preview")
	}
}
