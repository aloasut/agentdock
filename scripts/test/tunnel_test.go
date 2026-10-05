package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuickTunnelParsingStaysInRuntime(t *testing.T) {
	const marker = "Your quick Tunnel has been created! Visit it at"
	tests := []struct {
		path      string
		wantCount int
	}{
		// Installer no longer waits for or parses public readiness; it starts Tunnel asynchronously.
		{path: "../install/install.ps1", wantCount: 0},
		// Runtime remains the single authority for Quick URL parsing and requires the success marker.
		{path: "../../internal/desktopruntime/quick_tunnel_log.go", wantCount: 1},
	}

	for _, tt := range tests {
		t.Run(filepath.Base(tt.path), func(t *testing.T) {
			data, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			if got := strings.Count(string(data), marker); got != tt.wantCount {
				t.Fatalf("%s Quick Tunnel parser marker count = %d, want %d", tt.path, got, tt.wantCount)
			}
		})
	}
}

func TestWindowsTunnelLifecycleTestsIsolateAgentDockHome(t *testing.T) {
	for _, name := range []string{
		"test-windows-quick-tunnel-lifecycle.ps1",
		"test-windows-named-tunnel-lifecycle.ps1",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			content := string(data)
			for _, want := range []string{
				"$oldHome = $env:AGENTDOCK_HOME",
				"$oldDefaultDir = $env:AGENTDOCK_DEFAULT_DIR",
				"$env:AGENTDOCK_HOME = Join-Path $root '.agentdock'",
				"$env:AGENTDOCK_DEFAULT_DIR = Join-Path $root 'workspace'",
				"$env:AGENTDOCK_HOME = $oldHome",
				"$env:AGENTDOCK_DEFAULT_DIR = $oldDefaultDir",
			} {
				if !strings.Contains(content, want) {
					t.Fatalf("%s must isolate the lifecycle fixture from the developer's AgentDock state; missing %q", name, want)
				}
			}
		})
	}
}

func TestDesktopControlSurfacesKeepQuickTunnelAndTailcatSeparate(t *testing.T) {
	checks := map[string][]string{
		filepath.Join("..", "..", "desktop", "windows", "winui", "SettingsPage.xaml.cs"): {
			"TemporaryTunnelButton_Click",
			"RegenerateQuickTunnelAsync",
			`SetTunnelModeAsync("quick", "", "")`,
			"BuildTailcatAccessSection()",
			"await RefreshAsync()",
		},
		filepath.Join("..", "..", "desktop", "windows", "winui", "TailcatAccessSection.cs"): {
			"ResetTailcatButton_Click",
			"ResetTailcatConnectionAsync",
			`SetTunnelModeAsync("tailcat", "", "", port, allowText, true)`,
		},
		filepath.Join("..", "..", "desktop", "windows", "shared", "Services", "RuntimeService.cs"): {
			"ResetTailcatConnectionAsync",
			"RegenerateQuickTunnelAsync",
			"--tailcat-port",
			"--tailcat-allow-set",
		},
		filepath.Join("..", "..", "desktop", "macos", "AgentDockApp", "Sources", "NativeControlPanelWindowController.swift"): {
			`if model.cloudflaredComponent.ready {`,
			`L10n.text("Regenerate temporary address")`,
			`await model.applyTunnel(mode: .quick, serverURL: "", tunnelToken: "")`,
			"TailcatAccessSection(model: model)",
			"LocalMCPAccessSection(model: model)",
			"setLANListen",
			"setTailcatServer",
		},
		filepath.Join("..", "..", "desktop", "macos", "AgentDockApp", "Sources", "LocalMCPAccessSection.swift"): {
			`L10n.text("Start LAN MCP")`,
			`L10n.text("Start Tailcat MCP")`,
			"setLANListen",
			"setTailcatServer",
		},
		filepath.Join("..", "..", "desktop", "macos", "AgentDockApp", "Sources", "TailcatAccessSection.swift"): {
			`L10n.text("Tailcat")`,
			`L10n.text("Reset connection string")`,
			"resetTailcatConnection",
			"applyTailcat",
		},
	}
	for path, required := range checks {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		for _, want := range required {
			if !strings.Contains(content, want) {
				t.Fatalf("%s missing Tailcat control behavior %q", path, want)
			}
		}
	}
}
