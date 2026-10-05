package nexusbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/uvwt/agentdock-protocol"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/mcp"
	"github.com/uvwt/agentdock/internal/observability"
	"github.com/uvwt/agentdock/internal/publicartifacts"
)

func TestBridgeToolDescriptorsPreservePresentationBinding(t *testing.T) {
	descriptors, err := bridgeToolDescriptors([]map[string]any{
		{
			"name":        "file_edit",
			"inputSchema": map[string]any{"type": "object"},
			"_meta":       map[string]any{"ui": map[string]any{"resourceUri": protocol.FileChangeUIResourceURI}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "file_edit" {
		t.Fatalf("descriptors = %#v", descriptors)
	}
	ui, ok := descriptors[0].Meta["ui"].(map[string]any)
	if !ok || ui["resourceUri"] != protocol.FileChangeUIResourceURI {
		t.Fatalf("presentation meta = %#v", descriptors[0].Meta)
	}
}

func TestBridgeHelloSeparatesToolsFromBridgeCapabilities(t *testing.T) {
	tools := []string{"read_file", "exec_command"}
	hello := bridgeHello(
		Identity{DeviceID: "device_abcdefgh"},
		tools,
		[]protocol.ToolDescriptor{{Name: "read_file"}, {Name: "exec_command"}},
		[]protocol.UIResourceCapability{},
		"sha256:test",
	)
	if !reflect.DeepEqual(hello.Capabilities, tools) {
		t.Fatalf("capabilities = %#v, want tools %#v", hello.Capabilities, tools)
	}
	if len(hello.BridgeCapabilities) != 1 || hello.BridgeCapabilities[0] != protocol.ArtifactReadCapability {
		t.Fatalf("bridge_capabilities = %#v", hello.BridgeCapabilities)
	}
	for _, capability := range hello.Capabilities {
		if capability == protocol.ArtifactReadCapability {
			t.Fatal("Bridge capability leaked into model-facing tool capabilities")
		}
	}
}

func TestBridgeWebSocketInvokeRecoveryAndShutdown(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	helloCh := make(chan protocol.Message, 1)
	responses := make(chan protocol.Message, 2)
	serverErr := make(chan error, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/connect" || r.Header.Get("Authorization") != "Bearer test-device-token" {
			http.Error(w, "invalid bridge request", http.StatusUnauthorized)
			return
		}
		socket, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer socket.Close()

		var hello protocol.Message
		if err := socket.ReadJSON(&hello); err != nil {
			serverErr <- err
			return
		}
		helloCh <- hello
		if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageNodeReady, ProtocolVersion: protocol.ConnectionProtocolVersion, HeartbeatMS: 60_000}); err != nil {
			serverErr <- err
			return
		}

		if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageToolInvoke, RequestID: "unsupported", Operation: "unsupported.operation"}); err != nil {
			serverErr <- err
			return
		}
		var unsupported protocol.Message
		if err := socket.ReadJSON(&unsupported); err != nil {
			serverErr <- err
			return
		}
		responses <- unsupported

		panicArgs := []byte(`{"method":"GET","path":"/internal/runtime/status"}`)
		if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageToolInvoke, RequestID: "panic", Operation: protocol.OperationRuntimeRequest, Arguments: panicArgs}); err != nil {
			serverErr <- err
			return
		}
		var recovered protocol.Message
		if err := socket.ReadJSON(&recovered); err != nil {
			serverErr <- err
			return
		}
		responses <- recovered

		// 保持服务端连接打开，确保测试取消的是 Client Context，而不是依赖服务端主动断链。
		for {
			if _, _, err := socket.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	state := &ConnectionState{}
	client := NewClient(
		Identity{Endpoint: server.URL, NodeID: "node-test", DeviceID: "device-test", DeviceToken: "test-device-token"},
		mcp.NewServer(nil, config.Config{}),
		nil,
		publicartifacts.Store{},
		state,
	)
	done := make(chan struct{})
	go func() {
		client.Run(ctx)
		close(done)
	}()

	select {
	case hello := <-helloCh:
		if hello.Type != protocol.MessageNodeHello || hello.Hello == nil || hello.Hello.DeviceID != "device-test" {
			t.Fatalf("hello = %#v", hello)
		}
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bridge hello")
	}

	for _, requestID := range []string{"unsupported", "panic"} {
		select {
		case response := <-responses:
			if response.Type != protocol.MessageToolError || response.RequestID != requestID || response.Error == nil {
				t.Fatalf("response = %#v", response)
			}
			if requestID == "panic" && (response.Error.Code != "NODE_OPERATION_FAILED" || response.Error.Category != "internal") {
				t.Fatalf("panic error = %#v", response.Error)
			}
		case err := <-serverErr:
			t.Fatal(err)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s response", requestID)
		}
	}
	if !state.Connected() {
		t.Fatal("bridge should remain connected after recovered invoke panic")
	}

	cancel()
	select {
	case <-done:
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not exit after context cancellation")
	}
	if state.Connected() {
		t.Fatal("bridge connection state remained connected after shutdown")
	}
}

