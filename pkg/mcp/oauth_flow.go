package mcp

import (
	"context"
	"fmt"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

func AuthorizeOAuth(ctx context.Context, serverURL string, cfg config.MCPOAuthConfig, serverName string) error {
	tokenStore, err := NewFileTokenStore(serverName)
	if err != nil {
		return fmt.Errorf("cannot create token store: %w", err)
	}

	existingToken, err := tokenStore.GetToken(ctx)
	if err == nil && existingToken != nil && !existingToken.IsExpired() {
		logger.InfoCF("mcp-oauth", "Reusing existing valid token", map[string]any{
			"server": serverName,
		})
		return nil
	}

	if existingToken != nil && existingToken.RefreshToken != "" {
		logger.InfoCF("mcp-oauth", "Token expired, attempting refresh", map[string]any{
			"server": serverName,
		})
		oauthHandler := mcptransport.NewOAuthHandler(mcptransport.OAuthConfig{
			TokenStore: tokenStore,
			HTTPClient: nil,
		})
		oauthHandler.SetBaseURL(serverURL)

		if refreshed, refreshErr := oauthHandler.RefreshToken(ctx, existingToken.RefreshToken); refreshErr == nil {
			logger.InfoCF("mcp-oauth", "Token refreshed successfully", map[string]any{
				"server": serverName,
			})
			_ = refreshed
			return nil
		}

		logger.WarnCF("mcp-oauth", "Token refresh failed, starting full OAuth flow", map[string]any{
			"server": serverName,
		})
	}

	return runOAuthFlow(ctx, serverURL, cfg, serverName, tokenStore)
}

func runOAuthFlow(ctx context.Context, serverURL string, cfg config.MCPOAuthConfig, serverName string, tokenStore *FileTokenStore) error {
	oauthCfg := mcptransport.OAuthConfig{
		TokenStore:  tokenStore,
		RedirectURI: cfg.RedirectURI,
		PKCEEnabled: true,
	}

	if cfg.ClientID != "" {
		oauthCfg.ClientID = cfg.ClientID
	}
	if cfg.ClientSecret != "" {
		oauthCfg.ClientSecret = cfg.ClientSecret
	}
	if cfg.Scopes != "" {
		oauthCfg.Scopes = strings.Fields(cfg.Scopes)
	}

	handler := mcptransport.NewOAuthHandler(oauthCfg)
	handler.SetBaseURL(serverURL)

	if _, err := handler.GetServerMetadata(ctx); err != nil {
		logger.WarnCF("mcp-oauth", "Server metadata discovery failed, proceeding anyway", map[string]any{
			"server": serverName,
			"error":  err.Error(),
		})
	}

	if cfg.ClientID == "" {
		logger.InfoCF("mcp-oauth", "No client ID configured, attempting dynamic registration", map[string]any{
			"server": serverName,
		})
		if err := handler.RegisterClient(ctx, "picoclaw"); err != nil {
			logger.WarnCF("mcp-oauth", "Dynamic registration failed", map[string]any{
				"server": serverName,
				"error":  err.Error(),
			})
		}
	}

	codeVerifier, err := mcptransport.GenerateCodeVerifier()
	if err != nil {
		return fmt.Errorf("cannot generate code verifier: %w", err)
	}
	codeChallenge := mcptransport.GenerateCodeChallenge(codeVerifier)
	state, err := mcptransport.GenerateState()
	if err != nil {
		return fmt.Errorf("cannot generate state: %w", err)
	}
	handler.SetExpectedState(state)

	authURL, err := handler.GetAuthorizationURL(ctx, state, codeChallenge)
	if err != nil {
		return fmt.Errorf("cannot build authorization URL: %w", err)
	}

	callbackPort := cfg.CallbackPort
	if callbackPort <= 0 {
		callbackPort = 9876
	}

	cbServer := NewCallbackServer(callbackPort)
	if err := cbServer.Start(); err != nil {
		return fmt.Errorf("cannot start callback server: %w", err)
	}
	defer cbServer.Close()

	logger.InfoCF("mcp-oauth", "Open this URL to authorize", map[string]any{
		"server": serverName,
		"url":    authURL,
	})

	result, err := cbServer.WaitForCallback(ctx)
	if err != nil {
		return fmt.Errorf("OAuth callback failed: %w", err)
	}

	if err := handler.ProcessAuthorizationResponse(ctx, result.Code, result.State, codeVerifier); err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	logger.InfoCF("mcp-oauth", "OAuth authorization successful", map[string]any{
		"server": serverName,
	})

	return nil
}

func extractOAuthHandler(err error) (*mcptransport.OAuthHandler, bool) {
	if mcpclient.IsOAuthAuthorizationRequiredError(err) {
		handler := mcpclient.GetOAuthHandler(err)
		return handler, true
	}
	return nil, false
}
