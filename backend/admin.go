package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// AdminMux serves the SPA, JSON API and terminal WebSocket on the unix admin
// socket, under the configured baseurl prefix (which is also from env).
type AdminMux struct {
	renv     *RuntimeEnv
	sessions *SessionManager
	spa      http.Handler
}

func newAdminMux(renv *RuntimeEnv) *AdminMux {
	return &AdminMux{
		renv:     renv,
		sessions: NewSessionManager(renv),
	}
}

// writeJSON writes an indented JSON response.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// writeErr writes a JSON error body with the given status.
func writeErr(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": msg})
}

// sessionInfo is the JSON summary of a live session.
type sessionInfo struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"createdAt"`
	LastActive string `json:"lastActive"`
	Size       int64  `json:"size"`
	Exited     bool   `json:"exited"`
}

// SetSPA attaches the embedded frontend assets to the admin mux.
func (m *AdminMux) SetSPA(fsys fs.FS) {
	m.spa = spaHandler(fsys, m.renv.AdminBaseURL)
}

// --- API handlers ---

func (m *AdminMux) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"ok": true,
		"runtime": map[string]interface{}{
			"adminSock":     m.renv.AdminSock,
			"adminBaseURL":  m.renv.AdminBaseURL,
			"sessionDir":    m.renv.SessionDir,
			"quickCmdsFile": m.renv.QuickCmdsFile,
			"userModeFile":  m.renv.UserModeFile,
			"shell":         m.renv.Shell,
			"home":          m.renv.Home,
			"version":       m.renv.Version,
			"lang":          m.renv.Lang,
			// 网关 unix sock 传递的当前 NAS 用户（X-Trim-Userid），缺失时回退进程用户
			"trimUid":    r.Header.Get("X-Trim-Userid"),
			"nasUser":    nasUserInfo(m.renv, r.Header.Get("X-Trim-Userid")),
			"currentUid": os.Getuid(),
		},
	})
}

// nasUserInfo 解析网关通过 X-Trim-Userid 传递的当前 NAS 用户信息。
func nasUserInfo(renv *RuntimeEnv, trimUID string) map[string]interface{} {
	ru, err := resolveRunUser(renv, "nas", trimUID)
	if err != nil {
		return nil
	}
	return map[string]interface{}{
		"uid":      ru.uid,
		"gid":      ru.gid,
		"username": ru.username,
		"home":     ru.home,
	}
}

// handleGetUserMode 读取持久化的启动用户模式（nas | root）。
func (m *AdminMux) handleGetUserMode(w http.ResponseWriter, r *http.Request) {
	mode, err := loadUserModeFile(m.renv.UserModeFile)
	if err != nil {
		writeErr(w, "failed to load user mode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":          true,
		"mode":        mode,
		"path":        m.renv.UserModeFile,
		"defaultMode": "nas",
		"nasUser":     nasUserInfo(m.renv, r.Header.Get("X-Trim-Userid")),
	})
}

// handleSaveUserMode 持久化启动用户模式（nas | root）。
func (m *AdminMux) handleSaveUserMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !validUserModes[req.Mode] {
		writeErr(w, "invalid mode, must be nas or root", http.StatusBadRequest)
		return
	}
	if err := saveUserModeFile(m.renv.UserModeFile, req.Mode); err != nil {
		writeErr(w, "failed to save user mode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	logger().Printf("[usermode] saved mode=%s path=%s", req.Mode, m.renv.UserModeFile)
	writeJSON(w, map[string]interface{}{"ok": true, "mode": req.Mode, "path": m.renv.UserModeFile})
}

func (m *AdminMux) handleSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{"ok": true, "sessions": m.sessions.list()})
}

// handleAppUsers 返回「以应用用户启动」弹窗的可选列表：appcenter-cli list 的
// APP NAME，已过滤系统软件（trim.*）与没有系统用户/家目录的应用。
func (m *AdminMux) handleAppUsers(w http.ResponseWriter, r *http.Request) {
	apps, err := listAppUsers(m.renv)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":           true,
		"apps":         apps,
		"homeTemplate": m.renv.AppHomeTpl,
	})
}

func (m *AdminMux) handleSessionHistory(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, "missing id", http.StatusBadRequest)
		return
	}
	data, err := m.sessions.history(id, maxHistoryBytes)
	if err != nil {
		writeErr(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "id": id, "size": len(data), "content": string(data)})
}

func (m *AdminMux) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, "missing id", http.StatusBadRequest)
		return
	}
	if err := m.sessions.closeByID(id); err != nil {
		writeErr(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "id": id})
}

func (m *AdminMux) handleClearSession(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, "missing id", http.StatusBadRequest)
		return
	}
	if err := m.sessions.clearHistory(id); err != nil {
		writeErr(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "id": id})
}

// spaHandler serves the embedded SPA assets.
func spaHandler(fsys fs.FS, baseurl string) http.Handler {
	sub, err := fs.Sub(fsys, "embed")
	if err == nil {
		fsys = sub
	}
	base := strings.TrimRight(baseurl, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if base != "" && strings.HasPrefix(p, base) {
			p = strings.TrimPrefix(p, base)
			if p == "" {
				p = "/"
			}
		}
		reqPath := strings.TrimPrefix(p, "/")
		if reqPath == "" {
			reqPath = "index.html"
		}
		// 优先直接提供存在的文件；首页始终注入 base href。
		if b, err := fs.ReadFile(fsys, reqPath); err == nil {
			if reqPath == "index.html" && base != "" {
				b = rewriteIndexBase(b, base)
			}
			serveBytes(w, reqPath, b)
			return
		}
		// 不存在的路径回退到 index.html 以支持 SPA 前端路由
		if b, err := fs.ReadFile(fsys, "index.html"); err == nil {
			if base != "" {
				b = rewriteIndexBase(b, base)
			}
			serveBytes(w, "index.html", b)
			return
		}
		http.NotFound(w, r)
	})
}

func rewriteIndexBase(body []byte, base string) []byte {
	s := string(body)
	s = strings.ReplaceAll(s, `src="./assets/`, `src="`+base+`/assets/`)
	s = strings.ReplaceAll(s, `href="./assets/`, `href="`+base+`/assets/`)
	headTag := `<base href="` + base + `/">`
	if idx := strings.Index(strings.ToLower(s), "<head"); idx != -1 {
		if ci := strings.Index(s[idx:], ">"); ci != -1 {
			pos := idx + ci + 1
			return []byte(s[:pos] + headTag + s[pos:])
		}
	}
	return []byte(headTag + s)
}

func serveBytes(w http.ResponseWriter, name string, b []byte) {
	ct := "text/plain; charset=utf-8"
	switch {
	case strings.HasSuffix(name, ".html"):
		ct = "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		ct = "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		ct = "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		ct = "application/json; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		ct = "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		ct = "image/png"
	case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
		ct = "image/jpeg"
	case strings.HasSuffix(name, ".ico"):
		ct = "image/x-icon"
	case strings.HasSuffix(name, ".woff2"):
		ct = "font/woff2"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(200)
	w.Write(b)
}

// buildHandler wires all routes. The baseurl prefix is stripped before matching.
func (m *AdminMux) buildHandler() http.Handler {
	base := strings.TrimRight(m.renv.AdminBaseURL, "/")
	term := &terminalHandler{mgr: m.sessions}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if base != "" && strings.HasPrefix(p, base) {
			p = strings.TrimPrefix(p, base)
			if p == "" {
				p = "/"
			}
		}
		r.URL.Path = p

		if p == "/terminal" && isWebSocket(r) {
			term.ServeHTTP(w, r)
			return
		}

		switch {
		case p == "/api/info" && r.Method == http.MethodGet:
			m.handleInfo(w, r)
		case p == "/api/sessions" && r.Method == http.MethodGet:
			m.handleSessions(w, r)
		case p == "/api/apps" && r.Method == http.MethodGet:
			m.handleAppUsers(w, r)
		case p == "/api/session/history" && r.Method == http.MethodGet:
			m.handleSessionHistory(w, r)
		case p == "/api/session" && r.Method == http.MethodDelete:
			m.handleCloseSession(w, r)
		case p == "/api/session/clear" && r.Method == http.MethodPost:
			m.handleClearSession(w, r)
		case p == "/api/quickcmds" && r.Method == http.MethodGet:
			m.handleGetQuickCmds(w, r)
		case p == "/api/quickcmds" && r.Method == http.MethodPost:
			m.handleSaveQuickCmds(w, r)
		case p == "/api/user-mode" && r.Method == http.MethodGet:
			m.handleGetUserMode(w, r)
		case p == "/api/user-mode" && r.Method == http.MethodPost:
			m.handleSaveUserMode(w, r)
		default:
			m.serveSPA(w, r, spaPath(p))
		}
	})
	return mux
}

func (m *AdminMux) serveSPA(w http.ResponseWriter, r *http.Request, name string) {
	if m.spa == nil {
		http.Error(w, "frontend not embedded", http.StatusNotFound)
		return
	}
	m.spa.ServeHTTP(w, r)
}

func spaPath(p string) string {
	if p == "/" || p == "" {
		return "/index.html"
	}
	if path.Ext(p) == "" {
		return "/index.html"
	}
	return p
}

func isWebSocket(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	up := strings.ToLower(r.Header.Get("Upgrade"))
	return up == "websocket"
}

// serveAdminSocket listens on the unix admin socket (the only access point).
func serveAdminSocket(m *AdminMux) error {
	renv := m.renv
	os.Remove(renv.AdminSock)
	if dir := filepath.Dir(renv.AdminSock); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	ln, err := netListen("unix", renv.AdminSock)
	if err != nil {
		return err
	}
	if e := os.Chmod(renv.AdminSock, 0o660); e != nil {
		logger().Printf("admin socket chmod: %v", e)
	}
	h := m.buildHandler()
	srv := &http.Server{Handler: h}
	go func() {
		<-stopCh
		srv.Close()
	}()
	logger().Printf("terminal listening on unix socket %s baseurl %q", renv.AdminSock, renv.AdminBaseURL)
	return srv.Serve(ln)
}