func TestBridgeRunReturnsAfterCanceledDialContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient(
		Identity{Endpoint: "http://127.0.0.1:1", DeviceToken: "test-device-token"},
		mcp.NewServer(nil, config.Config{}),
		nil,
		publicartifacts.Store{},
		&ConnectionState{},
	)
	done := make(chan struct{})
	go func() {
		client.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge Run did not return for canceled context")
	}
}

func TestBridgeToolInvokeContinuesTraceContextIntoRuntime(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		AgentDockDefaultDir: filepath.Join(root, "workspace"),
		AgentDockHome:       filepath.Join(root, ".agentdock"),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	node := mcp.NewServer(runtime, cfg)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	responseCh := make(chan protocol.Message, 1)
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer socket.Close()
		var hello protocol.Message
		if err := socket.ReadJSON(&hello); err != nil {
			serverErr <- err
			return
		}
		if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageNodeReady, ProtocolVersion: protocol.ConnectionProtocolVersion, HeartbeatMS: 60_000}); err != nil {
			serverErr <- err
			return
		}
		arguments, _ := json.Marshal(map[string]any{"tool": "agentdock_context", "arguments": map[string]any{}})
		if err := socket.WriteJSON(protocol.Message{
			Type: protocol.MessageToolInvoke, RequestID: "trace-call", Operation: protocol.OperationToolCall, Arguments: arguments,
			Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			Tracestate:  "rojo=00f067aa0ba902b7",
		}); err != nil {
			serverErr <- err
			return
		}
		var response protocol.Message
		if err := socket.ReadJSON(&response); err != nil {
			serverErr <- err
			return
		}
		responseCh <- response
		for {
			if _, _, err := socket.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(
		Identity{Endpoint: server.URL, NodeID: "node-trace", DeviceID: "device-trace", DeviceToken: "trace-token"},
		node, runtime, publicartifacts.Store{}, &ConnectionState{},
	)
	done := make(chan struct{})
	go func() {
		client.Run(ctx)
		close(done)
	}()

	select {
	case response := <-responseCh:
		if response.Type != protocol.MessageToolResult || response.RequestID != "trace-call" {
			t.Fatalf("response = %#v", response)
		}
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for traced bridge response")
	}

	recent, ok := runtime.RuntimeAnalytics()["recent_calls"].([]observability.ExecutionRecord)
	if !ok || len(recent) == 0 {
		t.Fatalf("recent calls = %#v", runtime.RuntimeAnalytics()["recent_calls"])
	}
	record := recent[0]
	if record.Tool != "agentdock_context" || record.Source != observability.SourceNexus {
		t.Fatalf("bridge runtime record = %#v", record)
	}
	if record.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q", record.TraceID)
	}
	if record.SpanID != "" {
		t.Fatalf("runtime span id = %q, want empty without an installed SDK", record.SpanID)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not stop after cancellation")
	}
}

type inboundNode struct{}

