package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件负责「以 NAS 应用用户（APP NAME）身份启动终端会话」：
//   - 执行 `appcenter-cli list` 解析已安装应用（APP NAME / DISPLAY NAME）
//   - 过滤系统软件（trim.*）与没有对应系统用户 / 家目录的应用
//   - 把 `app:<APP NAME>` 形式的用户标识解析为可 setuid 的 runUser
//
// 应用用户在系统里就是同名系统用户（/etc/passwd，如 Harness:x:817:901::...），
// HOME 统一指向应用家目录（TERMINAL_APP_HOME_TEMPLATE，默认 /var/apps/%s/home）。

// appUserSpecPrefix 是前端传过来的「应用用户」标识前缀（如 user=app:Harness）。
const appUserSpecPrefix = "app:"

// appNamePlaceholder 是应用 HOME（家目录）模板中的应用名占位符。
const appNamePlaceholder = "%s"

// appHomeTemplateDefault / appCenterCLIDefault 是环境变量缺省值的兜底（config.go 已设默认）。
const (
	appHomeTemplateDefault = "/var/apps/%s/home"
	appCenterCLIDefault    = "appcenter-cli"
)

// systemAppPrefix 是系统自带应用前缀：不参与「以应用用户启动」的选择列表。
const systemAppPrefix = "trim."

// appListTTL 缓存 appcenter-cli list 的结果：新建终端会反复弹窗，避免每次都拉起进程。
const appListTTL = 5 * time.Second

// appListTimeout 限制 appcenter-cli 的执行时间，避免其卡死拖住 HTTP 请求。
const appListTimeout = 10 * time.Second

// appEntry 是选择列表里的一条应用（name = APP NAME，也就是应用用户名）。
type appEntry struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
}

// appNameRe 限制可选的应用名（同时用于校验前端传入的 user=app:<name>）。
var appNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ansiRe 用于剥掉 appcenter-cli 输出里可能的终端颜色序列，便于按表格解析。
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// resolveAppRunUser 把应用名解析为目标用户（uid/gid/HOME）。
// 应用家目录取 TERMINAL_APP_HOME_TEMPLATE 展开结果（默认 /var/apps/<APP NAME>/home，
// 软链到 /vol1/@apphome/<APP NAME>）：既是会话的工作目录（cmd.Dir），也是会话的
// HOME——系统不会给应用用户设置 HOME，必须由本终端赋予。
// 该目录必须存在，否则 pty.Start 会因 cmd.Dir 无效直接失败。
func resolveAppRunUser(renv *RuntimeEnv, name string) (*runUser, error) {
	if !appNameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid app user %q", name)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("app %q has no system user", name)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid <= 0 {
		return nil, fmt.Errorf("app %q has invalid uid %q", name, u.Uid)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("app %q has invalid gid %q", name, u.Gid)
	}
	home := appHomeDir(renv, name)
	if !isDir(home) {
		// 家目录是符号链接（/var/apps/<app>/home → /vol1/@apphome/<app>）时同样算存在；
		// 模板目录缺失时才回退到 passwd 里的 HomeDir（通常 /home/<name>，多半不存在）。
		if u.HomeDir == "" || !isDir(u.HomeDir) {
			return nil, fmt.Errorf("app %q home dir %s not found", name, home)
		}
		home = u.HomeDir
	}
	return &runUser{uid: uid, gid: gid, home: home, username: u.Username}, nil
}

// appHomeDir 按模板展开应用家目录（默认 /var/apps/<APP NAME>/home）。
func appHomeDir(renv *RuntimeEnv, name string) string {
	tpl := appHomeTemplateDefault
	if renv != nil && renv.AppHomeTpl != "" {
		tpl = renv.AppHomeTpl
	}
	return strings.ReplaceAll(tpl, appNamePlaceholder, name)
}

// appRootDir 从 HOME 模板反推应用根目录（/var/apps/%s/home → /var/apps），
// 仅用于 appcenter-cli 不可用时的兜底扫描；模板不匹配时返回空串。
func appRootDir(renv *RuntimeEnv) string {
	tpl := appHomeTemplateDefault
	if renv != nil && renv.AppHomeTpl != "" {
		tpl = renv.AppHomeTpl
	}
	if !strings.HasSuffix(filepath.ToSlash(tpl), appNamePlaceholder+"/home") {
		return ""
	}
	probe := filepath.Dir(strings.Replace(tpl, appNamePlaceholder, "probe", 1))
	return filepath.Dir(probe)
}

