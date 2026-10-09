package main

import (
	"embed"
	"errors"
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

// bootID 是本次进程实例的随机标识（每次启动都不同），经 /api/info 的 bootId 下发。
// 前端把它当作「后端是否已重启」的唯一判据：进程重启后旧会话全部不复存在（退出时会话
// 临时目录被整目录删除），页面必须清空全部标签、让用户重新选择用户，而不是拿旧 id
// 静默重建会话。
var bootID = newID()

func logger() *log.Logger {
	return log.New(os.Stdout, "[Terminal] ", log.LstdFlags)
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

	// 先绑定再进信号等待：绑定失败就退出（而不是空转），失败信息进日志由 supervisor 兜底。
	ln, err := listenAdminSocket(&renv)
	if err != nil {
		logger().Printf("admin socket listen %s: %v", renv.AdminSock, err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: admin.buildHandler()}
	srvErr := make(chan error, 1)
	go func() {
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			srvErr <- serr
		}
	}()

	// 等待退出信号（或 accept 彻底失败：此时已没有可用入口，按进程退出处理）
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	exitCode := 0
	select {
	case <-sig:
	case serr := <-srvErr:
		logger().Printf("admin socket server: %v", serr)
		exitCode = 1
	}

	// 优雅退出：先停掉入口（不再接受新请求/新会话）→ 终止全部会话 → 删除 socket 与会话目录。
	// 曾经只在 goroutine 里等一个**从不关闭**的 stopCh，srv.Close() 永远不执行，
	// 上面这些清理是在 server 仍在服务时做的。
	_ = srv.Close()
	admin.sessions.CloseAll()
	os.Remove(renv.AdminSock)
	if createdSessionDir {
		os.RemoveAll(renv.SessionDir)
	}
	os.Exit(exitCode)
}