func (inboundNode) ToolNames() []string { return []string{"echo"} }
func (inboundNode) ToolDescriptors() []map[string]any {
	return []map[string]any{{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}
}
func (inboundNode) UIResources() []protocol.UIResourceCapability { return nil }
func (inboundNode) ToolContractHash() string                     { return "hash" }
func (inboundNode) AgentDockLocalContext(context.Context) (map[string]any, error) {
	return map[string]any{}, nil
}
func (inboundNode) Invoke(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}
func (inboundNode) ReadAppResource(string) (map[string]any, error) {
	return map[string]any{}, nil
}

func TestTailcatInboundHelloAndOutboundConflict(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	nexus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") == "" {
			t.Errorf("outbound dial missing device token")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error_code":"TAILCAT_NODE_DIAL","error":"dial in"}`))
	}))
	defer nexus.Close()

	state := &ConnectionState{}
	client := NewClient(
		Identity{Endpoint: nexus.URL, NodeID: "node-test", DeviceID: "device-test", DeviceToken: "test-device-token"},
		inboundNode{},
		nil,
		publicartifacts.Store{},
		state,
	)
	serveCtx, serveCancel := context.WithCancel(context.Background())
	defer serveCancel()
	go func() { _ = client.Serve(serveCtx, ln) }()

	dialer := websocket.Dialer{
		HandshakeTimeout: 2 * time.Second,
		NetDialContext: func(context.Context, string, string) (net.Conn, error) {
			return net.Dial("tcp", ln.Addr().String())
		},
	}
	var socket *websocket.Conn
	deadline := time.Now().Add(2 * time.Second)
	for {
		socket, _, err = dialer.Dial("ws://agentdock.tailcat/v1/nodes/connect", nil)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial without origin or authorization: %v", err)
	}
	defer socket.Close()

	var hello protocol.Message
	if err := socket.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(hello.Hello)
	if err != nil {
		t.Fatal(err)
	}
	if hello.Type != protocol.MessageNodeHello || hello.ProtocolVersion != protocol.ConnectionProtocolVersion || hello.Hello == nil || hello.Hello.DeviceID != "device-test" || hello.Hello.ProtocolVersion != protocol.ConnectionProtocolVersion {
		t.Fatalf("hello = %#v", hello)
	}
	if !bytes.Contains(encoded, []byte(`"ui_resources":[]`)) {
		t.Fatalf("hello json = %s", encoded)
	}
	if err := socket.WriteJSON(protocol.Message{
		Type: protocol.MessageNodeReady, ProtocolVersion: protocol.ConnectionProtocolVersion, HeartbeatMS: 30000,
	}); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]any{"tool": "echo", "arguments": map[string]any{}})
	if err := socket.WriteJSON(protocol.Message{
		Type: protocol.MessageToolInvoke, RequestID: "call-1", Operation: protocol.OperationToolCall, Arguments: arguments,
	}); err != nil {
		t.Fatal(err)
	}
	var result protocol.Message
	if err := socket.ReadJSON(&result); err != nil {
		t.Fatal(err)
	}
	if result.Type != protocol.MessageToolResult || result.RequestID != "call-1" || !bytes.Contains(result.Result, []byte(`"ok":true`)) {
		t.Fatalf("result = %#v", result)
	}
	if !state.Connected() {
		t.Fatal("inbound session was not marked connected")
	}

	outCtx, outCancel := context.WithCancel(context.Background())
	defer outCancel()
	go client.Run(outCtx)
	time.Sleep(1200 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Fatalf("outbound attempts during dial-in = %d, want 1", got)
	}
	if !state.Connected() {
		t.Fatal("outbound 409 cleared the inbound session")
	}
}

func TestInboundEndWakesOutboundProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	nexus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error_code":"TAILCAT_NODE_DIAL","error":"dial in"}`))
	}))
	defer nexus.Close()

	state := &ConnectionState{}
	client := NewClient(
		Identity{Endpoint: nexus.URL, NodeID: "node-test", DeviceID: "device-test", DeviceToken: "test-device-token"},
		inboundNode{}, nil, publicartifacts.Store{}, state,
	)
	serveCtx, serveCancel := context.WithCancel(context.Background())
	defer serveCancel()
	go func() { _ = client.Serve(serveCtx, ln) }()

	socket := dialInbound(t, ln.Addr().String())
	var hello protocol.Message
	if err := socket.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if err := socket.WriteJSON(protocol.Message{
		Type: protocol.MessageNodeReady, ProtocolVersion: protocol.ConnectionProtocolVersion, HeartbeatMS: 30000,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !state.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !state.Connected() {
		t.Fatal("inbound session was not marked connected")
	}

	outCtx, outCancel := context.WithCancel(context.Background())
	defer outCancel()
	go client.Run(outCtx)
	deadline = time.Now().Add(2 * time.Second)
	for hits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() < 1 {
		t.Fatal("outbound probe did not start")
	}
	_ = socket.Close()
	deadline = time.Now().Add(2 * time.Second)
	for hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() < 2 {
		t.Fatalf("outbound probes after inbound end = %d, want at least 2", hits.Load())
	}
}

func TestInboundProtocolErrorClosesSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(
		Identity{Endpoint: "http://nexus.example", NodeID: "node-test", DeviceID: "device-test", DeviceToken: "test-device-token"},
		inboundNode{}, nil, publicartifacts.Store{}, &ConnectionState{},
	)
	serveCtx, serveCancel := context.WithCancel(context.Background())
	defer serveCancel()
	go func() { _ = client.Serve(serveCtx, ln) }()

	socket := dialInbound(t, ln.Addr().String())
	defer socket.Close()
	var hello protocol.Message
	if err := socket.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageNodeReady, ProtocolVersion: "0"}); err != nil {
		t.Fatal(err)
	}
	_ = socket.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = socket.ReadMessage()
	if err == nil {
		t.Fatal("inbound session stayed open after rejecting node.ready")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("session was not closed; read timed out")
	}
}

func TestInboundRedialWorksWhilePreviousSessionOpen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	state := &ConnectionState{}
	client := NewClient(
		Identity{Endpoint: "http://nexus.example", NodeID: "node-test", DeviceID: "device-test", DeviceToken: "test-device-token"},
		inboundNode{}, nil, publicartifacts.Store{}, state,
	)
	serveCtx, serveCancel := context.WithCancel(context.Background())
	defer serveCancel()
	go func() { _ = client.Serve(serveCtx, ln) }()

	first := dialInbound(t, ln.Addr().String())
	defer first.Close()
	second := dialInbound(t, ln.Addr().String())
	defer second.Close()
	for _, socket := range []*websocket.Conn{first, second} {
		var hello protocol.Message
		if err := socket.ReadJSON(&hello); err != nil {
			t.Fatal(err)
		}
		if hello.Type != protocol.MessageNodeHello {
			t.Fatalf("hello = %#v", hello)
		}
		if err := socket.WriteJSON(protocol.Message{
			Type: protocol.MessageNodeReady, ProtocolVersion: protocol.ConnectionProtocolVersion, HeartbeatMS: 60_000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	arguments, _ := json.Marshal(map[string]any{"tool": "echo", "arguments": map[string]any{}})
	for _, socket := range []*websocket.Conn{second, first} {
		if err := socket.WriteJSON(protocol.Message{
			Type: protocol.MessageToolInvoke, RequestID: "call-overlap", Operation: protocol.OperationToolCall, Arguments: arguments,
		}); err != nil {
			t.Fatal(err)
		}
		var result protocol.Message
		if err := socket.ReadJSON(&result); err != nil {
			t.Fatal(err)
		}
		if result.Type != protocol.MessageToolResult || !bytes.Contains(result.Result, []byte(`"ok":true`)) {
			t.Fatalf("result = %#v", result)
		}
	}
	if !state.Connected() {
		t.Fatal("overlapping inbound sessions were not marked connected")
	}
}

func dialInbound(t *testing.T, addr string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{
		HandshakeTimeout: 2 * time.Second,
		NetDialContext: func(context.Context, string, string) (net.Conn, error) {
			return net.Dial("tcp", addr)
		},
	}
	deadline := time.Now().Add(2 * time.Second)
	var socket *websocket.Conn
	var err error
	for {
		socket, _, err = dialer.Dial("ws://agentdock.tailcat/v1/nodes/connect", nil)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial without origin or authorization: %v", err)
	}
	return socket
}
