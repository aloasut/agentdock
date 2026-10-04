package desktopruntime

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"

	"strconv"

	"github.com/uvwt/agentdock/internal/desktopcontrol"
	"github.com/uvwt/agentdock/internal/tailcatnode"
)

// TunnelStatus 是桌面端和 CLI 共享的结构化 Tunnel 状态。
// TailcatAddress 是连接串，只出现在本机控制面的这次输出里，调用方不能再写进日志。
type TunnelStatus struct {
	Mode           string   `json:"mode"`
	Running        bool     `json:"running"`
	Ready          bool     `json:"ready"`
	StartupEnabled bool     `json:"startup_enabled"`
	PublicURL      string   `json:"public_url,omitempty"`
	TailcatPort    int      `json:"tailcat_port,omitempty"`
	TailcatAddress string   `json:"tailcat_address,omitempty"`
	TailcatAllow   []string `json:"tailcat_allow,omitempty"`
	TailcatError   string   `json:"tailcat_error,omitempty"`
}

type TunnelConfigureRequest struct {
	RuntimeRoot     string
	Mode            string
	ServerURL       string
	TokenFile       string
	TailcatPort     int
	TailcatAllow    string
	TailcatAllowSet bool
}

func RunTunnelCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return tunnelCommandUsageError()
	}

	switch args[0] {
	case "launch":
		runtimeRoot, err := parseLaunchRuntimeRoot("agentdock tunnel launch", args[1:], stderr)
		if err != nil {
			return err
		}
		return platformLaunchTunnel(ctx, runtimeRoot)
	case "status":
		runtimeRoot, err := parseRuntimeRoot("agentdock tunnel status", args[1:], stderr)
		if err != nil {
			return err
		}
		var status TunnelStatus
		err = desktopcontrol.Call(ctx, runtimeRoot, "tunnel.status", controlActionParams{RuntimeRoot: runtimeRoot}, &status)
		if err != nil {
			status, err = platformTunnelStatus(ctx, runtimeRoot)
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(status)
	case "start", "stop", "restart", "regenerate":
		action := args[0]
		runtimeRoot, err := parseRuntimeRoot("agentdock tunnel "+action, args[1:], stderr)
		if err != nil {
			return err
		}
		// 写操作可能重启当前核心，必须由独立控制进程直接调用系统适配器；
		// IPC 仅用于不会改变服务生命周期的状态读取。
		if err := platformTunnelAction(ctx, runtimeRoot, action); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(serviceCommandResult{Action: action, Completed: true})
	case "configure":
		flags := flag.NewFlagSet("agentdock tunnel configure", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runtimeRoot := flags.String("runtime-root", "", "AgentDock 桌面运行目录")
		mode := flags.String("mode", "", "服务模式：none、tailcat、quick 或 named")
		serverURL := flags.String("server-url", "", "Named Tunnel HTTPS Origin")
		tokenFile := flags.String("token-file", "", "临时 Tunnel Token 文件")
		tailcatPort := flags.String("tailcat-port", "", "Tailcat 隧道内 TCP 端口")
		tailcatAllow := flags.String("tailcat-allow", "", "允许拨入的 nodekey，逗号或空白分隔")
		tailcatAllowSet := flags.Bool("tailcat-allow-set", false, "更新 Tailcat 允许名单；空名单表示不限制")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*runtimeRoot) == "" {
			return errors.New("用法：agentdock tunnel configure --runtime-root <目录> --mode <none|tailcat|named> [--server-url <HTTPS Origin>] [--token-file <文件>] [--tailcat-port <端口>] [--tailcat-allow <nodekey>] [--tailcat-allow-set]")
		}
		normalizedMode := strings.ToLower(strings.TrimSpace(*mode))
		if normalizedMode != "none" && normalizedMode != "quick" && normalizedMode != "tailcat" && normalizedMode != "named" {
			return errors.New("tunnel configure 的 mode 必须是 none、tailcat、quick 或 named")
		}
		port := 0
		if strings.TrimSpace(*tailcatPort) != "" {
			parsed, err := strconv.Atoi(strings.TrimSpace(*tailcatPort))
			if err != nil || parsed < 1 || parsed > 65535 {
				return errors.New("tailcat-port 必须是 1-65535")
			}
			port = parsed
		}
		if normalizedMode == "tailcat" && (*tailcatAllowSet || port != 0) {
			if _, err := tailcatnode.ParseAllow(*tailcatAllow); err != nil && *tailcatAllowSet {
				return err
			}
		}
		request := TunnelConfigureRequest{
			RuntimeRoot:     *runtimeRoot,
			Mode:            normalizedMode,
			ServerURL:       strings.TrimSpace(*serverURL),
			TokenFile:       strings.TrimSpace(*tokenFile),
			TailcatPort:     port,
			TailcatAllow:    *tailcatAllow,
			TailcatAllowSet: *tailcatAllowSet,
		}
		if err := platformConfigureTunnel(ctx, request); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(serviceCommandResult{Action: "configure", Completed: true})
	case "autostart":
		flags := flag.NewFlagSet("agentdock tunnel autostart", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runtimeRoot := flags.String("runtime-root", "", "AgentDock 桌面运行目录")
		enabled := flags.String("enabled", "", "是否启用：true 或 false")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*runtimeRoot) == "" {
			return errors.New("用法：agentdock tunnel autostart --runtime-root <目录> --enabled <true|false>")
		}
		shouldEnable, err := parseCommandBoolean("tunnel autostart", *enabled)
		if err != nil {
			return err
		}
		if err := platformSetTunnelAutostart(ctx, *runtimeRoot, shouldEnable); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(serviceCommandResult{Action: "autostart", Completed: true})
	default:
		return tunnelCommandUsageError()
	}
}

func parseLaunchRuntimeRoot(name string, args []string, stderr io.Writer) (string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	runtimeRoot := flags.String("runtime-root", DefaultRuntimeRoot(), "AgentDock 桌面运行目录")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*runtimeRoot) == "" {
		return "", errors.New("用法：" + name + " --runtime-root <目录>")
	}
	return strings.TrimSpace(*runtimeRoot), nil
}

func parseCommandBoolean(command, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New(command + " 的 enabled 必须是 true 或 false")
	}
}

func tunnelCommandUsageError() error {
	return errors.New("用法：agentdock tunnel <launch|status|start|stop|restart|regenerate|configure|autostart> --runtime-root <目录>")
}
