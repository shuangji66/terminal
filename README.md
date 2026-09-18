# Terminal — 本地 Web 终端

一个**本地终端**应用：Go 后端 + Vue 3 前端，仅通过 **Unix Socket** 访问（nginx 等
前置服务把 HTTP/WS 反代到该 socket 的 baseurl 前缀），类似 dsh 控制台的 Web 终端，
但独立运行、无侧边栏与底栏，聚焦多标签终端体验。

> 参考自 `dsh` 目录控制台项目的终端实现（`dsh/backend/terminal.go`、
> `dsh/frontend/src/views/TerminalView.vue` 与 `genesis-DESIGN.md` 设计语言）。

---

## 功能特性

- **多标签终端** — 顶部标签栏「+」常驻左侧新开会话（每个标签一个独立 PTY 会话）；
  标签条支持**鼠标拖动 / 滚轮横向滚动**，双击标签可重命名，关闭按钮常驻显示
  （已有会话关闭需二次确认）；全部标签关闭后自动新开。
- **启动用户切换** — 设置弹窗选择新会话以哪个用户进入 bash：
  - `当前登录用户`（默认，网关 `X-Trim-Userid` 指定的 NAS 用户）
  - `ROOT`（切换需二次确认）
  - `自定义`：启动无会话可恢复时固定以登录用户建立会话；此后**每次新建终端弹窗选择**
    登录用户 / ROOT / **应用用户**。偏好持久化到 `TERMINAL_USER_MODE_FILE`。有会话可
    恢复时保持原会话用户。
- **以应用用户启动（NAS 应用）** — 自定义模式的弹窗会列出本机已安装应用：
  后端执行 `appcenter-cli list` 解析 **APP NAME**（即系统里的应用用户名），
  过滤系统软件（`trim.*`）与没有同名系统用户 / 家目录的应用，按名称排序后返回；
  选中即以该用户进入 bash，**`HOME` 与工作目录都设为应用家目录
  `/var/apps/<APP NAME>/home`**（模板可经 `TERMINAL_APP_HOME_TEMPLATE` 覆盖），
  因此进入后 `~` 即该目录。系统不会给应用用户设置 HOME，必须由本终端赋予。
  后端需以 root 运行方可 setuid 到应用用户。
- **会话持久化与恢复** — 每个会话的终端输出实时镜像到**临时文件**（目录来自环境变量
  `TERMINAL_SESSION_DIR`），**应用停止时整目录自动清除**；前端刷新后从临时文件恢复
  会话历史并重新挂到原会话。**清屏同步清空**该临时历史文件，重连/刷新不再回放旧内容。
- **搜索** — 桌面端顶栏搜索按钮（移动端屏蔽）弹出悬浮搜索框：输入即增量搜索，
  支持 ↑/↓ 或 Enter/Shift+Enter 跳转，显示匹配计数（如 `2/7`），黄（普通）/橙（当前）
  高亮，关闭时清除高亮；基于 xterm SearchAddon。
- **功能名折叠/展开** — 桌面端《》按钮常驻搜索按钮左侧手动折叠/展开功能名
  （搜索/粘贴/清屏/重连/快捷指令/设置），标签溢出（标签页变多）时自动收起；
  展开仅手动触发（展开时标签可视区变窄属预期）。
- **鼠标选中自动复制** — 桌面端框选/双击选词后自动复制进剪贴板并 toast 提示，
  移动端由系统文字工具取代复制按钮（点击/长按终端文本即调起原生粘贴/选择菜单，
  并桥接输入到会话，可正常键入）；复制按钮已完全移除。
- **快捷指令** — 终端快捷命令持久化到文件（`TERMINAL_QUICK_CMDS_FILE`），弹窗顶部
  提供「新增 / 关闭」，支持编辑 / 删除 / 一键执行（可勾选自动回车执行），执行后光标
  聚焦回终端。
- **设置弹窗** — 顶部一行「主题（图标+当前模式，点击循环浅色/深色/跟随系统）与语言」；
  启动用户（三选项一行）；终端字号（左减右加 10–26，存浏览器 localStorage 即时生效）；
  末尾「关于」极简技术栈 + GitHub 链接。
