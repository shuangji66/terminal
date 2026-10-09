package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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

// connWriteTimeout 是向单个已挂载连接写数据（实时广播 / 顶号通知）的写超时。
// 客户端停止读取（挂起的笔记本、静默掉线的网络、被 SIGSTOP 的标签页）时内核发送缓冲
// 会填满，无超时的 Write 会永久阻塞——而广播是在 histMu 内做的，一旦卡住，同会话的
// 历史回放（attach）、history/clear 接口会一起挂死，会话从此无法再被任何设备挂载。
// 超时即视为该端已失联：广播侧解除挂载（下一轮 conn 为 nil，直接跳过）。
const connWriteTimeout = 5 * time.Second

// exitedGrace 是「shell 已退出」的会话在服务端的保留时长：这段时间内仍可回放历史，
// 到期由后端自动回收（进程 + 临时文件）。已退出的会话不再有任何交互价值，不回收就会
// 在进程生命周期内无限堆积（PTY 已死，输入只会得到 EIO）。注意这只针对**已退出**的
// 会话：运行中的会话仍然只能经 DELETE /api/session 终止（见 AGENTS.md）。
// （var 而非 const：便于测试把宽限期缩短。）
var exitedGrace = 10 * time.Minute

// Session 是一个独立的 PTY 终端会话。输出实时镜像到临时历史文件（应用停止时
// 整目录清除），浏览器断开只“解挂载”不杀会话，刷新后凭 id 重新挂载并回放历史。
type Session struct {
	// spec 是创建时会话请求的原始 user= 参数（'' / 'nas' / 'root' / 'app:<APP NAME>'）。
	// 前端靠它把「恢复的标签」还原成正确的 userSpec —— 后端重启后自动重建会话时，
	// 若只按运行用户名猜，root/应用用户会被重建成登录用户（标签名却还写着原用户）。
	spec       string
	id         string
	cmd        *exec.Cmd
	pty        *os.File
	hist       *os.File
	createdAt  time.Time
	lastActive time.Time

	// username 是会话实际运行用户的显示名（root / NAS 用户名 / 应用 APP NAME），
	// 经 /api/sessions 的 user 字段下发，用于标签上的「终端 N:<用户>」标注。
	username string

	// histMu 串行化“追加历史 + 广播到已挂载连接”：回放历史与实时追加在同一把
	// 锁内完成，保证不存在重复或丢失的字节；向连接的写也在锁内（带写超时，见 pump）。
	histMu sync.Mutex
	conn   net.Conn
	connMu sync.Mutex

	exited bool
	// exitedCh 在 pump 结束（shell 退出、输出读尽）时关闭，供到期回收协程等待。
	exitedCh chan struct{}
	mu       sync.Mutex
	closed   bool
}

// errNotOwner：请求来自已不是操作端的连接（已被其他设备顶掉的旧连接）。
// 上层据此静默忽略：它只是残留帧，不是错误。
var errNotOwner = errors.New("not session owner")

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
// **解析失败一律返回错误，不静默回退**：曾经把无法识别的 user= 值当成「当前登录用户」，
// 于是拼错参数（或网关给了非数字的 X-Trim-Userid）会悄悄拿到后端进程用户的 shell
// （部署时即 root），调用方却以为一切正常。前端只会发 nas/root/app:<NAME>，
// 所以这里收紧不会影响既有交互。
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
	fromHeader := false
	switch userSpec {
	case "", "nas":
		uidStr, fromHeader = trimUID, true
	default:
		uidStr = userSpec
	}
	uid := os.Getuid()
	if uidStr != "" {
		n, err := strconv.Atoi(uidStr)
		if err != nil || n < 0 {
			if fromHeader {
				return nil, fmt.Errorf("invalid X-Trim-Userid %q", uidStr)
			}
			return nil, fmt.Errorf("invalid user spec %q (want nas / root / app:<APP NAME> / <uid>)", userSpec)
		}
		uid = n
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
		// 既查不到 /etc/passwd 条目、也没有可继承的 HOME：用根目录（必然存在）而不是
		// 拼一个不存在的 /home/<uid>，否则 PTY 会因 cmd.Dir 无效而直接启动失败。
		home = "/"
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
				s.writeConn(c, wsFrame(opText, chunk))
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
		s.writeConn(c, wsFrame(opText, note))
		s.writeConn(c, wsFrame(opText, []byte("\x1b]exit\x07")))
	}
	s.histMu.Unlock()
	close(s.exitedCh)
}

// writeFramed 写一帧并带上写超时（见 connWriteTimeout）。失败由调用方决定怎么处理：
// 已挂载连接走 writeConn（解挂载），挂载中的回放直接把错误返回给 WS handler。
func writeFramed(c net.Conn, frame []byte) error {
	_ = c.SetWriteDeadline(time.Now().Add(connWriteTimeout))
	_, err := c.Write(frame)
	return err
}

