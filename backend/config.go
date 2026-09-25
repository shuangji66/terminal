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
	return RuntimeEnv{
		AdminSock:     envOr("TERMINAL_ADMIN_SOCK", filepath.Join(base, "app.sock")),
		AdminBaseURL:  os.Getenv("TERMINAL_ADMIN_BASEURL"),
		QuickCmdsFile: envOr("TERMINAL_QUICK_CMDS_FILE", filepath.Join(base, "quickcmds.json")),
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