- **终端配色** — 深色模式黑底绿字（绿色略暗淡，`#2bd957`）；浅色模式米白底黑字；
  顶栏浅色为温和白色，与终端米白错开。
- **移动端辅助功能键** — 两行胶囊按键条：第一行 ESC | ↑ | Tab | Ctrl | Alt | Shift |
  Ins，第二行 ← | ↓ | → 与 ` . / - = "`；方向键支持长按连发；修饰键（Ctrl/Alt/Shift）
  按下后变色，**输入一次键盘输入后自动解除**。
- **点击/长按调起系统文字工具** — 移动端点击或长按终端文本即调起**系统文字工具**
  （通过隐藏 textarea 获得原生文本菜单，同时桥接输入/粘贴到会话，菜单调起后仍可正常键入）。
- **虚拟键盘适配（移动端）** — 键盘弹起时整个界面跟随收缩：底部辅助键条整体上抬到键盘之上，
  终端区域自动变小并重排列数（下发 `resize` 给 PTY），键盘收起后原样恢复。实现上监听
  `visualViewport` 把可视视口几何写入 CSS 变量驱动壳层高度（`--vvh`）与位移（`--vvt`），
  因此 **iOS Safari 同样生效**——`interactive-widget=resizes-content` 只被
  Chrome/Firefox for Android 支持，Safari 至今未实现，仅靠 `100dvh` 在 iOS 上不会跟随。
  键盘弹起时底部安全区（home indicator）已被键盘覆盖，键条内边距同步归零，不留多余空隙。
- **移动端单指拖动滚动（含 iOS）** — 在终端区域直接上下滑动即可翻阅回滚历史：拖动终端内容
  滚动（超过 10px 判定为滚动，轻触/长按仍调起系统文字工具），也可拖动右侧滚动条（触屏下常驻
  可见、热区加宽，见下）。xterm v6 的滚动改由 JS 驱动，`.xterm-viewport` 已无原生可滚动内容，
  因此拖动由前端把位移换算成行数调用 `scrollLines()`；容器设 `touch-action: none` 并给滚动条
  `touch-action: none`，避免手势被浏览器认领为整页平移（iOS 上就是「页面抖动/橡皮筋回弹」）。
  翻阅历史时后台新输出不会把视图顶回底部（`stickToBottom`：仅跟随底部或用户主动输入时回底）。
- **WebSocket + PTY** — `creack/pty` 交互式 bash，OSC 控制消息处理 resize 与心跳保活。
- **仅 unix socket 访问** — 无 TCP 监听；baseurl 由后端运行时注入 `<base href>`，
  前端资源与 API/WS 统一基于 `document.baseURI` 解析。

---

## 技术栈

| 层 | 技术 |
| --- | --- |
| 后端 | Go ≥ 1.27（标准库 + `github.com/creack/pty`，按 uid 切换用户） |
| 前端 | Vue 3.5（Composition API / `<script setup>`）+ TypeScript 6 + Vite 8 |
| 前端构建 | Tailwind CSS v4（`@tailwindcss/vite`）、Pinia 4、xterm.js v6（fit/webgl/search/web-links/clipboard/unicode11/serialize/image） |
| 类型检查 | `npm run type-check`（`vue-tsc --noEmit`，覆盖 `.vue` 的 script 与 template + 全部 `.ts`；锁定 TypeScript 6.x，TS 7 与 vue-tsc 不兼容） |
| 通信 | Unix Socket、HTTP JSON API、WebSocket（终端） |
| 运行用户 | **root**（后端为 root 方可 setuid 以 NAS 用户/root 运行会话） |

---

## 目录结构

