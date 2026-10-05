package nexusbridge

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
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

// Serve 在 Tailcat 监听上接受节点 WebSocket。还没有配对身份时返回 503。
func (h *Host) Serve(ctx context.Context, ln net.Listener) error {
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			client, sessionCtx := h.current()
			if client == nil {
				http.Error(w, "agentdock is not paired", http.StatusServiceUnavailable)
				return
			}
			client.serveInbound(sessionCtx, w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
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
