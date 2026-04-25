package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

type CallbackResult struct {
	Code  string
	State string
}

type CallbackServer struct {
	port   int
	result chan CallbackResult
	server *http.Server
}

func NewCallbackServer(port int) *CallbackServer {
	if port <= 0 {
		port = 9876
	}
	return &CallbackServer{
		port:   port,
		result: make(chan CallbackResult, 1),
	}
}

func (cs *CallbackServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cs.handleCallback)
	mux.HandleFunc("/", cs.handleCallback)

	cs.server = &http.Server{
		Handler: mux,
	}

	addr := fmt.Sprintf("127.0.0.1:%d", cs.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot start callback server on %s: %w", addr, err)
	}

	logger.InfoCF("mcp-oauth", "OAuth callback server listening", map[string]any{
		"addr": addr,
	})

	go func() {
		if err := cs.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.WarnCF("mcp-oauth", "Callback server stopped", map[string]any{
				"error": err.Error(),
			})
		}
	}()

	return nil
}

func (cs *CallbackServer) WaitForCallback(ctx context.Context) (CallbackResult, error) {
	timeout := 5 * time.Minute
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case result := <-cs.result:
		logger.InfoCF("mcp-oauth", "Received OAuth callback", map[string]any{
			"has_code": result.Code != "",
		})
		return result, nil
	case <-ctx.Done():
		return CallbackResult{}, fmt.Errorf("timed out waiting for OAuth callback: %w", ctx.Err())
	}
}

func (cs *CallbackServer) Close() error {
	if cs.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return cs.server.Shutdown(ctx)
	}
	return nil
}

func (cs *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errorParam := r.URL.Query().Get("error")

	if errorParam != "" {
		errorDesc := r.URL.Query().Get("error_description")
		http.Error(w, "OAuth error: "+errorParam+": "+errorDesc, http.StatusBadRequest)
		return
	}

	if code == "" {
		http.Error(w, "Missing authorization code", http.StatusBadRequest)
		return
	}

	select {
	case cs.result <- CallbackResult{Code: code, State: state}:
	default:
	}

	fmt.Fprintf(w, "<html><body><h1>Authorization successful!</h1><p>You can close this tab.</p></body></html>")
	fmt.Fprintf(w, "<script>window.close();</script>")
}
