package httpx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tailscale/tailcat"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/mcp"
	"github.com/uvwt/agentdock/internal/nexusbridge"
	"github.com/uvwt/agentdock/internal/publicartifacts"
	"tailscale.com/derp/derpserver"
	"tailscale.com/net/stun/stuntest"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/filter"
)

// TestChainFixture 只在 CHAIN_FIXTURE=1 时启动。
// 它拉起真实的 AgentDock MCP、局域网 HTTP 和本机 DERP 上的 Tailcat，
// 用官方 MCP 客户端走这两条入口，然后把连接串交给 Nexus 侧的拨入测试。
func TestChainFixture(t *testing.T) {
	if os.Getenv("CHAIN_FIXTURE") != "1" {
		t.Skip()
	}
	nexusURL := os.Getenv("CHAIN_NEXUS_URL")
	pairCode := os.Getenv("CHAIN_PAIR_CODE")
	outPath := os.Getenv("CHAIN_OUT")
	if nexusURL == "" || pairCode == "" || outPath == "" {
		t.Fatal("CHAIN_NEXUS_URL, CHAIN_PAIR_CODE and CHAIN_OUT are required")
	}

	cfg := testConfig(t)
	notePath := filepath.Join(cfg.AgentDockDefaultDir, "note.txt")
	if err := os.WriteFile(notePath, []byte("chain-note"), 0o600); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(cfg.AgentDockDefaultDir, "tiny.png")
	writeChainPNG(t, imagePath)

	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	mcpServer := mcp.NewServer(runtime, cfg)
	artifacts := publicartifacts.New(cfg.AgentDockHome, "", cfg.Port)
	if err := artifacts.EnsureSecret(); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", MCPHandler(mcpServer, cfg, auth.NewOAuthStore()))
	mux.HandleFunc("/artifacts/public/", func(w http.ResponseWriter, r *http.Request) {
		artifacts.ServeHTTP(w, r, "/artifacts/public/")
	})
	lanListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lanServer := newHTTPServer(lanListener.Addr().String(), mux)
	go func() { _ = lanServer.Serve(lanListener) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lanServer.Shutdown(shutdownCtx)
	})

	derpMap := runLocalDERP(t, t.Logf)
	region := derpMap.Regions[1]
	if region == nil {
		t.Fatal("local DERP has no region 1")
	}
	tailcatServer := &tailcat.Server{
		Logf:           t.Logf,
		Region:         region,
		ServedTCPPorts: []filter.PortRange{{First: 80, Last: 80}},
		ServedUDPPorts: []filter.PortRange{},
	}
	t.Cleanup(func() { tailcatServer.Close() })
	listenCtx, listenCancel := context.WithTimeout(context.Background(), 20*time.Second)
	tunnelListener, err := tailcatServer.Listen(listenCtx, "tcp", ":80")
	listenCancel()
	if err != nil {
		t.Fatal(err)
	}
	warm := &tailcat.Client{Server: tailcatServer.TailcatAddr(), Logf: t.Logf}
	t.Cleanup(func() { warm.Close() })

	hostCtx, hostCancel := context.WithCancel(context.Background())
	t.Cleanup(hostCancel)
	host := nexusbridge.NewHost(cfg.AgentDockHome, mcpServer, runtime, artifacts, &nexusbridge.ConnectionState{})
	host.UseMCP(MCPHandler(mcpServer, cfg, auth.NewOAuthStore()))
	serveErr := make(chan error, 1)
	go func() { serveErr <- host.Serve(hostCtx, tunnelListener) }()
	if err := waitForTailcat(warm); err != nil {
		t.Fatal(err)
	}

	identity, err := nexusbridge.Pair(context.Background(), cfg.AgentDockHome, nexusbridge.PairOptions{
		Endpoint: nexusURL, Code: pairCode, Name: "ChainDock",
	})
	if err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{
		"address":   string(tailcatServer.TailcatAddr()),
		"node_id":   identity.NodeID,
		"device_id": identity.DeviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture ready node=%s", identity.NodeID)
	// 生产环境先登记 Tailcat 拨入，再接受节点连接。出站若抢先升级，会把正在进行的工具调用替换掉。
	// 等到 Nexus 写好 armed 再 Run，出站升级会得到 409，会话只留拨入这一条。
	armed := outPath + ".armed"
	armedDeadline := time.Now().Add(30 * time.Second)
	for {
		if _, statErr := os.Stat(armed); statErr == nil {
			break
		}
		if time.Now().After(armedDeadline) {
			t.Fatal("timed out waiting for nexus to arm the tailcat dialer")
		}
		time.Sleep(50 * time.Millisecond)
	}
	go host.Run(hostCtx)

	problems := walkChainClient(t, "lan", "http://"+lanListener.Addr().String()+"/mcp", nil)
	tunnelTransport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return warm.DialTCPPort(ctx, 80)
		},
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: 0,
		DisableCompression:    true,
	}
	problems = append(problems, walkChainClient(t, "tailcat", "http://agentdock.tailcat/mcp", &http.Client{Transport: tunnelTransport})...)
	for _, problem := range problems {
		t.Errorf("%s", problem)
	}
	walked, err := json.Marshal(map[string]any{"problems": problems})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath+".walked", append(walked, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	select {
	case <-sigCtx.Done():
	case err := <-serveErr:
		if err != nil {
			t.Fatal(err)
		}
	}
}

func waitForTailcat(client *tailcat.Client) error {
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := client.DialTCPPort(ctx, 80)
		cancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		last = err
		time.Sleep(300 * time.Millisecond)
	}
	return last
}

