package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type FileTokenStore struct {
	filePath string
	token    *mcptransport.Token
}

func NewFileTokenStore(serverName string) (*FileTokenStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	dir := filepath.Join(home, ".picoclaw", "tokens")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("cannot create token directory: %w", err)
	}

	safeName := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(serverName)
	fp := filepath.Join(dir, safeName+".json")

	store := &FileTokenStore{filePath: fp}
	if err := store.load(); err != nil {
		logger.DebugCF("mcp-oauth", "No existing token file, starting fresh", map[string]any{
			"server": serverName,
			"error":  err.Error(),
		})
	}

	return store, nil
}

func (s *FileTokenStore) GetToken(_ context.Context) (*mcptransport.Token, error) {
	if s.token == nil {
		return nil, mcptransport.ErrNoToken
	}
	return s.token, nil
}

func (s *FileTokenStore) SaveToken(_ context.Context, token *mcptransport.Token) error {
	s.token = token
	return s.write()
}

func (s *FileTokenStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}

	var token mcptransport.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return fmt.Errorf("invalid token file: %w", err)
	}

	s.token = &token
	return nil
}

func (s *FileTokenStore) write() error {
	data, err := json.MarshalIndent(s.token, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot marshal token: %w", err)
	}

	if err := os.WriteFile(s.filePath, data, 0600); err != nil {
		return fmt.Errorf("cannot write token file: %w", err)
	}

	return nil
}
