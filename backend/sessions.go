package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// 会话历史回放/读取的大小上限：避免把无限增长的临时文件全部塞进单次 WebSocket
// 或 JSON 响应。仅回放最近 maxHistoryBytes 的内容即可满足“恢复会话历史”。
const maxHistoryBytes = 4 * 1024 * 1024 // 4MB

// wsChunkSize 是历史回放时单帧的最大字节数。
const wsChunkSize = 32 * 1024 // 32KB

// Session 是一个独立的 PTY 终端会话。输出实时镜像到临时历史文件（应用停止时
// 整目录清除），浏览器断开只“解挂载”不杀会话，刷新后凭 id 重新挂载并回放历史。
type Session struct {
	id         string
	shell      string
	renv       *RuntimeEnv
	cmd        *exec.Cmd
	pty        *os.File
	hist       *os.File
	createdAt  time.Time
	lastActive time.Time

	// 会话以哪个用户运行（root 或 X-Trim-Userid 指定的 NAS 用户 / 指定 uid）
	uid      int
	gid      int
	home     string
	username string

	// histMu 串行化“追加历史 + 广播到已挂载连接”：回放历史与实时追加在同一把
	// 锁内完成，保证不存在重复或丢失的字节。
	histMu sync.Mutex
	conn   net.Conn
	connMu sync.Mutex

	exited bool
	mu     sync.Mutex
	closed bool
}

// runUser 描述一次会话要切换到的目标用户（uid/gid/home/name）。
type runUser struct {
	uid      int
	gid      int
	home     string
	username string
}

// resolveRunUser 决定新会话以哪个用户运行：
//   - userSpec == "root" → root 用户（uid 0）
//   - userSpec == "app:<APP NAME>" → NAS 应用用户（appcenter-cli list 的 APP NAME），
//     以应用家目录 /var/apps/<APP NAME>/home 为工作目录并作为 HOME
//   - userSpec 为空 / "nas" → 网关 unix sock 传递的 X-Trim-Userid 指定的 NAS 用户
//     （头缺失时回退当前进程用户）
//   - userSpec 为数字 → 指定 uid
//
// 通过 /etc/passwd（os/user.LookupId）解析用户名与家目录；无条目时回退。
func resolveRunUser(renv *RuntimeEnv, userSpec, trimUID string) (*runUser, error) {
	if userSpec == "root" {
		return &runUser{uid: 0, gid: 0, home: "/root", username: "root"}, nil
	}
	if name, ok := strings.CutPrefix(userSpec, appUserSpecPrefix); ok {
		return resolveAppRunUser(renv, name)
	}
	uidStr := ""
	if userSpec == "" || userSpec == "nas" {
		uidStr = trimUID
	} else {
		uidStr = userSpec
	}
	uid := os.Getuid()
	if uidStr != "" {
		if n, err := strconv.Atoi(uidStr); err == nil && n >= 0 {
			uid = n
		}
	}
	gid := os.Getgid()
	home := os.Getenv("HOME")
	name := ""
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		if g, gerr := strconv.Atoi(u.Gid); gerr == nil {
			gid = g
		}
		if u.HomeDir != "" {
			home = u.HomeDir
		}
		name = u.Username
	}
	if home == "" {
		home = "/home/" + uidStr
	}
	return &runUser{uid: uid, gid: gid, home: home, username: name}, nil
}

// buildSessionEnv 构建 shell 会话环境：目标用户的 HOME/PATH，强制 UTF-8 语义。
//
// HOME/PWD 必须是**唯一**的一项：环境变量重复时后者生效，而父进程（由 appcenter
// 脚本用 bash 启动，会导出 HOME/PWD）的值会覆盖本函数的赋值。若被覆盖，后果是
// HOME 指向错误、且 bash 的 PWD 变成物理路径——符号链接家目录下提示符将显示完整
// 路径而非 `~`。故先从继承环境中剔除 HOME/PWD，再写入目标用户的值。
func buildSessionEnv(renv *RuntimeEnv, shell string, runAs *runUser) []string {
	home := renv.Home
	if runAs != nil && runAs.home != "" {
		home = runAs.home
	}
	inherited := os.Environ()
	env := make([]string, 0, len(inherited)+8)
	for _, e := range inherited {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "PWD=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env,
		"HOME="+home,
		"PWD="+home,
		"PATH="+renv.Path,
		"TERM=xterm-256color",
		"LANG="+localeLang(renv.Lang),
		"COLORTERM=truecolor",
		"SHELL="+shell,
	)
	if runAs != nil && runAs.username != "" {
		env = append(env, "USER="+runAs.username, "LOGNAME="+runAs.username)
	}
	return env
}

