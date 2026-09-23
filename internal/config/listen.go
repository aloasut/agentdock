package config

import "net"

// ListenHosts 返回 HTTP 监听要绑定的主机列表（不含端口）。
//
// lan 模式解析为「回环 + 本机当前全部私网网段地址」：机器同时在多个网段（多网卡、
// 多 VLAN）时每个网段都会监听。私网判定用 RFC1918（IPv4）与 ULA fc00::/7（IPv6）；
// link-local（fe80::）需要作用域后缀，无法稳定生成客户端可用的 URL，刻意排除。
// 发现不到私网地址时回退为仅回环，由调用方决定如何告警。
//
// 回环始终在列表里：本机控制面（桌面 App、健康探测）按 127.0.0.1 访问，
// 不随 LAN 模式改变。其余 host 值按单主机原样返回。
func (c Config) ListenHosts() []string {
	if c.Host != ListenHostLAN {
		return []string{c.Host}
	}
	return lanListenHosts()
}

func lanListenHosts() []string {
	hosts := []string{"127.0.0.1"}
	seen := map[string]struct{}{"127.0.0.1": {}}
	interfaces, err := net.Interfaces()
	if err != nil {
		return hosts
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil || !ipNet.IP.IsPrivate() {
				continue
			}
			host := ipNet.IP.String()
			if _, dup := seen[host]; dup {
				continue
			}
			seen[host] = struct{}{}
			hosts = append(hosts, host)
		}
	}
	return hosts
}
