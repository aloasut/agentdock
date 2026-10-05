package nexusbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/uvwt/agentdock-protocol"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/buildinfo"
	"github.com/uvwt/agentdock/internal/observability"
	"github.com/uvwt/agentdock/internal/publicartifacts"
	"github.com/uvwt/agentdock/internal/runtimeapi"
	"github.com/uvwt/agentdock/internal/secretredact"
)

const (
	maxMessageBytes      = 8 << 20
	invokeDrainTimeout   = 5 * time.Second
	maxReconnectBackoff  = 30 * time.Second
	readyReadTimeout     = 15 * time.Second
	tailcatDialErrorCode = "TAILCAT_NODE_DIAL"
)

// errTailcatDial 表示 Nexus 已经保存了连接串，出站升级会被 409 拒绝。
// 入站会话不受这次失败影响。之后按上限退避探测，连接串清空后才能恢复出站。
var errTailcatDial = errors.New("NexusDock Tailcat dial-in is active")

var inboundUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	// Nexus 的拨号请求没有 Origin，也不能把 Origin 当成身份。
	CheckOrigin: func(*http.Request) bool { return true },
}

// NodeAPI 由 Nexus Bridge 消费方定义，只包含握手和远程 operation 真正需要的节点能力。
type NodeAPI interface {
	ToolNames() []string
	ToolDescriptors() []map[string]any
	UIResources() []protocol.UIResourceCapability
	ToolContractHash() string
	AgentDockLocalContext(context.Context) (map[string]any, error)
	Invoke(context.Context, string, map[string]any) (map[string]any, error)
	ReadAppResource(string) (map[string]any, error)
}

type Client struct {
	identity  Identity
	node      NodeAPI
	runtime   runtimeapi.Runtime
	artifacts publicartifacts.Store
	state     *ConnectionState
	invokeWG  sync.WaitGroup
	// wake 让出站在入站会话全部结束后立刻再探测。缓冲 1 个，避免拨入抖动把探测排成一串。
	wake chan struct{}
}

// liveSession 是一条已经升级的节点 WebSocket。
// 写锁和取消表都在会话上：稳定会话断开后 Nexus 会马上重拨，新旧两条连接会短暂重叠。
// 旧连接的写超时不能挡住新连接的 node.hello，Nexus 只等第一条消息 15 秒。
type liveSession struct {
	socket   *websocket.Conn
	writeMu  sync.Mutex
	cancelMu sync.Mutex
	cancels  map[string]context.CancelFunc
}

func NewClient(identity Identity, node NodeAPI, runtime runtimeapi.Runtime, artifacts publicartifacts.Store, state *ConnectionState) *Client {
	return &Client{
		identity: identity, node: node, runtime: runtime, artifacts: artifacts, state: state,
		wake: make(chan struct{}, 1),
	}
}

func (c *Client) Run(ctx context.Context) {
	defer c.drainInvocations()
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := backoff
		if errors.Is(err, errTailcatDial) {
			// 409 之后不要从 1 秒开始猛连。入站还在时 30 秒探测一次。
			// 入站会话全部结束后会提前叫醒这一轮，清空连接串就能马上恢复出站。
			wait = maxReconnectBackoff
			backoff = maxReconnectBackoff
			slog.Warn("NexusDock Tailcat dial-in is active; outbound connect paused", "retry_in", wait)
		} else {
			slog.Warn("NexusDock connection lost", "error", secretredact.Text(err.Error()), "retry_in", wait)
			if backoff < maxReconnectBackoff {
				backoff *= 2
			}
		}
		timer := time.NewTimer(wait + time.Duration(rand.IntN(500))*time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-c.wake:
			// 入站已经没了。清空连接串后这次探测能马上把出站拉回来；连接串还在则会再拿到 409。
			timer.Stop()
		case <-timer.C:
		}
	}
}

