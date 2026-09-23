package config

import (
	"net"
	"testing"
)

func TestNormalizeAcceptsLANListenHost(t *testing.T) {
	for _, raw := range []string{"lan", "LAN", " Lan "} {
		cfg := Config{Host: raw}
		if err := cfg.Normalize(); err != nil {
			t.Fatalf("Normalize(%q) error = %v", raw, err)
		}
		if cfg.Host != ListenHostLAN {
			t.Fatalf("Normalize(%q).Host = %q, want %q", raw, cfg.Host, ListenHostLAN)
		}
	}
}

func TestValidateAuthRequiresCredentialForLANHost(t *testing.T) {
	// lan 是非回环监听：与显式公网地址一样必须带认证，否则拒绝启动。
	cfg := Config{Host: ListenHostLAN, Stdio: false}
	if err := cfg.ValidateAuth(); err == nil {
		t.Fatal("ValidateAuth() = nil for lan host without credentials, want error")
	}
	cfg.AuthToken = "token"
	if err := cfg.ValidateAuth(); err != nil {
		t.Fatalf("ValidateAuth() error = %v with bearer token, want nil", err)
	}
}

func TestListenHostsPassesPlainHostThrough(t *testing.T) {
	cfg := Config{Host: "127.0.0.1"}
	hosts := cfg.ListenHosts()
	if len(hosts) != 1 || hosts[0] != "127.0.0.1" {
		t.Fatalf("ListenHosts() = %v, want [127.0.0.1]", hosts)
	}
}

func TestLANListenHostsLoopbackFirstAndPrivateOnly(t *testing.T) {
	hosts := lanListenHosts()
	if len(hosts) == 0 || hosts[0] != "127.0.0.1" {
		t.Fatalf("lanListenHosts() = %v, want loopback first", hosts)
	}
	seen := map[string]struct{}{}
	for _, host := range hosts {
		ip := net.ParseIP(host)
		if ip == nil {
			t.Fatalf("invalid host %q in listen list", host)
		}
		if _, dup := seen[host]; dup {
			t.Fatalf("lanListenHosts() contains duplicate %q", host)
		}
		seen[host] = struct{}{}
		if host == "127.0.0.1" {
			continue
		}
		if ip.IsLoopback() || !ip.IsPrivate() {
			t.Fatalf("lanListenHosts() contains non-private host %q", host)
		}
	}
}
