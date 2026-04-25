package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcptransport "github.com/mark3labs/mcp-go/client/transport"
)

func TestFileTokenStore_New(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store, err := NewFileTokenStore("test-server")
	if err != nil {
		t.Fatalf("NewFileTokenStore() error = %v", err)
	}

	expectedPath := filepath.Join(tmpDir, ".picoclaw", "tokens", "test-server.json")
	if store.filePath != expectedPath {
		t.Errorf("filePath = %q, want %q", store.filePath, expectedPath)
	}

	dir := filepath.Join(tmpDir, ".picoclaw", "tokens")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("token dir not created: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("token dir perm = %o, want 0700", info.Mode().Perm())
	}
}

func TestFileTokenStore_SaveAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store, err := NewFileTokenStore("save-test")
	if err != nil {
		t.Fatalf("NewFileTokenStore() error = %v", err)
	}

	ctx := context.Background()
	token := &mcptransport.Token{
		AccessToken:  "test-access-token",
		TokenType:    "Bearer",
		RefreshToken: "test-refresh-token",
		ExpiresIn:    3600,
	}

	if err := store.SaveToken(ctx, token); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	fileInfo, err := os.Stat(store.filePath)
	if err != nil {
		t.Fatalf("token file not created: %v", err)
	}
	if fileInfo.Mode().Perm() != 0600 {
		t.Errorf("token file perm = %o, want 0600", fileInfo.Mode().Perm())
	}

	got, err := store.GetToken(ctx)
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if got.AccessToken != token.AccessToken {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, token.AccessToken)
	}
	if got.RefreshToken != token.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, token.RefreshToken)
	}
}

func TestFileTokenStore_GetNoToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store, err := NewFileTokenStore("no-token-test")
	if err != nil {
		t.Fatalf("NewFileTokenStore() error = %v", err)
	}

	_, err = store.GetToken(context.Background())
	if err != mcptransport.ErrNoToken {
		t.Errorf("GetToken() error = %v, want ErrNoToken", err)
	}
}

func TestFileTokenStore_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store1, err := NewFileTokenStore("persist-test")
	if err != nil {
		t.Fatalf("NewFileTokenStore() error = %v", err)
	}

	token := &mcptransport.Token{
		AccessToken: "persisted-token",
		TokenType:   "Bearer",
	}
	if err := store1.SaveToken(context.Background(), token); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	store2, err := NewFileTokenStore("persist-test")
	if err != nil {
		t.Fatalf("NewFileTokenStore() second time error = %v", err)
	}

	got, err := store2.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if got.AccessToken != "persisted-token" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "persisted-token")
	}
}

func TestFileTokenStore_SanitizeName(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store, err := NewFileTokenStore("server/with:special\\chars")
	if err != nil {
		t.Fatalf("NewFileTokenStore() error = %v", err)
	}

	expected := filepath.Join(tmpDir, ".picoclaw", "tokens", "server_with_special_chars.json")
	if store.filePath != expected {
		t.Errorf("filePath = %q, want %q", store.filePath, expected)
	}
}

func TestFileTokenStore_CorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	dir := filepath.Join(tmpDir, ".picoclaw", "tokens")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}

	corruptPath := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{invalid json"), 0600); err != nil {
		t.Fatal(err)
	}

	store, err := NewFileTokenStore("corrupt")
	if err != nil {
		t.Fatalf("NewFileTokenStore() error = %v", err)
	}

	_, err = store.GetToken(context.Background())
	if err != mcptransport.ErrNoToken {
		t.Errorf("GetToken() after corrupt load = %v, want ErrNoToken", err)
	}
}

func TestCallbackServer_Success(t *testing.T) {
	cb := NewCallbackServer(0)

	cb.port = getFreePort(t)

	if err := cb.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer cb.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?code=test-code&state=test-state", cb.port))
		if err != nil {
			t.Logf("callback GET error: %v", err)
			return
		}
		resp.Body.Close()
	}()

	result, err := cb.WaitForCallback(context.Background())
	if err != nil {
		t.Fatalf("WaitForCallback() error = %v", err)
	}
	if result.Code != "test-code" {
		t.Errorf("Code = %q, want %q", result.Code, "test-code")
	}
	if result.State != "test-state" {
		t.Errorf("State = %q, want %q", result.State, "test-state")
	}
}

func TestCallbackServer_ErrorParam(t *testing.T) {
	cb := NewCallbackServer(0)
	cb.port = getFreePort(t)

	if err := cb.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer cb.Close()

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?error=access_denied&error_description=User+denied", cb.port))
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestCallbackServer_Timeout(t *testing.T) {
	cb := NewCallbackServer(0)
	cb.port = getFreePort(t)

	if err := cb.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer cb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := cb.WaitForCallback(ctx)
	if err == nil {
		t.Error("WaitForCallback() should timeout")
	}
}

func TestCallbackServer_MissingCode(t *testing.T) {
	cb := NewCallbackServer(0)
	cb.port = getFreePort(t)

	if err := cb.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer cb.Close()

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?state=abc", cb.port))
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestCallbackServer_DefaultPort(t *testing.T) {
	cb := NewCallbackServer(0)
	if cb.port != 9876 {
		t.Errorf("port = %d, want 9876", cb.port)
	}
}

func TestFileTokenStore_ValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store, err := NewFileTokenStore("json-test")
	if err != nil {
		t.Fatalf("NewFileTokenStore() error = %v", err)
	}

	token := &mcptransport.Token{
		AccessToken: "abc123",
		TokenType:   "Bearer",
		ExpiresIn:   3600,
	}
	if err := store.SaveToken(context.Background(), token); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	data, err := os.ReadFile(store.filePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("token file is not valid JSON: %v", err)
	}
	if parsed["access_token"] != "abc123" {
		t.Errorf("access_token = %v, want abc123", parsed["access_token"])
	}
}

func getFreePort(t *testing.T) int {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ResolveTCPAddr: %v", err)
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		t.Fatalf("ListenTCP: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