// nudgeOutbound 在入站会话结束后叫醒出站探测。仍有活会话时不叫，避免和拨入重叠着猛连 Nexus。
func (c *Client) nudgeOutbound() {
	if c == nil || c.wake == nil || c.state.Connected() {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) drainInvocations() {
	done := make(chan struct{})
	go func() {
		c.invokeWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(invokeDrainTimeout):
		slog.Warn("NexusDock bridge in-flight invocations drain timeout")
	}
}

// Serve 在 Tailcat 拨进来的 TCP 上接受节点 WebSocket。调用方负责这条监听的生命周期。
func (c *Client) Serve(ctx context.Context, ln net.Listener) error {
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c.serveInbound(ctx, w, r)
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

func (c *Client) serveInbound(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/nodes/connect" {
		http.NotFound(w, r)
		return
	}
	// 隧道上的认证是连接串里的预共享密钥。这里不看 Authorization，也不要求 Origin。
	socket, err := inboundUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// 会话在本地结束时必须关掉 TCP。defer 顺序是先解除 AfterFunc，再关闭连接；
	// 否则 stop 会拆掉关闭回调，Nexus 要一直读到两个心跳的超时才知道这条会话没了。
	// 撑过一个心跳的断开会被马上重拨，没撑过的走退避。
	defer socket.Close()
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(sessionCtx, func() { _ = socket.Close() })
	defer stop()
	if err := c.runSession(sessionCtx, socket); err != nil && ctx.Err() == nil {
		slog.Warn("NexusDock inbound session ended", "error", secretredact.Text(err.Error()))
	}
	c.nudgeOutbound()
}

func (c *Client) connect(ctx context.Context) error {
	endpoint, err := url.Parse(c.identity.Endpoint)
	if err != nil {
		return err
	}
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/nodes/connect"
	header := http.Header{"Authorization": []string{"Bearer " + c.identity.DeviceToken}}
	socket, response, err := websocket.DefaultDialer.DialContext(ctx, endpoint.String(), header)
	if err != nil {
		return dialError(response, err)
	}
	defer socket.Close()
	// gorilla/websocket 的阻塞读取不会自动观察 context；父 Context 取消时主动关闭连接，
	// 让 ReadJSON 立即返回，确保 Bridge 能在 Runtime 关闭前退出。
	stopContextClose := context.AfterFunc(ctx, func() { _ = socket.Close() })
	defer stopContextClose()
	return c.runSession(ctx, socket)
}

func dialError(response *http.Response, err error) error {
	if response == nil {
		return fmt.Errorf("连接 NexusDock: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	_ = response.Body.Close()
	if response.StatusCode == http.StatusConflict && bytes.Contains(body, []byte(tailcatDialErrorCode)) {
		return errTailcatDial
	}
	text := secretredact.Text(strings.TrimSpace(string(body)))
	if text == "" {
		return fmt.Errorf("连接 NexusDock（HTTP %d）: %w", response.StatusCode, err)
	}
	return fmt.Errorf("连接 NexusDock（HTTP %d）: %s: %w", response.StatusCode, text, err)
}

func (c *Client) runSession(ctx context.Context, socket *websocket.Conn) error {
	socket.SetReadLimit(maxMessageBytes)
	session := &liveSession{socket: socket, cancels: make(map[string]context.CancelFunc)}

	tools := c.node.ToolNames()
	descriptors, err := bridgeToolDescriptors(c.node.ToolDescriptors())
	if err != nil {
		return err
	}
	if err := session.write(protocol.Message{
		Type: protocol.MessageNodeHello, ProtocolVersion: protocol.ConnectionProtocolVersion,
		Hello: bridgeHello(c.identity, tools, descriptors, c.node.UIResources(), c.node.ToolContractHash()),
	}); err != nil {
		return err
	}
	// Nexus 等第一条消息的期限是 15 秒。这里用同一期限等 node.ready，避免握手卡住时占着连接。
	_ = socket.SetReadDeadline(time.Now().Add(readyReadTimeout))
	var ready protocol.Message
	if err := socket.ReadJSON(&ready); err != nil {
		return fmt.Errorf("读取 NexusDock 握手响应: %w", err)
	}
	if ready.Type != protocol.MessageNodeReady || ready.ProtocolVersion != protocol.ConnectionProtocolVersion {
		return errors.New("NexusDock 返回了不兼容的节点协议")
	}
	if oauthRuntime, ok := c.runtime.(runtimeapi.NexusOAuthCallbackRuntime); ok {
		if err := oauthRuntime.SetNexusOAuthCallback(ready.PublicURL, c.identity.NodeID); err != nil {
			// Nexus 公网 URL 只影响“通过 Nexus 授权”这个可选路径，不能让 Recall、
			// Runtime 管理等整条节点 Bridge 因一项回调配置失效。
			slog.Warn("NexusDock OAuth callback unavailable", "error", err)
		}
		defer func() {
			if err := oauthRuntime.SetNexusOAuthCallback("", c.identity.NodeID); err != nil {
				slog.Warn("clear NexusDock OAuth callback failed", "error", err)
			}
		}()
	}
	detach := c.state.Attach()
	defer detach()
	slog.Info("NexusDock node connected", "node_id", c.identity.NodeID, "endpoint", c.identity.Endpoint)
	heartbeat := time.Duration(ready.HeartbeatMS) * time.Millisecond
	if heartbeat <= 0 {
		heartbeat = 30 * time.Second
	}
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 读超时与 Nexus 一致：两个心跳。期间没有消息就结束会话，对方才能按断开去重拨或退避。
	readTimeout := 2 * heartbeat
	_ = socket.SetReadDeadline(time.Now().Add(readTimeout))
	go c.heartbeat(connectionCtx, session, heartbeat)
	for {
		var incoming protocol.Message
		if err := socket.ReadJSON(&incoming); err != nil {
			return err
		}
		_ = socket.SetReadDeadline(time.Now().Add(readTimeout))
		switch incoming.Type {
		case protocol.MessageToolInvoke:
			c.invokeWG.Add(1)
			go func() {
				defer c.invokeWG.Done()
				c.invoke(connectionCtx, session, incoming)
			}()
		case protocol.MessageToolCancel:
			session.cancel(incoming.RequestID)
		case protocol.MessageNodeHeartbeat:
		}
	}
}

func bridgeHello(identity Identity, tools []string, descriptors []protocol.ToolDescriptor, uiResources []protocol.UIResourceCapability, toolContractHash string) *protocol.Hello {
	if uiResources == nil {
		// Nexus 拒绝省略或 null。没有界面资源时必须是空数组。
		uiResources = []protocol.UIResourceCapability{}
	}
	return &protocol.Hello{
		DeviceID:           identity.DeviceID,
		Version:            buildinfo.Version,
		ProtocolVersion:    protocol.ConnectionProtocolVersion,
		OS:                 runtime.GOOS,
		Arch:               runtime.GOARCH,
		Capabilities:       append([]string(nil), tools...),
		BridgeCapabilities: []string{protocol.ArtifactReadCapability},
		ToolContractHash:   toolContractHash,
		Tools:              descriptors,
		UIResources:        uiResources,
	}
}

func (c *Client) invoke(parent context.Context, session *liveSession, incoming protocol.Message) {
	ctx, cancel := context.WithCancel(parent)
	ctx = extractBridgeTraceContext(ctx, &incoming)
	session.cancelMu.Lock()
	session.cancels[incoming.RequestID] = cancel
	session.cancelMu.Unlock()
	defer func() {
		recovered := recover()
		cancel()
		session.cancelMu.Lock()
		delete(session.cancels, incoming.RequestID)
		session.cancelMu.Unlock()
		if recovered != nil {
			// Nexus Bridge 是远程 RPC 边界。单次工具 panic 只结束当前请求，
			// 避免把整个 AgentDock 守护进程和其他本地会话一并带崩。
			slog.Error("NexusDock node operation panicked", "request_id", incoming.RequestID, "operation", incoming.Operation, "panic", recovered, "stack", string(debug.Stack()))
			_ = session.write(protocol.Message{
				Type:      protocol.MessageToolError,
				RequestID: incoming.RequestID,
				Error:     &protocol.RemoteError{Code: "NODE_OPERATION_FAILED", Message: "AgentDock node operation failed", Category: "internal"},
			})
		}
	}()

	var result map[string]any
	var err error
	switch incoming.Operation {
	case protocol.OperationRuntimeRequest:
		var request runtimeapi.Request
		if decodeErr := json.Unmarshal(incoming.Arguments, &request); decodeErr != nil {
			err = fmt.Errorf("解析 Runtime 请求: %w", decodeErr)
		} else {
			result, err = c.dispatchRuntimeRequest(ctx, request)
		}
	case protocol.OperationContextLocal:
		result, err = c.node.AgentDockLocalContext(ctx)
	case protocol.OperationToolCall:
		var request struct {
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		}
		if decodeErr := json.Unmarshal(incoming.Arguments, &request); decodeErr != nil {
			err = fmt.Errorf("解析工具请求: %w", decodeErr)
		} else {
			toolCtx := observability.WithSource(ctx, observability.SourceNexus)
			result, err = c.node.Invoke(toolCtx, request.Tool, request.Arguments)
		}
	case protocol.OperationResourceRead:
		var request struct {
			URI string `json:"uri"`
		}
		if decodeErr := json.Unmarshal(incoming.Arguments, &request); decodeErr != nil {
			err = fmt.Errorf("解析 MCP App resource 请求: %w", decodeErr)
		} else {
			result, err = c.node.ReadAppResource(request.URI)
		}
	case protocol.OperationArtifactRead:
		var request struct {
			ArtifactID string `json:"artifact_id"`
			Offset     int64  `json:"offset"`
			MaxBytes   int    `json:"max_bytes"`
		}
		if decodeErr := json.Unmarshal(incoming.Arguments, &request); decodeErr != nil {
			err = fmt.Errorf("解析 Artifact 读取请求: %w", decodeErr)
		} else {
			result, err = c.readArtifactChunk(request.ArtifactID, request.Offset, request.MaxBytes)
		}
	default:
		err = fmt.Errorf("不支持的 NexusDock 节点操作: %s", incoming.Operation)
	}
	if err != nil {
		_ = session.write(protocol.Message{Type: protocol.MessageToolError, RequestID: incoming.RequestID, Error: bridgeError(err)})
		return
	}
	encoded, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		_ = session.write(protocol.Message{Type: protocol.MessageToolError, RequestID: incoming.RequestID, Error: bridgeError(fmt.Errorf("编码节点结果: %w", encodeErr))})
		return
	}
	_ = session.write(protocol.Message{Type: protocol.MessageToolResult, RequestID: incoming.RequestID, Result: encoded})
}

func (c *Client) dispatchRuntimeRequest(ctx context.Context, request runtimeapi.Request) (map[string]any, error) {
	// Nexus Runtime operation 沿用原有 64 KiB Bridge 请求上限；HTTP 各路由仍保留自己的错误语义。
	if len(request.Body) > 64*1024 {
		return nil, &app.ToolError{Code: "INVALID_ARGUMENT", Message: "runtime request body is too large", Category: "validation"}
	}
	return runtimeapi.Dispatch(ctx, c.runtime, request)
}

func bridgeToolDescriptors(descriptors []map[string]any) ([]protocol.ToolDescriptor, error) {
	encoded, err := json.Marshal(descriptors)
	if err != nil {
		return nil, fmt.Errorf("编码 Nexus Bridge 工具契约: %w", err)
	}
	var tools []protocol.ToolDescriptor
	if err := json.Unmarshal(encoded, &tools); err != nil {
		return nil, fmt.Errorf("解析 Nexus Bridge 工具契约: %w", err)
	}
	return tools, nil
}

func (c *Client) heartbeat(ctx context.Context, session *liveSession, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := session.write(protocol.Message{Type: protocol.MessageNodeHeartbeat}); err != nil {
				_ = session.socket.Close()
				return
			}
		}
	}
}

func (s *liveSession) write(outgoing protocol.Message) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.socket.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return s.socket.WriteJSON(outgoing)
}

func (s *liveSession) cancel(requestID string) {
	s.cancelMu.Lock()
	cancel := s.cancels[requestID]
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func bridgeError(err error) *protocol.RemoteError {
	converted := &protocol.RemoteError{Code: "NODE_OPERATION_FAILED", Message: err.Error()}
	var toolErr *app.ToolError
	if errors.As(err, &toolErr) {
		converted.Code = toolErr.Code
		converted.Message = toolErr.Message
		converted.Category = toolErr.Category
		converted.Retryable = toolErr.Retryable
		converted.Details = toolErr.Details
	}
	return converted
}
