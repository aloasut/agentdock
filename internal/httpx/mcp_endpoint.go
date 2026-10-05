package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/buildinfo"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/httpx/requestmeta"
	"github.com/uvwt/agentdock/internal/mcp"
)

func agentDockContextHandler(server *mcp.Server, cfg config.Config, oauthStore *auth.OAuthStore) http.HandlerFunc {
	authorizer := auth.Bearer{Token: cfg.AuthToken}
	authRequired := cfg.AuthRequired()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		staticOK := cfg.AuthToken != "" && authorizer.Authorized(r)
		oauthOK := authorizedOAuth(r, cfg, oauthStore)
		if authRequired && !staticOK && !oauthOK {
			setBearerChallenge(w, cfg, r, strings.TrimSpace(r.Header.Get("Authorization")) != "")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		result, err := server.AgentDockContext(ctx)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, result)
	}
}

// MCPHandler 是与本机 /mcp 相同的 Streamable HTTP。
// 隧道上的连接串不能代替这里检查的 Bearer。传入空的 OAuth 库时只认静态访问令牌。
func MCPHandler(server *mcp.Server, cfg config.Config, oauthStore *auth.OAuthStore) http.Handler {
	if oauthStore == nil {
		oauthStore = auth.NewOAuthStore()
	}
	return mcpEndpointHandler(server, cfg, oauthStore)
}

func mcpEndpointHandler(server *mcp.Server, cfg config.Config, oauthStore *auth.OAuthStore) http.HandlerFunc {
	authorizer := auth.Bearer{Token: cfg.AuthToken}
	authRequired := cfg.AuthRequired()
	transport := server.HTTPHandler()
	return func(w http.ResponseWriter, r *http.Request) {
		staticOK := cfg.AuthToken != "" && authorizer.Authorized(r)
		oauthOK := authorizedOAuth(r, cfg, oauthStore)
		if authRequired && !staticOK && !oauthOK {
			setBearerChallenge(w, cfg, r, strings.TrimSpace(r.Header.Get("Authorization")) != "")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && !prepareMCPRequestBody(w, r) {
			return
		}
		ctx := extractMCPTraceContext(r.Context(), r.Header)
		ctx = requestmeta.WithBaseURL(ctx, requestPublicBaseURL(cfg, r))
		transport.ServeHTTP(w, r.WithContext(ctx))
	}
}
func prepareMCPRequestBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcp.MaxRequestBodyBytes))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, fmt.Sprintf("request body exceeds %d bytes", mcp.MaxRequestBodyBytes), http.StatusRequestEntityTooLarge)
			return false
		}
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      nil,
			"error":   map[string]any{"code": -32700, "message": "Parse error", "data": err.Error()},
		})
		return false
	}
	var payload json.RawMessage
	if err := decodeSingleJSON(bytes.NewReader(body), &payload); err != nil {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      nil,
			"error":   map[string]any{"code": -32700, "message": "Parse error", "data": err.Error()},
		})
		return false
	}
	body = repairCancelNotification(body, r.Header.Get(mcpProtocolVersionHeader))
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return true
}
func requestPublicBaseURL(cfg config.Config, r *http.Request) string {
	configured := strings.TrimRight(strings.TrimSpace(cfg.OAuthServerURL), "/")
	if configured != "" {
		return configured
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// 任意客户端都可以带上 X-Forwarded-Proto。直接采信会把公开文件签成对端打不开的 https，
	// nexusdock-mcp 也只改写 http:// 开头的地址。只有直连对端落在可信代理网段时才采用最近一跳。
	if remote := parseRemoteIP(r.RemoteAddr); remote != nil && ipInNetworks(remote, trustedProxyNetworks(cfg.TrustedProxyCIDRs)) {
		if forwarded := strings.ToLower(lastForwardedValue(r.Header.Get("X-Forwarded-Proto"))); forwarded == "http" || forwarded == "https" {
			scheme = forwarded
		}
	}
	return scheme + "://" + host
}

func lastForwardedValue(raw string) string {
	parts := strings.Split(raw, ",")
	return strings.TrimSpace(parts[len(parts)-1])
}
func serverCard(cfg config.Config, r *http.Request) map[string]any {
	issuer := issuerFor(cfg, r)
	authInfo := map[string]any{"type": "none"}
	if cfg.AuthToken != "" {
		authInfo = map[string]any{"type": "bearer", "scheme": "Bearer", "header": "Authorization"}
	}
	if cfg.OAuthEnabled {
		authInfo = map[string]any{"type": "oauth2", "scheme": "Bearer", "header": "Authorization", "authorizationUrl": issuer + "/oauth/authorize", "tokenUrl": issuer + "/oauth/token"}
	}
	return map[string]any{"name": config.ServerName, "title": "AgentDock", "version": buildinfo.Version, "description": "Local coding tools MCP server", "transport": map[string]any{"type": "streamable-http", "url": issuer + "/mcp"}, "auth": authInfo}
}