// writeConn 在 histMu 内（由调用方持有）向已挂载连接写一帧，带写超时。
// 超时/出错说明该端已不读数据：解除挂载（后续广播直接跳过它）+ 关掉这个连接
// （让对端若还活着就能立刻看到「连接已关闭」，而不是停在「看着连着、其实没输出」），
// 绝不让一次失联的写把 histMu 长期占住——否则同会话的 attach/history 会被一起堵死。
func (s *Session) writeConn(c net.Conn, frame []byte) {
	if err := writeFramed(c, frame); err != nil {
		s.detach(c)
		_ = c.Close()
	}
}

// attach 把连接挂载到会话：先回放历史文件内容（上限 maxHistoryBytes，分帧发送），
// 再发送 ready 标记，最后接管实时流。整段在 histMu 内完成，防止新增输出既被回放
// 又被实时广播造成重复。
//
// **单挂载点语义**：一个会话在任何时刻只允许一个操作端（能写输入、能改 PTY 尺寸）。
// 挂载成功即成为操作端，返回值是**被顶掉的旧连接**（无则 nil），由调用方负责通知并
// 关闭它（terminal.go 的 kickDetached）——只影响这一个会话，其他会话的连接不动。
func (s *Session) attach(c net.Conn) (net.Conn, error) {
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
						// 回放同样带写超时：新端不读数据时不能让 histMu 一直被占着
						if werr := writeFramed(c, wsFrame(opText, chunk[:n])); werr != nil {
							return nil, werr
						}
					}
					if rerr != nil {
						break
					}
				}
			}
		}
	}
	if err := writeFramed(c, wsFrame(opText, []byte("\x1b]ready\x07"))); err != nil {
		return nil, err
	}
	// 换主与历史回放同处一个 histMu 临界区：新连接既不漏字节，也不与广播交叉。
	s.connMu.Lock()
	prev := s.conn
	s.conn = c
	s.connMu.Unlock()
	if prev == c {
		prev = nil // 重复挂载同一连接：无需顶号
	}
	return prev, nil
}

// isOwner 判断该连接是否仍是会话的操作端（被顶掉的旧连接为 false）。
func (s *Session) isOwner(c net.Conn) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn != nil && s.conn == c
}

// detach 解除当前连接挂载（不杀会话，会话继续运行并写入历史文件）。
func (s *Session) detach(c net.Conn) {
	s.connMu.Lock()
	if s.conn == c {
		s.conn = nil
	}
	s.connMu.Unlock()
}

// writeInput 把客户端输入写入 PTY。**只有当前操作端能写**：被顶掉的旧连接即使还有
// 残留帧抵达，也在服务端被丢弃——否则两台设备会同时操作同一个 PTY（两边都能打字，
// 却只有一边看得到输出）。
func (s *Session) writeInput(c net.Conn, p []byte) error {
	if !s.isOwner(c) {
		return errNotOwner
	}
	s.markActive()
	_, err := s.pty.Write(p)
	return err
}

// resize 调整 PTY 尺寸。同样只接受操作端的请求：被顶掉的旧设备随后会触发一次
// 尺寸重排，若不加限制就会把 PTY 改成它自己的窗口大小。
func (s *Session) resize(c net.Conn, cols, rows uint16) error {
	if !s.isOwner(c) {
		return errNotOwner
	}
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
		// Kill 一定会让 Wait 返回「被信号杀死」的 *exec.ExitError —— 那是**预期**的终止
		// 结果，不是清理失败。曾把它当错误上抛，导致 DELETE /api/session 对运行中的
		// 会话一律回 404（会话其实已经删掉了）。只有真正的清理失败才返回错误。
		_ = s.cmd.Process.Kill()
		var exitErr *exec.ExitError
		if err := s.cmd.Wait(); err != nil && !errors.As(err, &exitErr) && firstErr == nil {
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
func (m *SessionManager) create(runAs *runUser, spec string) (*Session, error) {
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
		spec:       spec,
		cmd:        cmd,
		pty:        f,
		hist:       hist,
		createdAt:  time.Now(),
		lastActive: time.Now(),
		username:   runAs.username,
		exitedCh:   make(chan struct{}),
	}
	go s.pump()
	go m.reapWhenExited(s)
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

// reapWhenExited 在会话的 shell 退出后再等 exitedGrace 就回收它（PTY/进程 + 历史文件）。
// 已退出的会话没有任何交互价值（PTY 已死，输入只会得到 EIO），不回收就会在进程生命周期
// 内无限堆积。宽限期内历史文件仍在，前端此时挂载仍能回放（只是不会再恢复成标签）。
// 会话被用户显式关闭（DELETE）时这里同样会醒来：那时它已不在表里（或已关闭），直接返回。
func (m *SessionManager) reapWhenExited(s *Session) {
	<-s.exitedCh
	if s.isClosed() {
		return
	}
	time.Sleep(exitedGrace)
	if cur, ok := m.get(s.id); ok && cur == s && !s.isClosed() {
		m.closeByID(s.id)
	}
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
			UserSpec:   s.spec,
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