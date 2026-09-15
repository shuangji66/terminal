# AGENTS.md — 给 AI 代理与开发者的协作指引

本文档面向在本仓库内修改代码的开发者与 AI 代理（agent），记录**关键约定、易踩的坑
与必须遵守的规则**，以降低误改风险、保持一致风格。风格与 `dsh` 仓库的 AGENTS.md 对齐。

---

## 1. 项目本质

- 本仓库是**本地 Web 终端**：Go 后端 + Vue 3 前端，最终产物是**单个自包含 Go 二进制**
  （前端 `dist` 经 `//go:embed` 打入 `backend/embed` 编译进二进制，产物 `backend/terminal`）。
- 它**只**通过 **Unix Socket** 访问（nginx/caddy 等前置反代把 HTTP/WS 转到该 socket 的
  baseurl 前缀），**无任何 TCP 监听**；baseurl 由后端运行时注入 `<base href>`。
- 部署以 **root** 运行，方能 setuid 以「当前登录用户（`X-Trim-Userid` 指定）」或
  root 运行终端会话。

---

## 2. 构建约定（重要）

- 前端用**相对 base**（`base: './'`）构建，**禁止**把 baseurl 硬编码进产物；真实
  baseurl 由后端在运行时注入 `<base href>`（见 `admin.go` 的 `rewriteIndexBase`）。
- 前端资源/API/WS 路径统一基于 `document.baseURI` 解析（`src/serverapi/index.ts` 的
  `runtimeBase()` / `wsUrl()`），不要依赖构建时的相对 `BASE_URL`。
- `Makefile` 与 `build.sh` 等价：前端 `dist` → `backend/embed` → `go build -o backend/terminal`，
  结束时 `rm -rf backend/embed`（**embed 目录构建后不存在属正常**）。
- 每次前端改动后在说“已生效”之前必须**重新构建**（`make dev`）并验证实际 URL；
  仅改源码不构建不会让已嵌入的二进制更新。
- **版本号经 `-ldflags` 注入**（`make release V=x.y.z` / `-X main.terminalVersion=`），
  不要改代码常量。
- 后端 `go clean -cache` 在每次构建前执行，是刻意行为（保证 embed 资产被编译进去）。

---

## 3. 架构要点与模块职责

**后端（Go，`backend/`）**

- `main.go` — 入口：解析环境 → 建目录（会话临时目录、快捷指令/用户模式文件目录）→
  unix socket 监听 → 等信号优雅退出：终止全部会话、删 socket、**删除会话临时目录**。
- `config.go` — `RuntimeEnv` 全部来自环境变量（`TERMINAL_ADMIN_SOCK/BASEURL`、
  `QUICK_CMDS_FILE`、`SESSION_DIR`、`USER_MODE_FILE`、`SHELL`、HOME/PATH/LANG）。
- `admin.go` — Admin mux：`/api/*` 路由（info / sessions / session / session/clear /
  quickcmds / user-mode）+ SPA（baseurl 注入）+ WebSocket `/terminal` 分发。
- `sessions.go` — 会话管理：每个会话一个 PTY + 历史临时文件（**必须以 `O_RDWR` 打开**，
  否则 attach 回放 / history API 读会 EBADF）；`resolveRunUser` 解析 `user=` 参数与
  `X-Trim-Userid` 头（root / 指定 uid / NAS 用户），非当前 uid 用 `syscall.Credential`
  切换用户；`attach` 在 `histMu` 内“回放历史 + 接管实时流”，保证字节不重不漏；
  pump 协程持续读 PTY → 追加历史文件 + 广播到已挂载连接。
- `terminal.go` — WS 握手（自实现帧编解码）→ `id` 为空则新建（先发 `\x1b]id;<id>\x07`）
  或 `attach`（回放 + `\x1b]ready\x07`）；OSC 控制消息：`resize` / `ping` 不进 PTY；
  客户端断开**只解挂载不杀会话**。
