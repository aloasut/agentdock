package nexusbridge

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	protocol "github.com/uvwt/agentdock-protocol"
	"github.com/uvwt/agentdock/internal/publicartifacts"
)

func TestTunnelMCPStaysAvailableBeforePairing(t *testing.T) {
	host := NewHost(t.TempDir(), inboundNode{}, nil, publicartifacts.Store{}, &ConnectionState{})
	host.UseMCP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = host.Serve(ctx, ln) }()

	nodeReq, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/v1/nodes/connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	nodeRes, err := http.DefaultClient.Do(nodeReq)
	if err != nil {
		t.Fatal(err)
	}
	nodeBody, _ := io.ReadAll(nodeRes.Body)
	_ = nodeRes.Body.Close()
	if nodeRes.StatusCode != http.StatusServiceUnavailable || !bytes.Contains(nodeBody, []byte("not paired")) {
		t.Fatalf("node status=%d body=%s", nodeRes.StatusCode, nodeBody)
	}

	mcpReq, err := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	mcpReq.Header.Set("Authorization", "Bearer secret")
	mcpRes, err := http.DefaultClient.Do(mcpReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = mcpRes.Body.Close()
	if mcpRes.StatusCode != http.StatusNoContent {
		t.Fatalf("mcp status=%d", mcpRes.StatusCode)
	}
}

func TestTunnelPublicArtifactMatchesLANWithoutPairing(t *testing.T) {
	home := t.TempDir()
	store := publicartifacts.New(home, "", 80)
	published, err := store.PublishBytes(publicartifacts.PublishBytesRequest{
		Filename: "note.txt",
		Data:     []byte("hello-artifact"),
		MimeType: "text/plain",
		BaseURL:  "http://agentdock.tailcat:80",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactURL, err := url.Parse(published.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := NewHost(home, inboundNode{}, nil, store, &ConnectionState{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = host.Serve(ctx, ln) }()

	res, err := http.Get("http://" + ln.Addr().String() + artifactURL.RequestURI())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "hello-artifact" {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("content-type=%s", res.Header.Get("Content-Type"))
	}

	bare, err := http.Get("http://" + ln.Addr().String() + "/artifacts/public/" + published.ArtifactID + "/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	_ = bare.Body.Close()
	if bare.StatusCode != http.StatusNotFound {
		t.Fatalf("unsigned status=%d", bare.StatusCode)
	}
	postReq, err := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+artifactURL.RequestURI(), nil)
	if err != nil {
		t.Fatal(err)
	}
	postRes, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = postRes.Body.Close()
	if postRes.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post status=%d", postRes.StatusCode)
	}
	health, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	healthBody, _ := io.ReadAll(health.Body)
	_ = health.Body.Close()
	if health.StatusCode != http.StatusServiceUnavailable || !bytes.Contains(healthBody, []byte("not paired")) {
		t.Fatalf("health status=%d body=%s", health.StatusCode, healthBody)
	}
}

func TestTunnelHTTPServerMatchesLANLimits(t *testing.T) {
	server := newTunnelHTTPServer(http.NotFoundHandler())
	if server.ReadHeaderTimeout != 10*time.Second || server.ReadTimeout != 15*time.Second || server.IdleTimeout != 60*time.Second || server.WriteTimeout != 0 || server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("header=%s read=%s idle=%s write=%s max=%d", server.ReadHeaderTimeout, server.ReadTimeout, server.IdleTimeout, server.WriteTimeout, server.MaxHeaderBytes)
	}
}

func TestHostAcceptsHelloAfterIdentityAppears(t *testing.T) {
	home := t.TempDir()
	previous := identityPollInterval
	identityPollInterval = 15 * time.Millisecond
	t.Cleanup(func() { identityPollInterval = previous })

	host := NewHost(home, inboundNode{}, nil, publicartifacts.Store{}, &ConnectionState{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go host.Run(ctx)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = host.Serve(ctx, ln) }()

	deadline := time.Now().Add(2 * time.Second)
	sawUnavailable := false
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + ln.Addr().String() + "/v1/nodes/connect")
		if err != nil {
			time.Sleep(15 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode == http.StatusServiceUnavailable && bytes.Contains(body, []byte("not paired")) {
			sawUnavailable = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawUnavailable {
		t.Fatal("unpaired tailcat listener did not return 503")
	}

	if err := Save(home, Identity{
		Version: identityVersion, Endpoint: "https://nexus.example", NodeID: "node-hot", DeviceID: "device-hot", DeviceToken: "token-hot",
	}); err != nil {
		t.Fatal(err)
	}
	socket := dialInbound(t, ln.Addr().String())
	defer socket.Close()
	var hello protocol.Message
	if err := socket.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if hello.Type != protocol.MessageNodeHello || hello.Hello == nil || hello.Hello.DeviceID != "device-hot" {
		t.Fatalf("hello = %#v", hello)
	}
}