func (s *Session) markActive() {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
}

func (s *Session) isExited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// pump 持续读取 PTY 输出：追加到历史文件，并向已挂载的连接实时广播。
// 同一把 histMu 保证“文件”与“连接”两路输出顺序一致。
func (s *Session) pump() {
	buf := make([]byte, 8192)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			s.histMu.Lock()
			s.hist.Write(chunk)
			s.connMu.Lock()
			c := s.conn
			s.connMu.Unlock()
			if c != nil {
				c.Write(wsFrame(opText, chunk))
			}
			s.histMu.Unlock()
		}
		if err != nil {
			break
		}
	}
	s.mu.Lock()
	s.exited = true
	s.mu.Unlock()
	// 进程退出后补一条提示帧与退出控制帧（若还有连接在挂载）
	s.histMu.Lock()
	note := []byte("\r\n\x1b[31m[process exited]\x1b[0m\r\n")
	s.hist.Write(note)
	s.connMu.Lock()
	c := s.conn
	s.connMu.Unlock()
	if c != nil {
		c.Write(wsFrame(opText, note))
		c.Write(wsFrame(opText, []byte("\x1b]exit\x07")))
	}
	s.histMu.Unlock()
}

// attach 把连接挂载到会话：先回放历史文件内容（上限 maxHistoryBytes，分帧发送），
// 再发送 ready 标记，最后接管实时流。整段在 histMu 内完成，防止新增输出既被回放
// 又被实时广播造成重复。
func (s *Session) attach(c net.Conn) error {
	s.markActive()
	s.histMu.Lock()
	defer s.histMu.Unlock()
	if s.hist != nil {
		size, err := s.hist.Seek(0, io.SeekEnd)
		if err == nil {
			start := int64(0)
			if size > maxHistoryBytes {
				start = size - maxHistoryBytes
			}
			if _, err := s.hist.Seek(start, io.SeekStart); err == nil {
				chunk := make([]byte, wsChunkSize)
				for {
					n, rerr := s.hist.Read(chunk)
					if n > 0 {
						if _, werr := c.Write(wsFrame(opText, chunk[:n])); werr != nil {
							return werr
						}
					}
					if rerr != nil {
						break
					}
				}
			}
		}
	}
	if _, err := c.Write(wsFrame(opText, []byte("\x1b]ready\x07"))); err != nil {
		return err
	}
	s.connMu.Lock()
	s.conn = c
	s.connMu.Unlock()
	return nil
}

// detach 解除当前连接挂载（不杀会话，会话继续运行并写入历史文件）。
func (s *Session) detach(c net.Conn) {
	s.connMu.Lock()
	if s.conn == c {
		s.conn = nil
	}
	s.connMu.Unlock()
}

// writeInput 把客户端输入写入 PTY。
func (s *Session) writeInput(p []byte) error {
	s.markActive()
	_, err := s.pty.Write(p)
	return err
}

// resize 调整 PTY 尺寸。
func (s *Session) resize(cols, rows uint16) error {
	return pty.Setsize(s.pty, &pty.Winsize{Cols: cols, Rows: rows})
}

