package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// userModeFile 保存“新建终端会话以哪个用户启动”的全局偏好
// （persisted 到 TERMINAL_USER_MODE_FILE 指定的文件）：
//   - "nas"：默认，网关 unix sock 传递的 X-Trim-Userid 指定的当前 NAS 用户
//   - "root"：以 root 运行
//   - "custom"：启动无会话恢复时以登录用户建立会话；此后每次新建终端由前端弹窗选择
type userModeFile struct {
	Mode string `json:"mode"` // "nas" | "root" | "custom"
}

var userModeMu sync.Mutex

var validUserModes = map[string]bool{"nas": true, "root": true, "custom": true}

// loadUserModeFile 读取持久化的启动用户模式；文件不存在或内容非法时默认 "nas"。
func loadUserModeFile(path string) (string, error) {
	userModeMu.Lock()
	defer userModeMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "nas", nil
		}
		return "", err
	}
	var f userModeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return "", err
	}
	if !validUserModes[f.Mode] {
		return "nas", nil
	}
	return f.Mode, nil
}

// saveUserModeFile 原子写入启动用户模式（tmp 文件 + rename）。
func saveUserModeFile(path, mode string) error {
	userModeMu.Lock()
	defer userModeMu.Unlock()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(userModeFile{Mode: mode}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}