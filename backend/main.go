package main

import (
	"embed"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

//go:embed all:embed
var embeddedFS embed.FS

// terminalVersion is the version injected via -ldflags (default "dev").
var terminalVersion = "dev"

// stopCh is closed when the process should shut down gracefully.
var stopCh = make(chan struct{})

func logger() *log.Logger {
	return log.New(os.Stdout, "[Terminal] ", log.LstdFlags)
}

// netListen is a thin wrapper so admin.go can reference it.
func netListen(network, addr string) (net.Listener, error) {
	return net.Listen(network, addr)
}

func main() {
	renv := loadRuntimeEnv()
	// 日志纪律：只记录异常/失败（见 AGENTS.md），启动/停止这类例行信息不再输出。

	// 准备目录：会话临时目录（应用停止时整目录清除）与快捷指令文件所在目录
	createdSessionDir := false
	if renv.SessionDir != "" && renv.SessionDir != "/" && renv.SessionDir != "." {
		if err := os.MkdirAll(renv.SessionDir, 0o700); err != nil {
			logger().Printf("failed to create session dir %s: %v", renv.SessionDir, err)
		} else {
			createdSessionDir = true
		}
	}
	if dir := filepath.Dir(renv.QuickCmdsFile); dir != "" && dir != "." {
		os.MkdirAll(dir, 0o755)
	}

	// 唯一访问入口：unix socket（baseurl 仅用于前端资源前缀）
	if _, err := net.Dial("unix", renv.AdminSock); err == nil {
		logger().Printf("admin socket %s already in use, exiting", renv.AdminSock)
		os.Exit(1)
	}
	admin := newAdminMux(&renv)
	admin.SetSPA(embeddedFS)

	go func() {
		if err := serveAdminSocket(admin); err != nil && err != http.ErrServerClosed {
			logger().Printf("admin socket server: %v", err)
		}
	}()

	// 等待退出信号
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	select {
	case <-sig:
	case <-stopCh:
	}

	// 优雅退出：终止所有会话进程 → 删除临时会话目录
	admin.sessions.CloseAll()
	os.Remove(renv.AdminSock)
	if createdSessionDir {
		os.RemoveAll(renv.SessionDir)
	}
}