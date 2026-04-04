package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory"
)

func memoryCmd() {
	if len(os.Args) < 3 {
		memoryHelp()
		return
	}
	switch os.Args[2] {
	case "index":
		memoryIndexCmd()
	default:
		fmt.Printf("Unknown memory command: %s\n", os.Args[2])
		memoryHelp()
	}
}

func memoryIndexCmd() {
	// Parse --force flag
	force := false
	for _, arg := range os.Args[3:] {
		if arg == "--force" || arg == "-f" {
			force = true
		}
	}

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	if !cfg.Memory.Enabled {
		fmt.Println("Memory system is not enabled. Add \"memory\": {\"enabled\": true} to config.json")
		os.Exit(1)
	}

	workspace := cfg.WorkspacePath()

	// Create clients
	embedClient := memory.NewEmbeddingClient(cfg.Memory.GetEmbeddingURL(), cfg.Memory.GetEmbeddingModel())
	qdrantClient := memory.NewQdrantClient(cfg.Memory.GetQdrantURL(), cfg.Memory.GetCollectionName())

	// Ping with progress
	ctx := context.Background()

	fmt.Print("Checking embedding service... ")
	if err := embedClient.Ping(ctx); err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK")

	fmt.Print("Checking Qdrant... ")
	if err := qdrantClient.Ping(ctx); err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK")

	fmt.Print("Ensuring collection... ")
	if err := qdrantClient.EnsureCollection(ctx, 768); err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK")

	// Run indexer
	indexer := memory.NewIndexer(workspace, embedClient, qdrantClient, cfg.Memory.GetMaxChunkTokens(), cfg.Memory.GetIndexDirs())
	fmt.Printf("Indexing directories: %v\n", cfg.Memory.GetIndexDirs())
	if force {
		fmt.Println("Force mode: re-indexing all files")
	}

	start := time.Now()
	newCount, unchangedCount, errCount := indexer.IndexAll(context.Background(), force)
	elapsed := time.Since(start)

	fmt.Printf("\nDone in %s:\n", elapsed.Round(time.Millisecond))
	fmt.Printf("  New/updated: %d chunks\n", newCount)
	fmt.Printf("  Unchanged:   %d chunks\n", unchangedCount)
	if errCount > 0 {
		fmt.Printf("  Errors:      %d chunks\n", errCount)
	}
}

func memoryHelp() {
	fmt.Println("Usage: picoclaw memory <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  index [--force]  Index memory and mind directories into vector database")
}
