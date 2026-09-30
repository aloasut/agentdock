package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/buildinfo"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/mcp"
	"github.com/uvwt/agentdock/internal/publicartifacts"
	"github.com/uvwt/agentdock/internal/runtimeapi"
	"github.com/uvwt/agentdock/internal/startupdiag"
)

func Serve(ctx context.Context, server *mcp.Server, runtime runtimeapi.Runtime, cfg config.Config) error {
	listenStartedAt := time.Now()
	authRequired := cfg.AuthRequired()
	oauthStore := auth.NewOAuthStore()
	if cfg.OAuthEnabled {
		persistentStore, err := auth.NewPersistentOAuthStore(filepath.Join(cfg.AgentDockHome, "oauth", "state-v1.json"), oauthSigningKey())
		if err != nil {
			return fmt.Errorf("initialize OAuth token store: %w", err)
		}
		oauthStore = persistentStore
	}
	mux := http.NewServeMux()
	publicArtifactStore := publicartifacts.New(cfg.AgentDockHome, cfg.OAuthServerURL, cfg.Port)
	if err := publicArtifactStore.EnsureSecret(); err != nil {
		return fmt.Errorf("ensure public artifact secret: %w", err)
	}
	if err := publicArtifactStore.Cleanup(time.Now().UTC()); err != nil {
		return fmt.Errorf("clean public artifacts: %w", err)
	}
	slog.Info("http server configured", "host", cfg.Host, "port", cfg.Port, "auth_required", authRequired, "endpoint", "/mcp")
	mux.HandleFunc("/", statusPageHandler(server, runtime, cfg))
	mux.Handle("/analytics", loopbackOnly(analyticsPageHandler()))
	mux.Handle("/analytics/data", loopbackOnly(analyticsDataHandler(runtime)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		writeJSON(w, map[string]any{"ok": true, "version": buildinfo.Version})
	})
	mux.HandleFunc("/.well-known/mcp.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, serverCard(cfg, r))
	})
	mux.HandleFunc("/.well-known/mcp/server-card.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, serverCard(cfg, r))
	})
	mux.HandleFunc("/artifacts/public/", func(w http.ResponseWriter, r *http.Request) {
		publicArtifactStore.ServeHTTP(w, r, "/artifacts/public/")
	})
	registerOAuthRoutes(mux, cfg, oauthStore)
	registerMCPOAuthCallback(mux, runtime)
	mux.HandleFunc("/context", agentDockContextHandler(server, cfg, oauthStore))
	registerRuntimeAPI(mux, runtime, cfg, oauthStore)
	mux.HandleFunc("/mcp", mcpEndpointHandler(server, cfg, oauthStore))

	listenHosts := cfg.ListenHosts()
	if cfg.Host == config.ListenHostLAN && len(listenHosts) == 1 {
		// lan 模式发现不到私网地址（无网络/仅虚拟回环）时保持可用，但必须让运维看得到降级。
		slog.Warn("lan listen mode found no private network addresses; serving loopback only")
	}
	listeners := make([]net.Listener, 0, len(listenHosts))
	servers := make([]*http.Server, 0, len(listenHosts))
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for _, host := range listenHosts {
		addr := net.JoinHostPort(host, strconv.Itoa(cfg.Port))
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		listeners = append(listeners, listener)
		httpServer := newHTTPServer(addr, loggingMiddleware(mux))
		servers = append(servers, httpServer)
		startupdiag.Log(slog.Default(), "core", "http_listen", listenStartedAt, slog.String("addr", addr))
		slog.Info("http server listening", "addr", addr)
	}
	return serveHTTPListeners(ctx, servers, listeners)
}

// serveHTTPListeners 并行服务全部监听地址；任一地址报错即整体退出，
// 关闭次序与单监听一致：ctx 结束 → 限时 Shutdown → 超时降级 Close。
func serveHTTPListeners(ctx context.Context, servers []*http.Server, listeners []net.Listener) error {
	serveErrs := make(chan error, len(servers))
	for i, server := range servers {
		listener := listeners[i]
		go func() {
			serveErrs <- server.Serve(listener)
		}()
	}
	select {
	case err := <-serveErrs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErrs := make([]error, len(servers))
	var wg sync.WaitGroup
	for i, server := range servers {
		wg.Add(1)
		go func(i int, server *http.Server) {
			defer wg.Done()
			if err := server.Shutdown(shutdownCtx); err != nil {
				shutdownErrs[i] = server.Close()
				return
			}
		}(i, server)
	}
	wg.Wait()
	for i, err := range shutdownErrs {
		if err != nil {
			return fmt.Errorf("shutdown HTTP server %s: %w", servers[i].Addr, err)
		}
	}
	for range servers {
		err := <-serveErrs
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
