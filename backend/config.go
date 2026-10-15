package main

import (
	"os"
	"path/filepath"
)

// RuntimeEnv holds all runtime configuration resolved from environment variables.
// 本项目只支持 unix socket + baseurl 访问，两者均从环境变量获取。
type RuntimeEnv struct {
	AdminSock     string // TERMINAL_ADMIN_SOCK     唯一访问入口的 unix socket 路径
	AdminBaseURL  string // TERMINAL_ADMIN_BASEURL  前端资源 baseurl 前缀（空 = 根路径）
	QuickCmdsFile string // TERMINAL_QUICK_CMDS_FILE 快捷指令持久化文件
	// MasterKey / MasterKeyFile：快捷指令「命令内容」加密用的主密钥。环境变量优先，
	// 否则用主密钥文件（默认与快捷指令文件同目录的 master.key，缺失时自动生成 0600）。
	MasterKey     string // TERMINAL_MASTER_KEY      外部注入的主密钥（32 字节 base64/hex）
	MasterKeyFile string // TERMINAL_MASTER_KEY_FILE 主密钥文件路径
	SessionDir    string // TERMINAL_SESSION_DIR    终端会话临时目录（应用停止时整目录清除）
	AppHomeTpl    string // TERMINAL_APP_HOME_TEMPLATE 应用用户 HOME 模板（%s=APP NAME）
	AppCenterCLI  string // TERMINAL_APPCENTER_CLI  列出已安装应用的命令
	Shell         string // TERMINAL_SHELL           终端使用的 shell
	Home          string // 运行用户（root）的 HOME
	Path          string // PATH
	Lang          string // LANG
	Version       string // 版本号（-ldflags 注入）
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// keyDir 取某个文件所在目录（用于把主密钥放在快捷指令文件旁边）。
// 路径没有目录成分时用当前目录，避免拼出 "/master.key"。
func keyDir(path string) string {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return "."
	}
	return dir
}

// defaultBase is the default writable base dir for socket / quickcmds / sessions.
func defaultBase() string {
	return filepath.Join(os.TempDir(), "terminal")
}

func loadRuntimeEnv() RuntimeEnv {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/root"
	}
	base := defaultBase()
	quickCmdsFile := envOr("TERMINAL_QUICK_CMDS_FILE", filepath.Join(base, "quickcmds.json"))
	return RuntimeEnv{
		AdminSock:     envOr("TERMINAL_ADMIN_SOCK", filepath.Join(base, "app.sock")),
		AdminBaseURL:  os.Getenv("TERMINAL_ADMIN_BASEURL"),
		QuickCmdsFile: quickCmdsFile,
		MasterKey:     os.Getenv("TERMINAL_MASTER_KEY"),
		// 主密钥默认躺在快捷指令文件**同一个目录**里：数据目录被搬走/备份时密钥不会掉队
		// （代价见 secret.go 的威胁模型——密钥与密文同放，防的是「拿到磁盘快照的人」）。
		MasterKeyFile: envOr("TERMINAL_MASTER_KEY_FILE", filepath.Join(keyDir(quickCmdsFile), masterKeyFileName)),
		SessionDir:    envOr("TERMINAL_SESSION_DIR", filepath.Join(base, "sessions")),
		AppHomeTpl:    envOr("TERMINAL_APP_HOME_TEMPLATE", "/var/apps/%s/home"),
		AppCenterCLI:  envOr("TERMINAL_APPCENTER_CLI", "appcenter-cli"),
		Shell:         envOr("TERMINAL_SHELL", "/bin/bash"),
		Home:          home,
		Path:          os.Getenv("PATH"),
		Lang:          os.Getenv("LANG"),
		Version:       terminalVersion,
	}
}