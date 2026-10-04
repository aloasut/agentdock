package desktopruntime

import "github.com/uvwt/agentdock/internal/tailcatnode"

func rotateTailcat(root string) error {
	return tailcatnode.RotateSecrets(root)
}

func tailcatTunnelStatus(root string) TunnelStatus {
	status, err := tailcatnode.ReadStatus(root)
	if err != nil {
		return TunnelStatus{Mode: "tailcat", TailcatError: err.Error()}
	}
	cfg, cfgErr := tailcatnode.LoadConfig(root)
	if cfgErr == nil && status.Port == 0 {
		status.Port = cfg.Port
		if len(status.Allow) == 0 {
			status.Allow = cfg.Allow
		}
	}
	return TunnelStatus{
		Mode:           "tailcat",
		Running:        status.Running,
		Ready:          status.Running && status.Address != "" && status.Port > 0,
		StartupEnabled: false,
		TailcatPort:    status.Port,
		TailcatAddress: status.Address,
		TailcatAllow:   status.Allow,
		TailcatError:   status.Error,
	}
}