- `quickcmds.go` — 快捷指令整体保存（tmp + rename 原子写，兼容旧版裸数组格式）。
- `usermode.go` — 启动用户模式文件（`nas`|`root`|`custom`，非法回退 `nas`）。

**前端（Vue 3，`frontend/`）**

- **强制 Composition API + `<script setup>` + TypeScript**。
- `App.vue` — 壳：TabBar + 终端面板区（标签用 `visibility` 隐藏以保持尺寸/WS 存活）+
  各弹窗（快捷指令 / 设置 / 确认）；启动顺序：`loadInfo` + `loadUserMode` → `restore`。
- `TabBar.vue` — 顶栏：新建（常驻左侧）、标签条（拖拽/滚轮横滚、双击重命名、常驻
  关闭按钮）、桌面功能名（`labelsOn` 控制显示；《》手动折叠/展开 + 溢出自动收起）、
  移动端第二行功能键；custom 模式新建前弹 `UserPickDialog`。
- `TerminalPane.vue` — 每个标签一个 xterm 实例 + WS；xterm v6 + addons（fit/webgl/
  search/web-links/clipboard/unicode11/serialize/image）；主题（深色黑底绿字 / 浅色米白
  黑字）、字号（来自 settings store）动态应用；会话控制帧处理（`\x1b]id;` /
  `\x1b]ready\x07` / `\x1b]exit\x07`）；搜索悬浮框；鼠标选中自动复制（桌面端）；
  移动端点击/长按文本调起系统文字工具（`openSystemTextTool`，隐藏 textarea 唤起原生菜单
  并桥接输入）；辅助功能键（`KeypadBar`）按键修饰符输入一次后自动解除；
  向 `paneControls` 注册命令入口（paste/clear/reconnect/search/focus/send）。
- `KeypadBar.vue` — 移动端辅助键条（ESC/Tab/Ctrl/Alt/Shift/Ins/←↓→/符号）；
  长按连发；修饰键（Ctrl/Alt/Shift）变色指示按下态，输入一次后自动解除；
  所有非长按键采用 `@click` + `@touchstart.prevent` 双保险确保移动端可靠触发。
- `stores/` — `sessions`（标签 + userSpec + 恢复/关闭）、`settings`（用户模式 + 字号，
  字号存 localStorage）、`quickCmds`、`toast`、`paneControls`（**按标签 uid 的注册表**，
  顶栏按钮通过激活 uid 解析，避免后台标签覆盖）。
- `serverapi/index.ts` — `runtimeBase()` / `wsUrl(id?, user?)` / `api.*`。
- `i18n/zh.ts` `en.ts` — 文案集中管理；`composables/useTheme.ts` `useI18n.ts`。

---

## 4. 必须遵守的规则（Agent 优先）

1. **不要破坏“运行时 baseurl”机制**：前端资源/API/WS 一律走 `runtimeBase()` /
   `document.baseURI`；后端 SPA 服务统一走 `rewriteIndexBase`。
2. **不要硬编码平台路径**：一律经 `TERMINAL_*` 环境变量（socket / baseurl / 各类文件）。
3. **会话用户逻辑不可回归**：新建会话的用户由**标签级 `Tab.userSpec`** 决定
   （`wsUrl` 的 `user=` 参数），而非全局设置；恢复/挂载的会话保持原用户；
   custom 模式每次新建必须弹 `UserPickDialog`。
4. **标签关闭 = 唯一终止会话路径**：调用 `DELETE /api/session?id=`；浏览器断开只是
   “解挂载”，会话继续运行并写历史文件。
5. **清屏必须同步**：前端 `term.clear()` 同时调用 `/api/session/clear` 截断历史文件。
6. **前端状态**：优先 Composition API；共享状态进 Pinia；可复用逻辑进 composables；
   i18n / 主题 / 字号只存 localStorage（字号不持久化到后端）。
