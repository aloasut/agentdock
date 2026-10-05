package nexusbridge

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/publicartifacts"
	"github.com/uvwt/agentdock/internal/runtimeapi"
)

// identityPollInterval 是读取配对身份文件的间隔。配对发生在另一个进程，
// 桥接不能只在启动时看一次，否则 Tailcat 拨入会一直停在 503。
var identityPollInterval = time.Second

type runningBridge struct {
	client *Client
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// Host 按配对身份文件拉起或停掉节点桥。身份文件可以在进程启动之后才出现。
type Host struct {
	home      string
	node      NodeAPI
	runtime   runtimeapi.Runtime
	artifacts publicartifacts.Store
	state     *ConnectionState

	mu       sync.Mutex
	running  *runningBridge
	identity Identity
	paired   bool
	mcp      http.Handler
}

func NewHost(home string, node NodeAPI, runtime runtimeapi.Runtime, artifacts publicartifacts.Store, state *ConnectionState) *Host {
	return &Host{home: home, node: node, runtime: runtime, artifacts: artifacts, state: state}
}

// Run 直到 ctx 取消。退出前先停掉桥接并 drain，调用方才能接着关闭 Runtime。
func (h *Host) Run(ctx context.Context) {
	h.reconcile(ctx)
	ticker := time.NewTicker(identityPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.stop()
			return
		case <-ticker.C:
			h.reconcile(ctx)
		}
	}
}

// UseMCP 把本机同一份 Streamable HTTP /mcp 挂到 Tailcat 这一个端口上。
// 不另开隧道端口。连接串仍然不是 MCP 令牌，身份检查留在 handler 里面。
func (h *Host) UseMCP(handler http.Handler) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.mcp = handler
	h.mu.Unlock()
}

// Serve 在 Tailcat 监听上接受节点 WebSocket，并在 /mcp 提供编码代理用的 Streamable HTTP。
// 同一端口也提供 /artifacts/public/，签名规则和局域网 HTTP 相同。
// 还没有配对身份时，节点路径返回 503；/mcp 和公开文件不依赖这次配对。
func (h *Host) Serve(ctx context.Context, ln net.Listener) error {
	server := newTunnelHTTPServer(h.tunnelHandler())
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err := server.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// newTunnelHTTPServer 的读限制与局域网 HTTP 服务器相同。
// 请求体读完后 Go 会清掉读截止时间，长工具调用不会被这 15 秒切断，因此不设 WriteTimeout。
func newTunnelHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func (h *Host) tunnelHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			h.mu.Lock()
			handler := h.mcp
			h.mu.Unlock()
			if handler == nil {
				http.NotFound(w, r)
				return
			}
			handler.ServeHTTP(w, r)
			return
		}
		// 工具结果里的公开文件地址在局域网直连时由本机 HTTP 提供。隧道必须走同一份存储，
		// 否则编码代理拿到 URL 后无法下载。签名本身就是访问能力，不要求节点已经配对。
		if strings.HasPrefix(r.URL.Path, "/artifacts/public/") {
			h.artifacts.ServeHTTP(w, r, "/artifacts/public/")
			return
		}
		client, sessionCtx := h.current()
		if client == nil {
			http.Error(w, "agentdock is not paired", http.StatusServiceUnavailable)
			return
		}
		client.serveInbound(sessionCtx, w, r)
	})
}

func (h *Host) current() (*Client, context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running == nil {
		return nil, nil
	}
	return h.running.client, h.running.ctx
}

func (h *Host) reconcile(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	identity, err := Load(h.home)
	if errors.Is(err, os.ErrNotExist) {
		h.mu.Lock()
		paired := h.paired
		h.paired = false
		h.identity = Identity{}
		h.mu.Unlock()
		if paired {
			h.stop()
		}
		return
	}
	if err != nil {
		// 读失败时保留已经连上的桥。一次损坏的读取不能把在线节点拆掉。
		slog.Warn("NexusDock device identity ignored", "error", err)
		return
	}
	h.mu.Lock()
	same := h.paired && h.identity == identity
	h.mu.Unlock()
	if same {
		return
	}
	h.start(ctx, identity)
}

func (h *Host) start(ctx context.Context, identity Identity) {
	h.stop()
	if ctx.Err() != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	client := NewClient(identity, h.node, h.runtime, h.artifacts, h.state)
	bridge := &runningBridge{client: client, ctx: runCtx, cancel: cancel, done: done}
	h.mu.Lock()
	h.running = bridge
	h.identity = identity
	h.paired = true
	h.mu.Unlock()
	go func() {
		defer close(done)
		client.Run(runCtx)
	}()
}

func (h *Host) stop() {
	h.mu.Lock()
	running := h.running
	h.running = nil
	h.mu.Unlock()
	if running == nil {
		return
	}
	running.cancel()
	<-running.done
}
