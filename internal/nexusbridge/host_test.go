package nexusbridge

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	protocol "github.com/uvwt/agentdock-protocol"
	"github.com/uvwt/agentdock/internal/publicartifacts"
)

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
