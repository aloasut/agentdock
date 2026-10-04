package tailcatnode

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/uvwt/agentdock/internal/envstore"
)

// Enabled 报告这个运行目录是否把 Tailcat 当作当前服务模式。
// 旧的临时地址模式 quick 不会在这里自动改写；控制面应用 Tailcat 后才会写成 tailcat。
func Enabled(root string) bool {
	return modeOf(root) == "tailcat"
}

func modeOf(root string) string {
	if root == "" {
		return ""
	}
	if mode, err := os.ReadFile(filepath.Join(root, "cloudflared-mode.txt")); err == nil {
		if text := strings.ToLower(strings.TrimSpace(string(mode))); text != "" {
			return text
		}
	}
	values, err := envstore.ParseFile(filepath.Join(root, "cloudflared.env"))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(values["AGENTDOCK_TUNNEL_MODE"]))
}
