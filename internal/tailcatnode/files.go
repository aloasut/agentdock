package tailcatnode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/uvwt/agentdock/internal/secretredact"
)

const (
	keyFileName    = "key.json"
	regionFileName = "region.json"
)

func writeSecretFile(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s 不能是符号链接", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建 %s: %w", dir, err)
	}
	file, err := os.CreateTemp(dir, ".tailcat-*")
	if err != nil {
		return fmt.Errorf("创建 Tailcat 临时文件: %w", err)
	}
	tempPath := file.Name()
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("设置 Tailcat 临时文件权限: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("写入 Tailcat 临时文件: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("同步 Tailcat 临时文件: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭 Tailcat 临时文件: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("替换 %s: %w", path, err)
	}
	cleanup = false
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("收紧 %s 权限: %w", path, err)
	}
	return nil
}

func readSecretFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s 不能是符号链接", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("收紧 %s 权限: %w", path, err)
	}
	return data, nil
}

func writeStatus(root string, status Status) error {
	status.Error = secretredact.Text(status.Error)
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 Tailcat 状态: %w", err)
	}
	return writeSecretFile(statusPath(root), append(raw, '\n'))
}

// RotateSecrets 删掉服务器身份和已钉选的 DERP 区域。下次启动会得到新的连接串。
func RotateSecrets(root string) error {
	dir := filepath.Join(root, dirName)
	for _, name := range []string{keyFileName, regionFileName} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s 不能是符号链接", path)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除 %s: %w", path, err)
		}
	}
	status, err := ReadStatus(root)
	if err != nil {
		return err
	}
	status.Running = false
	status.Address = ""
	status.Error = ""
	return writeStatus(root, status)
}
