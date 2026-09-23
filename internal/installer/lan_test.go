package installer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHealthURLTranslatesLANListenHost(t *testing.T) {
	// AGENTDOCK_HOST=lan 只影响 Core 监听；本机访问地址始终按回环构造。
	if got := healthURL("lan", 8765); got != "http://127.0.0.1:8765/healthz" {
		t.Fatalf("healthURL(lan) = %q", got)
	}
	if got := healthURL("LAN", 8765); got != "http://127.0.0.1:8765/healthz" {
		t.Fatalf("healthURL(LAN) = %q", got)
	}
	if got := localMCPURL("lan", 8765); got != "http://127.0.0.1:8765/mcp" {
		t.Fatalf("localMCPURL(lan) = %q", got)
	}
}

func TestManifestHostKeepsLoopbackForLANMode(t *testing.T) {
	if got := manifestHost("lan"); got != "127.0.0.1" {
		t.Fatalf("manifestHost(lan) = %q, want 127.0.0.1", got)
	}
	if got := manifestHost("192.168.1.10"); got != "192.168.1.10" {
		t.Fatalf("manifestHost(192.168.1.10) = %q, want unchanged", got)
	}
}

func TestResultFromEnvTranslatesLANHostToLocalMCPURL(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "agentdock.env")
	content := "AGENTDOCK_HOST=lan\nAGENTDOCK_PORT=8765\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	result, err := resultFromEnv(envFile, Request{})
	if err != nil {
		t.Fatalf("resultFromEnv() error = %v", err)
	}
	if result.LocalMCPURL != "http://127.0.0.1:8765/mcp" {
		t.Fatalf("LocalMCPURL = %q, want loopback URL", result.LocalMCPURL)
	}
}
