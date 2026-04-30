package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
)

func consolidateCmd() {
	var phaseName string
	var dryRun bool
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--phase=") {
			phaseName = strings.TrimPrefix(arg, "--phase=")
		}
		if arg == "--dry-run" {
			dryRun = true
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	if !cfg.Memory.Enabled {
		fmt.Println("Memory system is not enabled. Set memory.enabled=true in config.json")
		os.Exit(1)
	}

	llmBaseURL, llmModel, llmAPIKey := bootstrapLLM(cfg)

	embedder := memory.NewEmbeddingClient(cfg.Memory.GetEmbeddingURL(), cfg.Memory.GetEmbeddingModel())
	qdrant := memory.NewQdrantClient(cfg.Memory.GetQdrantURL(), cfg.Memory.GetCollectionName())
	llm := memory.NewLLMClient(llmBaseURL, llmModel, llmAPIKey)

	if strings.Contains(cfg.Memory.GetEmbeddingURL(), "127.0.0.1:1234") {
		ensureLMStudioEmbeddings(cfg.Memory.GetEmbeddingModel())
	}

	ctx := context.Background()

	archivePath := cfg.Memory.GetArchivePath()
	if strings.HasPrefix(archivePath, "~/") {
		home, _ := os.UserHomeDir()
		archivePath = filepath.Join(home, archivePath[2:])
	}
	reflectionDir := filepath.Join(cfg.WorkspacePath(), "mind", "reflections")

	deps := &memory.ConsolidationDeps{
		Store:             qdrant,
		Embedder:          embedder,
		LLM:               llm,
		Archiver:          memory.NewArchiveWriter(archivePath),
		Config: memory.ConsolidationConfig{
			SimilarityThreshold: cfg.Memory.GetSimilarityThreshold(),
			PruneScoreThreshold: cfg.Memory.GetPruneScoreThreshold(),
			PruneMinAgeDays:     cfg.Memory.GetPruneMinAgeDays(),
			DecayRate:           cfg.Memory.GetDecayRate(),
		},
		ReflectionDir:      reflectionDir,
		DryRun:             dryRun,
		StoreAvailable:     true,
		EmbedderAvailable:  true,
		LLMAvailable:       true,
		ArchiverAvailable:  true,
	}

	if err := qdrant.Ping(ctx); err != nil {
		log.Printf("[consolidation] Qdrant not reachable: %v", err)
		deps.StoreAvailable = false
	}
	if err := embedder.Ping(ctx); err != nil {
		log.Printf("[consolidation] Embedding service not reachable: %v", err)
		deps.EmbedderAvailable = false
	}

	allPhases := []memory.NamedPhase{
		{Name: "abstract", Fn: memory.PhaseAbstract, Deps: memory.PhaseDeps{Store: true, Embedder: true, LLM: true, Archiver: true}},
		{Name: "strengthen", Fn: memory.PhaseStrengthen, Deps: memory.PhaseDeps{Store: true}},
		{Name: "score", Fn: memory.PhaseScore, Deps: memory.PhaseDeps{Store: true}},
		{Name: "prune", Fn: memory.PhasePrune, Deps: memory.PhaseDeps{Store: true, Archiver: true}},
		{Name: "reflect", Fn: memory.PhaseReflect, Deps: memory.PhaseDeps{Store: true, Embedder: true, LLM: true}},
	}

	phases := allPhases
	if phaseName != "" {
		phases = memory.FilterPhases(allPhases, phaseName)
		if len(phases) == 0 {
			fmt.Printf("Unknown phase: %s\nValid phases: abstract, strengthen, score, prune, reflect\n", phaseName)
			os.Exit(1)
		}
	}

	if dryRun {
		fmt.Println("=== DRY RUN MODE ===")
	}

	start := time.Now()
	stats, err := memory.RunConsolidation(ctx, deps, phases...)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Printf("Consolidation failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Consolidation complete in %s\n", elapsed.Round(time.Millisecond))
	fmt.Println(stats.String())
	if len(stats.Errors) > 0 {
		fmt.Printf("\nWarnings (%d):\n", len(stats.Errors))
		for _, e := range stats.Errors {
			fmt.Printf("  - %s\n", e)
		}
	}
}

func bootstrapLLM(cfg *config.Config) (baseURL, model, apiKey string) {
	lmsModelPath := cfg.Memory.GetConsolidationLMSModelPath()

	if lmsModelPath != "" {
		if ensureLMStudio(lmsModelPath) {
			return "http://127.0.0.1:1234/v1", lmsModelPath, "lm-studio"
		}
		log.Printf("[consolidation] LM Studio unavailable, falling back to OpenRouter")
	}

	modelName := cfg.Memory.GetConsolidationModel()
	for _, m := range cfg.ModelList {
		if m.ModelName == modelName {
			apiBase := "https://openrouter.ai/api/v1"
			if m.APIBase != "" {
				apiBase = m.APIBase
			}
			return apiBase, m.Model, m.APIKey
		}
	}

	return "https://openrouter.ai/api/v1", "openrouter/" + modelName, ""
}

func ensureLMStudio(modelPath string) bool {
	if _, err := exec.LookPath("lms"); err != nil {
		log.Printf("[lms] lms CLI not found: %v", err)
		return false
	}

	out, err := exec.Command("lms", "status").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Running") {
		log.Printf("[lms] server not running, starting...")
		if err := exec.Command("lms", "server", "start").Run(); err != nil {
			log.Printf("[lms] failed to start server: %v", err)
			return false
		}
		time.Sleep(3 * time.Second)
	}

	out, err = exec.Command("lms", "ls", "--loaded").CombinedOutput()
	if err == nil && strings.Contains(string(out), modelPath) {
		return true
	}

	log.Printf("[lms] loading model: %s", modelPath)
	if err := exec.Command("lms", "load", modelPath).Run(); err != nil {
		log.Printf("[lms] failed to load model: %v", err)
		return false
	}

	return true
}

func ensureLMStudioEmbeddings(embeddingModel string) {
	if _, err := exec.LookPath("lms"); err != nil {
		log.Printf("[lms-embed] lms CLI not found, skipping auto-start")
		return
	}

	out, err := exec.Command("lms", "status").CombinedOutput()
	if err == nil && strings.Contains(string(out), "Running") {
		log.Printf("[lms-embed] LM Studio server already running")
		return
	}

	log.Printf("[lms-embed] starting LM Studio server...")
	if err := exec.Command("lms", "server", "start").Run(); err != nil {
		log.Printf("[lms-embed] failed to start server: %v", err)
		return
	}
	time.Sleep(3 * time.Second)

	log.Printf("[lms-embed] loading embedding model: %s", embeddingModel)
	if err := exec.Command("lms", "load", embeddingModel).Run(); err != nil {
		log.Printf("[lms-embed] failed to load embedding model: %v", err)
		return
	}

	log.Printf("[lms-embed] embedding model loaded")
}