func runLocalDERP(t *testing.T, logf logger.Logf) *tailcfg.DERPMap {
	t.Helper()
	derp := derpserver.New(key.NewNode(), logf)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := derpserver.AddWebSocketSupport(derp, derpserver.Handler(derp))
	httpsrv := httptest.NewUnstartedServer(handler)
	httpsrv.Listener.Close()
	httpsrv.Listener = listener
	httpsrv.Config.ErrorLog = logger.StdLogger(logf)
	httpsrv.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	httpsrv.StartTLS()
	stunAddr, stunCleanup := stuntest.Serve(t)
	t.Cleanup(func() {
		httpsrv.CloseClientConnections()
		httpsrv.Close()
		derp.Close()
		stunCleanup()
		_ = listener.Close()
	})
	port := listener.Addr().(*net.TCPAddr).Port
	return &tailcfg.DERPMap{Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{
		1: {
			RegionID: 1, RegionCode: "test",
			Nodes: []*tailcfg.DERPNode{{
				Name: "t1", RegionID: 1, HostName: "127.0.0.1", IPv4: "127.0.0.1", IPv6: "none",
				STUNPort: stunAddr.Port, DERPPort: port, InsecureForTests: true, STUNTestIP: "127.0.0.1",
			}},
		},
	}}
}

func walkChainClient(t *testing.T, name, endpoint string, client *http.Client) []string {
	t.Helper()
	var problems []string
	note := func(format string, args ...any) {
		problems = append(problems, name+": "+fmt.Sprintf(format, args...))
		t.Logf(name+": "+format, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "coding-agent", Version: "1"}, nil).Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: client,
	}, nil)
	if err != nil {
		note("connect: %v", err)
		return problems
	}
	defer session.Close()

	if _, err := session.ListTools(ctx, nil); err != nil {
		note("list tools: %v", err)
	}
	echo, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "exec_command", Arguments: map[string]any{
		"cmd": "printf chain-echo", "execution_mode": "sync", "timeout_ms": 10000,
	}})
	if err != nil || echo == nil || echo.IsError {
		note("echo: err=%v result=%s", err, toolSummary(echo))
	} else if !textContains(echo, "chain-echo") {
		note("echo text missing: %s", toolSummary(echo))
	}

	view, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "view_image", Arguments: map[string]any{
		"path": "tiny.png", "format": "png",
	}})
	if err != nil || view == nil || view.IsError {
		note("view_image: err=%v result=%s", err, toolSummary(view))
	} else if !hasImage(view, "image/png") {
		note("view_image did not return a png content block: %s", toolSummary(view))
	}

	published, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "file_publish", Arguments: map[string]any{
		"path": "note.txt",
	}})
	if err != nil || published == nil || published.IsError {
		note("file_publish: err=%v result=%s", err, toolSummary(published))
	} else if url := structuredString(published, "url"); url == "" {
		note("file_publish has no url: %s", toolSummary(published))
	} else if body, status, fetchErr := fetchURL(ctx, client, url); fetchErr != nil || status != http.StatusOK || string(body) != "chain-note" {
		note("fetch %s status=%d err=%v body=%q", url, status, fetchErr, body)
	}

	started := time.Now()
	slow, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "exec_command", Arguments: map[string]any{
		"cmd": "sleep 18; printf slow-ok", "execution_mode": "sync", "timeout_ms": 60000,
	}})
	elapsed := time.Since(started)
	if err != nil || slow == nil || slow.IsError || !textContains(slow, "slow-ok") {
		note("sleep 18s after %s: err=%v result=%s", elapsed.Round(time.Millisecond), err, toolSummary(slow))
	}

	marker := filepath.Join(t.TempDir(), name+"-leaked")
	cancelCtx, cancelCall := context.WithCancel(ctx)
	callDone := make(chan error, 1)
	go func() {
		_, callErr := session.CallTool(cancelCtx, &mcpsdk.CallToolParams{Name: "exec_command", Arguments: map[string]any{
			"cmd": "sleep 30; printf leaked > \"$MARKER\"", "env": map[string]any{"MARKER": marker},
			"execution_mode": "sync", "timeout_ms": 60000,
		}})
		callDone <- callErr
	}()
	time.Sleep(time.Second)
	cancelCall()
	select {
	case <-callDone:
	case <-time.After(8 * time.Second):
		note("cancel did not finish the call within 8s")
	}
	time.Sleep(1500 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		note("cancelled command still wrote the marker")
	}
	return problems
}

func fetchURL(ctx context.Context, client *http.Client, rawURL string) ([]byte, int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return body, response.StatusCode, err
	}
	return body, response.StatusCode, nil
}

func hasImage(result *mcpsdk.CallToolResult, mime string) bool {
	if result == nil {
		return false
	}
	for _, content := range result.Content {
		image, ok := content.(*mcpsdk.ImageContent)
		if ok && image.MIMEType == mime && len(image.Data) > 0 {
			return true
		}
	}
	return false
}

func textContains(result *mcpsdk.CallToolResult, needle string) bool {
	if result == nil {
		return false
	}
	for _, content := range result.Content {
		text, ok := content.(*mcpsdk.TextContent)
		if ok && strings.Contains(text.Text, needle) {
			return true
		}
	}
	return strings.Contains(toolSummary(result), needle)
}

func structuredString(result *mcpsdk.CallToolResult, key string) string {
	if result == nil || result.StructuredContent == nil {
		return ""
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(encoded, &payload) != nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}

func toolSummary(result *mcpsdk.CallToolResult) string {
	if result == nil {
		return ""
	}
	encoded, err := json.Marshal(map[string]any{
		"isError": result.IsError, "structured": result.StructuredContent, "content": result.Content,
	})
	if err != nil {
		return err.Error()
	}
	if len(encoded) > 500 {
		encoded = encoded[:500]
	}
	return string(encoded)
}

func writeChainPNG(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}