// close 终止进程并清理资源（删除历史文件、从管理器移除由调用方负责）。
func (s *Session) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	var firstErr error
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		if err := s.cmd.Wait(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.pty != nil {
		s.pty.Close()
	}
	if s.hist != nil {
		name := s.hist.Name()
		s.hist.Close()
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SessionManager 管理全部活动会话。
type SessionManager struct {
	renv     *RuntimeEnv
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewSessionManager(renv *RuntimeEnv) *SessionManager {
	return &SessionManager{renv: renv, sessions: map[string]*Session{}}
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return strings.ReplaceAll(time.Now().Format("20060102150405.000000000"), ".", "")
}

// create 新建会话：生成 id、创建临时历史文件、启动 PTY 与 pump 协程。创建失败时
// 返回错误并保证不残留文件。调用方随后应把 id 通过控制帧告知前端。
// runAs 决定会话以哪个用户运行（nil → 当前进程用户）。
func (m *SessionManager) create(runAs *runUser) (*Session, error) {
	if runAs == nil {
		var err error
		runAs, err = resolveRunUser(m.renv, "", "")
		if err != nil {
			return nil, err
		}
	}
	id := newID()
	fname := filepath.Join(m.renv.SessionDir, id+".log")
	// 必须以可读可写打开：pump 追加写（O_APPEND），attach 回放 / history API 读。
	hist, err := os.OpenFile(fname, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(m.renv.Shell)
	cmd.Dir = runAs.home
	cmd.Env = buildSessionEnv(m.renv, m.renv.Shell, runAs)
	// 非当前用户时切换凭据（后端以 root 运行时方可 setuid；相同 uid 无需切换）
	if runAs.uid != os.Getuid() {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: uint32(runAs.uid), Gid: uint32(runAs.gid)},
		}
	}
	f, err := pty.Start(cmd)
	if err != nil {
		hist.Close()
		os.Remove(fname)
		return nil, err
	}
	s := &Session{
		id:         id,
		shell:      m.renv.Shell,
		renv:       m.renv,
		cmd:        cmd,
		pty:        f,
		hist:       hist,
		createdAt:  time.Now(),
		lastActive: time.Now(),
		uid:        runAs.uid,
		gid:        runAs.gid,
		home:       runAs.home,
		username:   runAs.username,
	}
	go s.pump()
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	logger().Printf("[terminal] session %s started (shell=%s user=%s uid=%d home=%s)", id, m.renv.Shell, runAs.username, runAs.uid, runAs.home)
	return s, nil
}

func (m *SessionManager) get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// list 返回会话摘要列表（供前端启动时恢复标签）。
func (m *SessionManager) list() []sessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]sessionInfo, 0, len(m.sessions))
	for id, s := range m.sessions {
		info := sessionInfo{
			ID:         id,
			CreatedAt:  s.createdAt.Format(time.RFC3339),
			LastActive: s.lastActive.Format(time.RFC3339),
			Exited:     s.isExited(),
			User:       s.username,
		}
		s.histMu.Lock()
		if s.hist != nil {
			if st, err := s.hist.Stat(); err == nil {
				info.Size = st.Size()
			}
		}
		s.histMu.Unlock()
		out = append(out, info)
	}
	return out
}

// history 读取会话历史文件内容（上限 maxHistoryBytes，取末尾部分）。
func (m *SessionManager) history(id string, max int64) ([]byte, error) {
	s, ok := m.get(id)
	if !ok {
		return nil, errors.New("session not found")
	}
	s.histMu.Lock()
	defer s.histMu.Unlock()
	if s.hist == nil {
		return []byte{}, nil
	}
	size, err := s.hist.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if max > 0 && size > max {
		start = size - max
	}
	if _, err := s.hist.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(s.hist)
}

// clearHistory 清空指定会话的历史临时文件（用于前端「清屏」同步清除历史，
// 重连/刷新后不再回放旧内容）。pump 仍在运行，截断后新输出从 0 继续追加。
func (m *SessionManager) clearHistory(id string) error {
	s, ok := m.get(id)
	if !ok {
		return errors.New("session not found")
	}
	s.histMu.Lock()
	defer s.histMu.Unlock()
	if s.hist == nil {
		return nil
	}
	return s.hist.Truncate(0)
}

// closeByID 关闭指定会话并移除。
func (m *SessionManager) closeByID(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return errors.New("session not found")
	}
	err := s.close()
	logger().Printf("[terminal] session %s closed", id)
	return err
}

// CloseAll 终止所有会话（进程 + 文件），随后由 main 删除整个临时目录。
func (m *SessionManager) CloseAll() {
	m.mu.Lock()
	items := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		items = append(items, s)
	}
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	for _, s := range items {
		s.close()
	}
}