```
.
├── Makefile                 # make dev / release / clean（前端→embed→Go 单二进制）
├── build.sh                 # 等价构建脚本
├── DESIGN.md                # 设计规范（源自 dsh genesis-DESIGN.md）
├── AGENTS.md                # AI 代理 / 开发协作指引
├── backend/                 # Go 后端
│   ├── main.go              # 入口：环境解析、unix socket 服务、优雅退出（清会话临时目录）
│   ├── config.go            # 运行时环境（环境变量解析）
│   ├── admin.go             # Admin mux：SPA、API 路由、baseurl 注入
│   ├── sessions.go          # 会话管理：PTY 会话（按用户运行/临时历史文件/挂载回放/清空/关闭）
│   ├── terminal.go          # WebSocket 终端处理器（新建会话用户解析、挂载、resize、心跳）
│   ├── quickcmds.go         # 快捷指令持久化 API
│   ├── usermode.go          # 启动用户模式（nas | root | custom）持久化 API
│   └── apps.go              # 应用用户：appcenter-cli list 解析/过滤，app:<APP NAME> 用户解析
└── frontend/                # Vue 3 前端
    ├── index.html           # 注入 <base> 由后端运行时改写
    ├── vite.config.ts       # 相对 base（./assets/...）+ @tailwindcss/vite
    └── src/
        ├── main.ts / App.vue / style.css
        ├── i18n/            # zh / en 文案
        ├── composables/     # useTheme / useI18n
        ├── stores/          # Pinia：sessions / settings / quickCmds / toast / paneControls
        ├── serverapi/       # 运行时 baseurl 感知的 API / WS 客户端
        └── components/      # TabBar / TerminalPane / KeypadBar / SettingsDialog /
                             # UserPickDialog / QuickCmds*Dialog / ConfirmDialog / Toast
```

---

## 构建

> 前置：Go ≥ 1.27、Node.js ≥ 24。

构建会把前端 `dist` 拷入 `backend/embed`，再用 `//go:embed` 打成一个**单文件自包含**
Go 二进制 `backend/terminal`。

```bash
make            # 开发构建（默认）
make release V=1.0.1   # Release 构建 + 版本号
make clean      # 清理构建产物
./build.sh      # 等价脚本（默认 dev）
./build.sh release 1.0.1

cd frontend && npm run dev        # 仅前端热更（配合后端调试）
cd frontend && npm run build      # 前端构建
cd frontend && npm run type-check # 类型检查（vue-tsc：.ts + .vue 的 script 与 template）
```

---

## 运行 / 环境变量

后端**只**监听 Unix Socket，由 nginx / caddy 等把 HTTP 与 WS 反代到该 socket 的
baseurl 前缀下；也可直接 `curl --unix-socket` 访问或本地反代到 127.0.0.1 调试。

| 环境变量 | 说明 | 默认 |
| --- | --- | --- |
| `TERMINAL_ADMIN_SOCK` | Unix socket 路径（唯一访问入口） | `$TMPDIR/terminal/app.sock` |
| `TERMINAL_ADMIN_BASEURL` | 前端资源 baseurl 前缀（空 = 根路径） | 空 |
| `TERMINAL_QUICK_CMDS_FILE` | 快捷指令持久化文件 | `$TMPDIR/terminal/quickcmds.json` |
| `TERMINAL_SESSION_DIR` | 终端会话临时目录（**应用停止时整目录清除**） | `$TMPDIR/terminal/sessions` |
| `TERMINAL_USER_MODE_FILE` | 启动用户模式（`nas`\|`root`\|`custom`）持久化文件 | `$TMPDIR/terminal/user-mode.json` |
| `TERMINAL_APP_HOME_TEMPLATE` | 应用用户 HOME / 工作目录模板（`%s` = APP NAME） | `/var/apps/%s/home` |
| `TERMINAL_APPCENTER_CLI` | 列出已安装应用的命令（失败时回退扫描应用根目录） | `appcenter-cli` |
| `TERMINAL_SHELL` | 终端使用的 shell | `/bin/bash` |

新终端会话以「当前登录用户」为默认：网关（nginx 等）在反代 unix socket 时附加
`X-Trim-Userid: <uid>` 请求头，后端按该 uid 运行会话（非 root 进程无法 setuid，因此
后端需以 root 启动）；`ROOT` 模式下新会话以 root 运行。

### 应用用户（NAS 应用）的前置条件

以应用用户（`app:<APP NAME>`）启动会话时：

- **后端必须以 root 运行**：需要 `setuid` 到应用用户，非 root 会 `permission denied`。
- **应用列表来自 `appcenter-cli list`**：该命令需要 `OfficialAppUsers` 组权限（socket
  `/run/trim_app_cgi/rpcbroker`），普通用户直接执行会 panic。命令失败（未安装 / 无权限 /
  输出为空）时后端记录日志并**回退扫描应用根目录**（由 `TERMINAL_APP_HOME_TEMPLATE`
  反推，默认 `/var/apps`），功能不至于完全不可用。
