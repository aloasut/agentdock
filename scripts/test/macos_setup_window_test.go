package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMacOSAdvancedConnectionGatesCloudflareBehindOptionalComponent(t *testing.T) {
	root := filepath.Join("..", "..", "desktop", "macos", "AgentDockApp")
	data, err := os.ReadFile(filepath.Join(root, "Sources", "NativeControlPanelWindowController.swift"))
	if err != nil {
		t.Fatalf("read NativeControlPanelWindowController.swift: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		`model.settingsPage == .advancedConnection`,
		`await model.refreshCloudflaredComponent()`,
		`SettingsSection(L10n.text("Cloudflare Tunnel"))`,
		`model.cloudflaredComponent.state == "broken"`,
		`L10n.text("Repair")`,
		`L10n.text("Install")`,
		`L10n.text("Uninstall")`,
		`TailcatAccessSection(model: model)`,
		`if model.cloudflaredComponent.ready {`,
		`L10n.text("Temporary domain")`,
		`L10n.text("Fixed domain")`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("macOS advanced connection missing optional component contract %q", want)
		}
	}

	forbidden := []string{
		`SetupWindowController`,
	}
	for _, value := range forbidden {
		if strings.Contains(content, value) {
			t.Fatalf("active macOS control panel must not reference legacy setup controller %q", value)
		}
	}
}

func TestMacOSAppRemainsInDockAfterWindowCloses(t *testing.T) {
	root := filepath.Join("..", "..", "desktop", "macos", "AgentDockApp", "Sources")
	mainSource, err := os.ReadFile(filepath.Join(root, "main.swift"))
	if err != nil {
		t.Fatalf("read main.swift: %v", err)
	}
	delegate, err := os.ReadFile(filepath.Join(root, "AppDelegate.swift"))
	if err != nil {
		t.Fatalf("read AppDelegate.swift: %v", err)
	}
	serviceController, err := os.ReadFile(filepath.Join(root, "ServiceController.swift"))
	if err != nil {
		t.Fatalf("read ServiceController.swift: %v", err)
	}
	plist, err := os.ReadFile(filepath.Join("..", "..", "packaging", "macos", "build-app.sh"))
	if err != nil {
		t.Fatalf("read build-app.sh: %v", err)
	}
	windowController, err := os.ReadFile(filepath.Join(root, "NativeControlPanelWindowController.swift"))
	if err != nil {
		t.Fatalf("read NativeControlPanelWindowController.swift: %v", err)
	}
	for _, want := range []string{
		"setActivationPolicy(.regular)",
		"applicationShouldTerminateAfterLastWindowClosed",
		"applicationShouldHandleReopen",
		"ensureCoreProcess",
		"prepareCoreServiceRegistration",
		"spawnFallbackCore",
		"service\", \"launch-core\"",
		"service.stopAppOwnedCore()",
		"stopStaleOwnedCores",
		"windowShouldClose",
		"hideToMenuBar",
		"kProcessTransformToUIElementApplication",
		"installMenuBarItem()",
		"autosaveName = \"AgentDockTray\"",
		"NSStatusItem Preferred Position AgentDockTray",
		"Quit completely",
	} {
		combined := string(mainSource) + string(delegate) + string(serviceController) + string(windowController)
		if !strings.Contains(combined, want) {
			t.Fatalf("macOS app must hide to the menu bar on close and still be able to quit; missing %q", want)
		}
	}
	hideStart := strings.Index(string(delegate), "func hideToMenuBar()")
	if hideStart < 0 {
		t.Fatal("hideToMenuBar must stay a distinct method")
	}
	hideEnd := strings.Index(string(delegate)[hideStart:], "\n    func ")
	if hideEnd < 0 {
		t.Fatal("hideToMenuBar must stay a distinct method")
	}
	hideBody := string(delegate)[hideStart : hideStart+hideEnd]
	installAt := strings.Index(hideBody, "installMenuBarItem()")
	transformAt := strings.Index(hideBody, "kProcessTransformToUIElementApplication")
	removeAt := strings.Index(hideBody, "removeStatusItem()")
	if installAt < 0 || transformAt < 0 || installAt > transformAt || removeAt >= 0 {
		t.Fatal("the menu bar icon must be installed before leaving the Dock, and that transition must keep the same icon")
	}
	if strings.Contains(string(mainSource), "setActivationPolicy(.accessory)") {
		t.Fatal("launching as an accessory app leaves no Dock icon while the window is open")
	}
	if strings.Contains(string(plist), "<key>LSUIElement</key>") {
		t.Fatal("LSUIElement hides the Dock icon and leaves no way back after the window closes")
	}
}

func TestMacOSLegacySetupControllerRemoved(t *testing.T) {
	path := filepath.Join("..", "..", "desktop", "macos", "AgentDockApp", "Sources", "SetupWindowController.swift")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy SetupWindowController must be removed; stat err=%v", err)
	}
}
