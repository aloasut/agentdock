package tailcatnode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/uvwt/agentdock/internal/secretredact"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"
)

// ServeFunc 在 Tailcat 已经拨通的 TCP 端口上提供节点 WebSocket。
type ServeFunc func(context.Context, net.Listener) error

// Run 在服务模式为 tailcat 时拉起服务器，直到 ctx 取消。
// DERP 暂时不可达只记在状态里并退避重试，不让 AgentDock 进程退出。
func Run(ctx context.Context, root string, serve ServeFunc) {
	if stringsTrim(root) == "" || !Enabled(root) {
		if stringsTrim(root) != "" {
			_ = writeStatus(root, Status{})
		}
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := runOnce(ctx, root, serve)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("tailcat server stopped", "error", secretredact.Text(errString(err)), "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func stringsTrim(value string) string {
	return string(bytes.TrimSpace([]byte(value)))
}

func runOnce(ctx context.Context, root string, serve ServeFunc) (err error) {
	cfg, err := LoadConfig(root)
	if err != nil {
		_ = writeStatus(root, Status{Error: err.Error()})
		return err
	}
	if err = ensureKey(root); err != nil {
		_ = writeStatus(root, Status{Port: cfg.Port, Allow: cfg.Allow, Error: err.Error()})
		return err
	}
	server, err := buildServer(root, cfg)
	if err != nil {
		_ = writeStatus(root, Status{Port: cfg.Port, Allow: cfg.Allow, Error: err.Error()})
		return err
	}
	listenCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	listener, listenErr := server.Listen(listenCtx, "tcp", fmt.Sprintf(":%d", cfg.Port))
	cancel()
	if listenErr != nil {
		_ = server.Close()
		_ = writeStatus(root, Status{Port: cfg.Port, Allow: cfg.Allow, Error: listenErr.Error()})
		return listenErr
	}
	address := string(server.TailcatAddr())
	if err = savePin(root, address); err != nil {
		_ = listener.Close()
		_ = server.Close()
		_ = writeStatus(root, Status{Port: cfg.Port, Allow: cfg.Allow, Error: err.Error()})
		return err
	}
	if err = writeStatus(root, Status{Running: true, Port: cfg.Port, Address: address, Allow: cfg.Allow}); err != nil {
		_ = listener.Close()
		_ = server.Close()
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = server.Close()
		status := Status{Port: cfg.Port, Address: address, Allow: cfg.Allow}
		if err != nil && ctx.Err() == nil {
			status.Error = err.Error()
		}
		_ = writeStatus(root, status)
	}()
	if serve == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	err = serve(ctx, listener)
	if err == nil {
		err = ctx.Err()
	}
	return err
}

func ensureKey(root string) error {
	path := filepath.Join(root, dirName, keyFileName)
	if _, err := readSecretFile(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, _, err := newIdentity()
	if err != nil {
		return err
	}
	return writeSecretFile(path, raw)
}

func newIdentity() ([]byte, string, error) {
	private := tailcat.NewPrivateKey()
	raw, err := json.MarshalIndent(private, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("编码 Tailcat 密钥: %w", err)
	}
	return append(raw, '\n'), private.Private.Public().String(), nil
}

func decodeIdentity(raw []byte) (*tailcat.PrivateKey, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var private tailcat.PrivateKey
	if err := decoder.Decode(&private); err != nil {
		return nil, fmt.Errorf("解析 Tailcat 密钥: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("解析 Tailcat 密钥: 存在多余内容")
	}
	if private.Private.IsZero() {
		return nil, errors.New("Tailcat 密钥为空")
	}
	if private.Public.PresharedKey.IsZero() {
		return nil, errors.New("Tailcat 密钥缺少预共享密钥")
	}
	if private.Public.ServerPublic.NodePublic != private.Private.Public() {
		return nil, errors.New("Tailcat 密钥的公钥与私钥不一致")
	}
	return &private, nil
}

func buildServer(root string, cfg Config) (*tailcat.Server, error) {
	raw, err := readSecretFile(filepath.Join(root, dirName, keyFileName))
	if err != nil {
		return nil, err
	}
	private, err := decodeIdentity(raw)
	if err != nil {
		return nil, err
	}
	allowed, err := allowedClients(cfg.Allow)
	if err != nil {
		return nil, err
	}
	server := &tailcat.Server{
		Key:          private.Private,
		PresharedKey: private.Public.PresharedKey,
		Logf:         safeLogf,
		// 只放行管理员声明的那个 TCP 端口。空 UDP 名单不再接受数据包。
		ServedTCPPorts: []filter.PortRange{{First: uint16(cfg.Port), Last: uint16(cfg.Port)}},
		ServedUDPPorts: []filter.PortRange{},
		AllowedClients: allowed,
	}
	region, err := pinnedRegion(root)
	if err != nil {
		return nil, err
	}
	server.Region = region
	return server, nil
}

func allowedClients(texts []string) ([]key.NodePublic, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([]key.NodePublic, 0, len(texts))
	for _, text := range texts {
		var public key.NodePublic
		if err := public.UnmarshalText([]byte(text)); err != nil || public.IsZero() {
			return nil, fmt.Errorf("解析允许的客户端 %s: %w", text, err)
		}
		out = append(out, public)
	}
	return out, nil
}

func pinnedRegion(root string) (*tailcfg.DERPRegion, error) {
	data, err := readSecretFile(filepath.Join(root, dirName, regionFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var region tailcfg.DERPRegion
	if err := json.Unmarshal(data, &region); err != nil || region.RegionID == 0 || len(region.Nodes) == 0 {
		return nil, errors.New("Tailcat DERP 钉选无效")
	}
	return &region, nil
}

func savePin(root, address string) error {
	info, err := tailcat.ParseAddr(tailcat.Addr(address))
	if err != nil {
		return fmt.Errorf("解析 Tailcat 地址中的 DERP 区域: %w", err)
	}
	if len(info.Region) != 1 || info.Region[0] == nil {
		return errors.New("Tailcat 地址未嵌入唯一 DERP 区域")
	}
	raw, err := json.MarshalIndent(info.Region[0], "", "  ")
	if err != nil {
		return fmt.Errorf("编码 DERP 区域: %w", err)
	}
	return writeSecretFile(filepath.Join(root, dirName, regionFileName), append(raw, '\n'))
}

func safeLogf(format string, args ...any) {
	slog.Debug("tailcat", "message", secretredact.Text(fmt.Sprintf(format, args...)))
}
