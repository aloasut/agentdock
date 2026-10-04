package tailcatnode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// DefaultPort 是隧道里的节点 WebSocket 端口。它不是 DERP 端口，也不是本机 HTTP 端口。
	DefaultPort = 80
	dirName     = "tailcat"
	configName  = "config.json"
	statusName  = "tailcat-status.json"
	maxAllow    = 128
)

// nodekey 文本是 "nodekey:" 加 32 字节公钥的十六进制。空名单表示持有连接串的客户端都能进。
var nodeKeyPattern = regexp.MustCompile(`^nodekey:[0-9a-fA-F]{64}$`)

// Config 是管理员在控制面保存的服务端口和允许名单。
type Config struct {
	Port  int      `json:"port"`
	Allow []string `json:"allow"`
}

// Status 是控制面可以展示的运行状态。Address 含预共享密钥，只写进权限收紧的状态文件。
type Status struct {
	Running bool     `json:"running"`
	Port    int      `json:"port,omitempty"`
	Address string   `json:"address,omitempty"`
	Allow   []string `json:"allow,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func configPath(root string) string {
	return filepath.Join(root, dirName, configName)
}

func statusPath(root string) string {
	return filepath.Join(root, statusName)
}

// LoadConfig 读取已保存的端口和允许名单。文件不存在时使用默认端口、空名单。
func LoadConfig(root string) (Config, error) {
	data, err := readSecretFile(configPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return Config{Port: DefaultPort}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("读取 Tailcat 配置: %w", err)
	}
	return parseConfig(data)
}

func parseConfig(data []byte) (Config, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("解析 Tailcat 配置: %w", err)
	}
	normalized, err := normalizeConfig(cfg.Port, cfg.Allow)
	if err != nil {
		return Config{}, err
	}
	return normalized, nil
}

func normalizeConfig(port int, allow []string) (Config, error) {
	if port == 0 {
		port = DefaultPort
	}
	if port < 1 || port > 65535 {
		return Config{}, errors.New("Tailcat 服务端口必须是 1-65535")
	}
	parsed, err := ParseAllow(strings.Join(allow, "\n"))
	if err != nil {
		return Config{}, err
	}
	return Config{Port: port, Allow: parsed}, nil
}

// ParseAllow 把控制面文本解析成 nodekey 列表。空白表示不限制客户端。
func ParseAllow(text string) ([]string, error) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	if len(fields) > maxAllow {
		return nil, errors.New("Tailcat 允许名单最多 128 个客户端")
	}
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		key := strings.TrimSpace(field)
		if key == "" {
			continue
		}
		if !nodeKeyPattern.MatchString(key) {
			return nil, errors.New("Tailcat 允许名单中的客户端必须是 nodekey: 加 64 位十六进制")
		}
		canonical := "nodekey:" + strings.ToLower(strings.TrimPrefix(key, "nodekey:"))
		if _, ok := seen[canonical]; ok {
			return nil, errors.New("Tailcat 允许名单里有重复的节点公钥")
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	sort.Strings(out)
	return out, nil
}

// EnsureConfig 落盘端口和允许名单。port 为 0 时保留已有端口；allowSet 为 false 时保留已有名单。
func EnsureConfig(root string, port int, allowText string, allowSet bool) error {
	current, err := LoadConfig(root)
	if err != nil {
		return err
	}
	if port == 0 {
		port = current.Port
	}
	allow := current.Allow
	if allowSet {
		allow, err = ParseAllow(allowText)
		if err != nil {
			return err
		}
	}
	cfg, err := normalizeConfig(port, allow)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 Tailcat 配置: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(root, dirName), 0o700); err != nil {
		return fmt.Errorf("创建 Tailcat 目录: %w", err)
	}
	return writeSecretFile(configPath(root), append(raw, '\n'))
}

// ReadStatus 读取控制面状态。文件不存在时返回未运行。
func ReadStatus(root string) (Status, error) {
	data, err := readSecretFile(statusPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return Status{}, fmt.Errorf("解析 Tailcat 状态: %w", err)
	}
	return status, nil
}
