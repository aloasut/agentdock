package tailcatnode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAllowCanonicalizesNodeKeys(t *testing.T) {
	key := "nodekey:" + strings.Repeat("ab", 32)
	upper := "nodekey:" + strings.Repeat("AB", 32)
	got, err := ParseAllow(upper + "\n" + key)
	if err == nil {
		t.Fatal("duplicate node key was accepted")
	}
	got, err = ParseAllow("  " + upper + ", ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != key {
		t.Fatalf("allow = %#v", got)
	}
	if _, err := ParseAllow("nodekey:abcd"); err == nil {
		t.Fatal("short node key was accepted")
	}
	empty, err := ParseAllow(" \n\t")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty allow = %#v, %v", empty, err)
	}
}

func TestEnsureConfigKeepsUnspecifiedFields(t *testing.T) {
	root := t.TempDir()
	key := "nodekey:" + strings.Repeat("cd", 32)
	if err := EnsureConfig(root, 443, key, true); err != nil {
		t.Fatal(err)
	}
	if err := EnsureConfig(root, 0, "", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 443 || len(cfg.Allow) != 1 || cfg.Allow[0] != key {
		t.Fatalf("config = %#v", cfg)
	}
	info, err := os.Lstat(configPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
}

func TestStatusFileOmitsAddressWhenRotated(t *testing.T) {
	root := t.TempDir()
	if err := writeStatus(root, Status{Running: true, Port: 80, Address: "tc" + strings.Repeat("a", 20), Allow: nil}); err != nil {
		t.Fatal(err)
	}
	if err := RotateSecrets(root); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus(root)
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || status.Address != "" {
		t.Fatalf("status after rotate = %#v", status)
	}
	data, err := os.ReadFile(filepath.Join(root, statusName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "tc"+strings.Repeat("a", 20)) {
		t.Fatal("status file kept the connection string")
	}
}

func TestImportBoundary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "github.com/tailscale/tailcat") || strings.Contains(text, "tailscale.com/") {
			if name != "runtime.go" {
				t.Fatalf("%s imports tailcat", name)
			}
		}
	}
}

func TestLegacyQuickModeDoesNotEnableTailcat(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cloudflared-mode.txt"), []byte("quick\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Enabled(root) {
		t.Fatal("quick mode must not start the tailcat server")
	}
	if err := os.WriteFile(filepath.Join(root, "cloudflared-mode.txt"), []byte("tailcat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Enabled(root) {
		t.Fatal("tailcat mode was not recognized")
	}
}