func isDir(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// --- 应用列表：appcenter-cli list + 兜底扫描 ---

var (
	appListMu  sync.Mutex
	appListAt  time.Time
	appListVal []appEntry
)

// listAppUsers 返回可用于新建终端的应用用户列表（结果短暂缓存）。
func listAppUsers(renv *RuntimeEnv) ([]appEntry, error) {
	appListMu.Lock()
	defer appListMu.Unlock()
	if appListVal != nil && time.Since(appListAt) < appListTTL {
		return appListVal, nil
	}
	raw, err := fetchAppList(renv)
	if err != nil {
		return nil, err
	}
	apps := usableAppUsers(renv, raw)
	appListVal = apps
	appListAt = time.Now()
	return apps, nil
}

// fetchAppList 优先执行 appcenter-cli list；失败（未安装 / 无权限 / 输出为空）时
// 回退扫描应用根目录（见 appRootDir）。
func fetchAppList(renv *RuntimeEnv) ([]appEntry, error) {
	cli := appCenterCLIDefault
	if renv != nil && renv.AppCenterCLI != "" {
		cli = renv.AppCenterCLI
	}
	ctx, cancel := context.WithTimeout(context.Background(), appListTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "list")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	var runErr error
	if err := cmd.Run(); err != nil {
		runErr = err
	} else if apps := parseAppList(stdout.String()); len(apps) > 0 {
		return apps, nil
	} else {
		runErr = errors.New("no app rows in output")
	}
	logger().Printf("[apps] %s list failed: %v (stderr: %s), falling back to directory scan", cli, runErr, strings.TrimSpace(stderr.String()))

	apps, err := scanAppRoot(renv)
	if err != nil {
		return nil, fmt.Errorf("failed to list apps: %v", runErr)
	}
	return apps, nil
}

// scanAppRoot 扫描应用根目录（兜底：命令不可用时）。
func scanAppRoot(renv *RuntimeEnv) ([]appEntry, error) {
	root := appRootDir(renv)
	if root == "" {
		return nil, errors.New("cannot derive app root dir")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	apps := make([]appEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			apps = append(apps, appEntry{Name: e.Name()})
		}
	}
	return apps, nil
}

// parseAppList 解析 appcenter-cli list 的表格输出，取 APP NAME / DISPLAY NAME 两列。
// 表格形如（列以 │ 分隔，分隔行为 ├─┼─┤ 不含 │，因此天然被跳过）：
//
//	┌─────────┬──────────────┐
//	│ APP NAME│ DISPLAY NAME │
//	├─────────┼──────────────┤
//	│ Bond    │ 家庭游戏平台 │
//	└─────────┴──────────────┘
func parseAppList(out string) []appEntry {
	var apps []appEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(ansiRe.ReplaceAllString(line, ""))
		if line == "" || !strings.Contains(line, "│") {
			continue
		}
		cells := strings.Split(line, "│")
		if len(cells) < 3 { // 至少要 [边框] APP NAME │ DISPLAY NAME [边框]
			continue
		}
		name := strings.TrimSpace(cells[1])
		display := strings.TrimSpace(cells[2])
		if name == "" || name == "APP NAME" {
			continue
		}
		// 分隔行（├─┼─┤ 之类）：不同版本的表格渲染可能也带 │，按制表符形态丢弃
		if strings.ContainsAny(name, "─━┄┈┬┼├┤┌┐└┘") {
			continue
		}
		apps = append(apps, appEntry{Name: name, DisplayName: display})
	}
	return apps
}

// usableAppUsers 过滤并排序应用列表：
//   - 去掉系统软件（trim.*）与空名/重名
//   - 去掉无法 setuid（无同名系统用户）或家目录不存在的应用
//   - 按应用名排序，保证弹窗列表稳定
func usableAppUsers(renv *RuntimeEnv, raw []appEntry) []appEntry {
	out := make([]appEntry, 0, len(raw))
	seen := map[string]bool{}
	for _, a := range raw {
		name := strings.TrimSpace(a.Name)
		if name == "" || seen[name] || strings.HasPrefix(name, systemAppPrefix) {
			continue
		}
		seen[name] = true
		if _, err := resolveAppRunUser(renv, name); err != nil {
			continue
		}
		display := strings.TrimSpace(a.DisplayName)
		if display == name {
			display = ""
		}
		out = append(out, appEntry{Name: name, DisplayName: display})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