7. **v-model 禁止绑定表达式**：如 `v-model:visible="x !== null"` 会编译报错，
   改用 `:visible` + `@update:visible`。
8. **中文注释习惯**：现有代码中文注释为主，新注释保持项目风格。

---

## 5. 常见的坑（Gotchas）

- **历史文件必须以 `O_RDWR` 打开**：`O_WRONLY` 打开后 attach 回放 / history API 读取
  会得到 `bad file descriptor`（EBADF）。
- **PTY 按用户切换依赖 root**：非 root 进程对 `user=root` 或不同 uid 会 `permission
  denied`，属预期；沙箱/CI 中只能验证「当前用户」路径与 API 层。
- **TypeScript 锁 6.x，不要升到 7**：vue-tsc 3.x 的 peer 是 `typescript >=5.0.0`，但 TS7
  移除了 `./lib/tsc`，vue-tsc 依赖它做类型检查，升到 7 会直接跑不起来。当前组合
  `typescript@6.0.3` + `vue-tsc@3.3.11`，`type-check` 用 `vue-tsc --noEmit -p tsconfig.json`。
- **`tsconfig.json` 的 `include` 要含 `src/**/*.vue`**：虽然 vue-tsc 会沿 import 图从
  `src/main.ts` 把 `.vue` 拉进来检查，但显式 include 才能覆盖未被任何模块 import 的
  `.vue`，别退回只写 `src/**/*.ts`。
- **`env.d.ts` 的 `declare module '*.vue'` shim 不会削弱检查**：vue-tsc 走真实 SFC 类型，
  实测 props 类型（如 `:ctrl="'yes'"` 传错类型）与模板未定义变量都能报出，无需删 shim。
- **addon-canvas 不要加回**：其 peer 仍是 `@xterm/xterm@^5`，与 xterm v6 冲突；
  WebGL 不可用时由 xterm 内置 DOM 渲染器兜底。
- **会话控制帧不写入 PTY**：`\x1b]resize;...\x07`、`\x1b]ping\x07` 与 `\x1b]id;` /
  `\x1b]ready\x07` / `\x1b]exit\x07` 均为前后端约定的 OSC 控制序列。
- **标签恢复的时序**：`App.onMounted` 先 `loadUserMode` 再 `restore`，保证无会话时
  新建标签按正确用户模式（custom → 登录用户）。
- **功能名折叠**：`labelsOn` 控制桌面功能名显示；仅**手动**展开（《》按钮），
  溢出**自动收起**；不要恢复旧的“宽度检测自动展开”逻辑。
- **外部部署行为**：本仓库构建产物可能被外部部署机制移动/重启
  （如 `/vol1/@appcenter/Terminal/bin/terminal`），工作区二进制消失/更新属外部流程，
  不要误判为构建失败。
- **paneControls 按 uid 注册**：`pc.register(uid, c)` / `pc.get(activeUid)`——顶栏按钮
  若再使用单一共享对象会导致操作作用于后台标签（历史 bug）。

---

## 6. 命令速查

```bash
make dev              # 开发构建（默认）；make release V=x.y.z / make clean
./build.sh            # 等价脚本
cd frontend && npm run dev / build / type-check
cd backend && go vet ./...    # 需要 backend/embed 存在（可先放占位文件或直接 make）
```

## 7. 新增/修改时建议的自查清单

- [ ] 前端资源与 API/WS 是否走 `runtimeBase()` / `wsUrl()`？
- [ ] 是否硬编码了 platform 路径 / baseurl？
- [ ] 会话用户逻辑（`Tab.userSpec` → `user=` 参数）是否符合三个模式？
- [ ] 关闭标签 / 清屏是否走了对应 API？
- [ ] 前端改动是否已重新构建（`make dev`）并验证？
- [ ] 新增 Pinia store / composable 是否遵循现有结构？
- [ ] `npm run type-check`（vue-tsc）与 `vite build` 是否都通过？
- [ ] 版本号是否经 `-ldflags` 注入？