- **应用用户的家目录必须存在**：它同时作为会话的 `cmd.Dir` 与 `HOME`，不存在会让
  `pty.Start` 失败，该应用会从列表中过滤掉。
- **系统不会给应用用户设 HOME**（`/etc/passwd` 里的 `/home/<app>` 并不存在），
  因此由本终端赋予 `HOME=/var/apps/<APP NAME>/home`；`HOME` 与 `cmd.Dir` 取同一路径，
  进入 bash 后 `~` 即该目录。
- **`HOME`/`PWD` 会从继承环境中剔除后再写入**：环境变量重复时后者生效，而父进程
  （appcenter 脚本经 bash 启动）自带 `HOME`/`PWD`，若不清除会顶掉目标用户的值，
  导致 `HOME` 错误、且符号链接家目录下提示符显示完整物理路径而非 `~`。

以 root 运行，例如：

```bash
export TERMINAL_ADMIN_SOCK=/tmp/terminal/app.sock
export TERMINAL_ADMIN_BASEURL=/app/terminal
./backend/terminal
```

nginx 反代示例（HTTP + WebSocket 都转发到 socket，并附加 NAS 用户头）：

```nginx
location /app/terminal/ {
    proxy_pass http://unix:/tmp/terminal/app.sock:/;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header X-Trim-Userid $uid;   # 网关传递当前 NAS 用户 uid
}
```

---

## HTTP API

| 方法 & 路径 | 说明 |
| --- | --- |
| `GET /api/info` | 运行时信息（socket/baseurl/会话目录/用户模式文件/shell/home/版本/nasUser/trimUid/currentUid） |
| `GET /api/user-mode` | 启动用户模式（`nas`\|`root`\|`custom`，含 `nasUser`） |
| `POST /api/user-mode` | 持久化启动用户模式（非法值 400） |
| `GET /api/sessions` | 活动会话列表（id/创建时间/最近活动/历史大小/是否退出） |
| `GET /api/apps` | 可选的应用用户列表（`appcenter-cli list` 的 APP NAME，已过滤 `trim.*` 与不可用项） |
| `GET /api/session/history?id=` | 会话历史内容（取末尾 ≤4MB） |
| `POST /api/session/clear?id=` | **清空**会话历史临时文件（前端"清屏"同步调用） |
| `DELETE /api/session?id=` | 终止会话（唯一终止路径；关闭标签页时调用） |
| `GET /api/quickcmds` / `POST /api/quickcmds` | 快捷指令列表 / 整体保存（原子写） |

---

## 终端协议（前端 ↔ 后端）

- **连接**：`WS /terminal?id=<id>`；`id` 为空时后端新建会话，并先回一帧
  `\x1b]id;<id>\x07` 通知前端取得会话 id；`id` 存在则先回放历史文件内容（≤4MB，
  每帧 ≤32KB），再发 `\x1b]ready\x07` 后进入实时流。
- **新建会话的用户**：`WS /terminal?user=root` → 以 root 运行；`user=app:<APP NAME>` →
  以该 NAS 应用用户运行（`HOME` 与工作目录均为 `/var/apps/<APP NAME>/home`）；
  不带 `user` 参数 → 以网关 `X-Trim-Userid` 指定的 NAS 用户运行；`user=<uid>` → 指定 uid。
  解析失败（应用名非法 / 无同名系统用户 / 家目录不存在）时回一帧错误文本并关闭连接。
- **数据**：文本帧双向；`\x1b]resize;<cols>;<rows>\x07` 调整 PTY 尺寸；
  `\x1b]ping\x07` 心跳不写入 PTY；进程退出时后端发 `\x1b]exit\x07` 控制帧。
- **关闭**：浏览器断开只解除挂载、**不杀会话**（会话继续在服务器端运行并写入历史
  文件），刷新页面后重新挂载即恢复；关闭标签页调用 `DELETE /api/session?id=` 才终止。

---

## 许可

[MIT](LICENSE)