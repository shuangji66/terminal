package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// QuickCmd is a user-defined terminal quick command persisted to a JSON file
// whose path is resolved from an environment variable (TERMINAL_QUICK_CMDS_FILE).
// The frontend manages the full list and saves it as a whole on each change.
type QuickCmd struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Auto    bool   `json:"auto"`
}

// quickCmdsFile is the on-disk envelope. A version field keeps the format
// forward-compatible.
type quickCmdsFile struct {
	Version  int        `json:"version"`
	Commands []QuickCmd `json:"commands"`
}

// maxQuickCmdsBody 是保存请求体的上限（整表提交，正常只有几 KB；没有上限时一个
// 客户端可以用超大 body 把进程内存打爆 —— 该进程以 root 运行且承载全部会话）。
const maxQuickCmdsBody = 1 << 20 // 1MB

// quickCmdsMu serializes concurrent reads/writes of the persisted file.
var quickCmdsMu sync.Mutex

func (m *AdminMux) handleGetQuickCmds(w http.ResponseWriter, r *http.Request) {
	cmds, err := loadQuickCmds(m.renv.QuickCmdsFile)
	if err != nil {
		writeErr(w, "failed to load quick commands: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "path": m.renv.QuickCmdsFile, "commands": cmds})
}

func (m *AdminMux) handleSaveQuickCmds(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxQuickCmdsBody)
	var req struct {
		Commands []QuickCmd `json:"commands"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		writeErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	cmds := make([]QuickCmd, 0, len(req.Commands))
	seen := map[string]bool{}
	for _, c := range req.Commands {
		c.Name = strings.TrimSpace(c.Name)
		c.Content = strings.TrimSpace(c.Content)
		if c.ID == "" || c.Name == "" || c.Content == "" {
			writeErr(w, "quick command requires id, name and content", http.StatusBadRequest)
			return
		}
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		cmds = append(cmds, c)
	}
	if err := saveQuickCmds(m.renv.QuickCmdsFile, cmds); err != nil {
		writeErr(w, "failed to save quick commands: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "path": m.renv.QuickCmdsFile, "commands": cmds})
}

// loadQuickCmds reads the persisted file. A missing file yields an empty list.
// It also tolerates a legacy bare-array format.
func loadQuickCmds(path string) ([]QuickCmd, error) {
	quickCmdsMu.Lock()
	defer quickCmdsMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []QuickCmd{}, nil
		}
		return nil, err
	}
	var f quickCmdsFile
	if err := json.Unmarshal(data, &f); err != nil {
		var bare []QuickCmd
		if err2 := json.Unmarshal(data, &bare); err2 == nil {
			if bare == nil {
				return []QuickCmd{}, nil
			}
			return bare, nil
		}
		return nil, err
	}
	if f.Commands == nil {
		return []QuickCmd{}, nil
	}
	return f.Commands, nil
}

// saveQuickCmds atomically writes the command list (tmp file + rename).
func saveQuickCmds(path string, cmds []QuickCmd) error {
	quickCmdsMu.Lock()
	defer quickCmdsMu.Unlock()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(quickCmdsFile{Version: 1, Commands: cmds}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}