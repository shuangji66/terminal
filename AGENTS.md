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
  `QUICK_CMDS_FILE`、`SESSION_DIR`、`USER_MODE_FILE`、`APP_HOME_TEMPLATE`、
  `APPCENTER_CLI`、`SHELL`、HOME/PATH/LANG）。
- `admin.go` — Admin mux：`/api/*` 路由（info / sessions / apps / session /
  session/clear / quickcmds / user-mode）+ SPA（baseurl 注入）+ WebSocket `/terminal`
  分发。`/api/apps` 返回弹窗用的应用用户列表（含 `homeTemplate`，前端据此提示家目录，
  不硬编码路径）。
- `sessions.go` — 会话管理：每个会话一个 PTY + 历史临时文件（**必须以 `O_RDWR` 打开**，
  否则 attach 回放 / history API 读会 EBADF）；`resolveRunUser` 解析 `user=` 参数与
  `X-Trim-Userid` 头（root / `app:<APP NAME>` 应用用户 / 指定 uid / NAS 用户；
  应用名非法或解析失败会**返回错误**，不再静默回退），非当前 uid 用 `syscall.Credential`
  切换用户；`attach` 在 `histMu` 内“回放历史 + 接管实时流”，保证字节不重不漏；
  pump 协程持续读 PTY → 追加历史文件 + 广播到已挂载连接。
- `terminal.go` — WS 握手（自实现帧编解码）→ `id` 为空则新建（先发 `\x1b]id;<id>\x07`）
  或 `attach`（回放 + `\x1b]ready\x07`）；OSC 控制消息：`resize` / `ping` 不进 PTY；
  客户端断开**只解挂载不杀会话**。
- `quickcmds.go` — 快捷指令整体保存（tmp + rename 原子写，兼容旧版裸数组格式）。
- `usermode.go` — 启动用户模式文件（`nas`|`root`|`custom`，非法回退 `nas`）。
- `apps.go` — 应用用户（NAS 应用）支持：执行 `appcenter-cli list` 解析 **APP NAME**，
  过滤 `trim.*` 系统软件与「无同名系统用户 / 家目录不存在」的应用，按名排序后供
  `/api/apps` 返回；`resolveAppRunUser` 把 `app:<APP NAME>` 解析为 uid/gid/HOME
  （模板 `TERMINAL_APP_HOME_TEMPLATE`，默认 `/var/apps/%s/home`）。
  **该目录必须真实存在**——它是 `cmd.Dir` 与 `HOME`，不存在会导致 `pty.Start` 失败。
  `listAppUsers` 对结果做**短缓存**（`appListTTL`）并限制命令超时（`appListTimeout`），
  避免每次弹窗都拉起进程或被子进程卡死；`fetchAppList` 失败时回退 `scanAppRoot`。

**前端（Vue 3，`frontend/`）**

- **强制 Composition API + `<script setup>` + TypeScript**。
- `App.vue` — 壳：TabBar + 终端面板区（标签用 `visibility` 隐藏以保持尺寸/WS 存活）+
  各弹窗（快捷指令 / 设置 / 确认）；启动顺序：`loadInfo` + `loadUserMode` →
  （custom 模式预取一次应用用户列表，见第 5 节「应用用户列表缓存」）→ `restore`。
- `TabBar.vue` — 顶栏：新建（常驻左侧）、标签条（拖拽/滚轮横滚、双击重命名、常驻
  关闭按钮）、桌面功能名（`labelsOn` 控制显示；《》手动折叠/展开 + 溢出自动收起）、
  移动端第二行功能键；custom 模式新建前弹 `UserPickDialog`。
- `TerminalPane.vue` — 每个标签一个 xterm 实例 + WS；xterm v6 + addons（fit/webgl/
  search/web-links/clipboard/unicode11/serialize/image）；主题（深色黑底绿字 / 浅色米白
  黑字）、字号（来自 settings store）动态应用；会话控制帧处理（`\x1b]id;` /
  `\x1b]ready\x07` / `\x1b]exit\x07`）；搜索悬浮框；鼠标选中自动复制（桌面端）；
  **移动端长按选词并自动复制**（`handleLongPress` → `selectWordAt`：按触摸坐标算列/行 →
  按 `wordSeparator` 切词 → `term.select()` → 由 `onSelectionChange` 统一复制 + toast。
  系统文本选择器/原生菜单已按需求移除）；**移动端单指触摸滚动自研**（xterm 锁定 6.0.0
  stable 无触摸代码，组件内 `scrollByPixels` 把纵向位移换算成行数调 `term.scrollLines()`，
  详见第 5 节「xterm 锁定 6.0.0」）；**iOS 第三方输入法
  双保险**：`installImeFallback`（composition 补发）+ composition 期键盘守卫
  （`attachCustomKeyEventHandler`，拦替代码键，见第 5 节「xterm 锁定 6.0.0」第 8 条）；辅助功能键
  （`KeypadBar`）按键修饰符输入一次后自动解除；
  向 `paneControls` 注册命令入口（paste/clear/reconnect/search/focus/send）。
- `KeypadBar.vue` — 移动端辅助键条（ESC/Tab/Ctrl/Alt/Shift/Ins/←↓→/符号）；
  显隐由 `v-if="mobileLayout"`（`useMobileLayout()`）控制，**不用 `md:hidden`**
  （iPad 宽度 ≥768px 会被宽度断点判成桌面而丢掉整条键条，见第 5 节「大屏触屏」）；
  长按连发；修饰键（Ctrl/Alt/Shift）变色指示按下态，输入一次后自动解除；
  所有非长按键采用 `@click` + `@touchstart.prevent` 双保险确保移动端可靠触发；
  底部内边距用 `--kb-safe-bottom`（键盘弹起时为 0，见下条）。
- `composables/useMobileLayout.ts` — **是否按移动端（触屏）布局渲染**（当前唯一使用者是
  `KeypadBar`）：判据 = 窄视口（<768px，保留旧行为）**或**触屏（`(hover:none) and
  (pointer:coarse)`，另有「移动/平板 UA + `maxTouchPoints > 0`」兜底，覆盖接了触控板后
  主指针变 `pointer: fine` 的情况）。模块级单例 ref，无生命周期钩子。
- `composables/useViewportHeight.ts` — **虚拟键盘适配（App 壳层调用一次）**：监听
  `visualViewport` 的 `resize`/`scroll`，把可视视口几何写入 `<html>` 的 CSS 变量：
  `--vvh`（可视视口高度 = `.app-shell` 高度）、`--vvt`（可视视口相对布局视口的位移 =
  `.app-shell` 的 `top`）、`--kb-safe-bottom`（键盘弹起时 0，覆盖键条的安全区内边距）、
  `--tabbar-h`（**实测**顶栏高度，见第 5 节「浮层与终端区域对齐」）。
  不用 `100dvh` 的原因见第 5 节「虚拟键盘适配」。
- `stores/` — `sessions`（标签 + userSpec + 恢复/关闭）、`settings`（用户模式 + 字号，
  字号存 localStorage）、`quickCmds`、`toast`、`paneControls`（**按标签 uid 的注册表**，
  顶栏按钮通过激活 uid 解析，避免后台标签覆盖）、`appUsers`（应用用户列表**内存缓存**，
  仅 custom 模式冷启动预取一次，见第 5 节「应用用户列表缓存」）。
- `serverapi/index.ts` — `runtimeBase()` / `wsUrl(id?, user?)` / `api.*`。
- `i18n/zh.ts` `en.ts` — 文案集中管理；`composables/useTheme.ts` `useI18n.ts`。

---

## 4. 必须遵守的规则（Agent 优先）

1. **不要破坏“运行时 baseurl”机制**：前端资源/API/WS 一律走 `runtimeBase()` /
   `document.baseURI`；后端 SPA 服务统一走 `rewriteIndexBase`。
2. **不要硬编码平台路径**：一律经 `TERMINAL_*` 环境变量（socket / baseurl / 各类文件）。
3. **会话用户逻辑不可回归**：新建会话的用户由**标签级 `Tab.userSpec`** 决定
   （`wsUrl` 的 `user=` 参数：`nas` 不带参数 / `root` / `app:<APP NAME>`），而非全局设置；
   恢复/挂载的会话保持原用户；custom 模式每次新建必须弹 `UserPickDialog`
   （登录用户 / ROOT / 应用用户列表，**两个常规选项必须保留在列表上方**）。
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

- **浮层与「终端区域」对齐（快捷指令弹窗的尺寸依据）**：终端区域 = `.app-shell`
  （高度 `--vvh`、顶部偏移 `--vvt`）**减去顶栏**；顶栏高度由 `useViewportHeight()`
  **实测** `<header>` 写入 `--tabbar-h`（`onMounted` 首测 + `ResizeObserver` 跟踪，
  断点切换/功能名折叠/旋转后自动更新）。需要与终端区同高的浮层用 style.css 的
  `.qc-region`：`top: calc(var(--vvt,0px) + var(--tabbar-h,0px))`、
  `height: calc(var(--vvh,100dvh) - var(--tabbar-h,0px))`（`.qc-panel` 撑满它）。
  **不要**用 `100vh/50vh`（iOS 键盘弹起/地址栏收合只改可视视口，vh 会算错）、
  **不要**用 `bottom: 0`（相对布局视口，键盘弹起时壳层底边被 `--vvt` 下移后错位）、
  **不要**在 CSS 里按 TabBar 的 padding/border 拼算顶栏高（改结构即失准；移动端第二行
  实测盒高比 `min-h-10` 多 1px，拼算会差 1px）。两个变量都写了回退值，缺失时不会塌高。
  当前唯一使用者是快捷指令弹窗（宽度仍 `max-w-lg` 居中卡片，高度与终端区一致）。
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
- **iframe 内必须用 xterm 内置 DOM 渲染器（`preserveDrawingBuffer` 压不住）**：本应用通常被
  桌面外壳（飞牛 OS 桌面等）嵌在 iframe 窗口里，「拖动窗口」= 移动 iframe 元素 → 合成器每帧
  重新光栅化 iframe 内容，此时 WebGL canvas 会整块掉字形/光标 —— 现象是**拖动时文字和光标闪，
  背景看着正常（主题背景同时画在 canvas 和 `.xterm-viewport` 的 CSS 背景上）、不错行不跳动，
  独立标签页不复现**。真机实测：`new WebglAddon(true)`（`preserveDrawingBuffer`）**仍然闪**，
  `?nogl=1` 换 DOM 渲染器才不闪。故 `TerminalPane.vue` 按 `window.self !== window.top` 选渲染器：
  **iframe 内 = DOM 渲染器（默认，不给用户开关）；顶层标签页 = WebGL**（平移顶层页面是纯合成
  操作、不需要重光栅，保留输出吞吐）。**不要**图省事改成「一律 DOM」（顶层白白损失吞吐），
  也**不要**退回「一律 WebGL」（桌面窗口里必闪）。
  机制侧实测（Playwright + headless Chromium/SwiftShader）：`pdb=false` 时让 iframe 跨若干
  合成帧移动后回读 canvas 得 **0** 个字形像素，`pdb=true` 仍为全量 —— 缓冲确实会被清空，但
  **保留缓冲并不足以救回合成器那一帧**，别把 `preserveDrawingBuffer` 当解法。另注意 headless
  下**复现不了闪烁本身**（软件合成逐帧像素完全一致，sd=0.00），这类问题只能真机拖动验证。
- **iframe 里「移动窗口」型拖动，子文档收不到任何事件**：实测 `resize` / `ResizeObserver` /
  `visualViewport` 回调全为 0（只有拉伸窗口才会逐帧触发）。所以别指望用事件判断「正在被
  拖动」，也别把这类闪烁当 fit/重排问题去修（fit 防抖那条只覆盖拉伸场景）。
- **WebGL 上下文丢失必须摘掉 addon**：xterm 收到 `webglcontextlost` 后**先等 3s**（给恢复
  机会）才 fire `onContextLoss`，不处理就会永久停在空白画面。`TerminalPane.vue` 在
  `onContextLoss` 里 `addon.dispose()`（xterm 随即换回内置 DOM 渲染器）并 `term.refresh()`
  重绘一次。排查提示：上下文丢失后 `getContextAttributes()` 返回 `null`，别拿它判断渲染器
  是否还在；测试要按 3s 以上等待，否则会误判「回退没生效」。
- **渲染器调试开关 `?nogl=1`**：强制走 xterm 内置 DOM 渲染器，用于把「WebGL 合成层」类
  显示问题与其它层（fit/布局/桌面外壳）一刀切开，不必改代码重建。iframe 内本来就是 DOM
  渲染器，该开关主要给顶层标签页用（正常访问不带参数，顶层走 WebGL）。
- **xterm 锁定 6.0.0 stable（已从 6.1.0-beta 回退），触摸滚动/滚动条自行处理**：
  `@xterm/xterm@6.0.0` + 各 addon 的 0.11.0/0.19.0/0.16.0/0.2.0 等 stable 组合。
  曾升级 6.1.0-beta 想用其自带触摸手势，实测**安卓/iOS 上轻触拉不起软键盘**（beta 的
  Gesture 层 `preventDefault` 掉了浏览器的合成鼠标事件，两端都只能靠组件自己的
  touchend 聚焦，真机仍无效）——**已整体回退，不要再次升级**。回退后的移动端行为：
  1) **单指触摸滚动必须自研**：6.0.0 的滚动由 VS Code `SmoothScrollableElement` 驱动
     （Viewport.ts），`.xterm-viewport` 是空壳（scrollHeight===clientHeight），browser 层
     **没有任何触摸代码**。`TerminalPane.vue` 用 `touchstart/move/end` 把纵向位移换算成
     行数调 `term.scrollLines()`（`scrollByPixels`，残差跨 move 累计避免慢拖丢行），
     `TAP_SLOP=30px` 越过即判拖动、取消长按。
  2) **滚动条滑块拖动不需要自研**：6.0.0 在 `.scrollbar` 节点监听 `pointerdown`
     （`_domNodePointerDown → _sliderPointerDown → GlobalPointerMoveMonitor`），内部
     `setPointerCapture` + window 级 pointermove + preventDefault，触摸/鼠标统一可用
     （**曾误判需要自接管并实现过一版，后证实多余且与原生抢事件，已删**）。
     组件只负责两件 CSS 事（style.css）：`(hover: none)` 下让 `.scrollbar.vertical` 与
     `.slider` 常驻可见并恢复 `pointer-events:auto`（Auto 可见性靠 hover，触屏永远等不到，
     否则滑块抓不到；`.invisible` 类自带 `pointer-events:none` 必须显式覆盖）+ 滑块热区
     左右各扩 4px；滑块 `touch-action: none` 防止拖动被认领成页面平移。
     ⚠️ 选择器必须带 `.vertical`：`.xterm-scrollable-element > .scrollbar.vertical > .slider`，
     因为同层还有一个 `.scrollbar.horizontal`（不特指会查到横条）。
     ⚠️ 6.0.0 类名是 `.scrollbar/.slider/.active/.invisible`（beta 才是 `.xterm-scrollbar`
     等），不要混用。
  3) **轻触聚焦必须自研**：iOS Safari 不把触摸合成鼠标事件；且 ① 的 `preventDefault`
     （滚动时必需）会切断 Android 的合成链。两端都只剩 `onTouchEnd` 里的 `focusTerm()`。
     阈值必须与 xterm 的 tap 判定（位移 <30px 且 <700ms）对齐：若小于 30px，手指漂移
     10~30px 时组件判「拖动」不聚焦、xterm 判 tap 不滚动，谁都不聚焦、键盘弹不出来。
  4) **聚焦一律走 `focusTerm()` 而非直接 `term.focus()`**：`term.open()` 之前
     `.xterm-helper-textarea` 不存在，xterm 内部 `if (this.textarea)` 直接返回，这次聚焦
     被静默丢弃——表现为「首次点终端没反应，再点一次才行」。`open()` 已提前到挂载时
     同步执行（终端字体是系统等宽栈，不含 webfont，测量不受 `document.fonts.ready`
     影响），`focusTerm()` 仍保留 `pendingFocus` 兜底。所有调用点（触摸/辅助键/粘贴/
     关闭搜索/标签激活/paneControls.focus）都已统一。
  5) **长按选词无论命中与否都必须聚焦**：`handleLongPress` 命中单词时只 `select()`
     不聚焦 → 焦点停 BODY、键盘拉不起来；恢复的标签满屏文字落点必中单词，新建标签
     空白屏落点无词——正是「只有恢复的标签不行，新建标签正常」的成因。
  6) **`legacyCopy` 用完临时 textarea 必须把焦点还给原元素**：`execCommand('copy')` 要求
     选区元素聚焦，`ta.focus()` 抢走终端 textarea 的焦点、`removeChild` 后落到 BODY，
     移动端即键盘收起/拉不起。http 反代部署（`isSecureContext === false`、
     `navigator.clipboard` 不可用）时每次自动复制都走这条路，桌面端则是「框选后无法
     直接键入」。
  7) `term.select(col,row,len)` 的 `row` 是**缓冲区绝对行号**（视口内行 + `viewportY`）；
     词首定位必须先向左回退到词首。xterm 6.0.0 的 `onSelectionChange` 在 `select()` 内
     同步触发，`copyText` 的抢焦点发生在 `select()` 返回之前，所以 `handleLongPress`
     里的 `focusTerm()` 必须放在 `select()` **之后**（现在是在函数开头 + select 之后
     效果等价，注意别把 focus 挪到 select 前就返回）。
  8) **beta 的 IME 兜底/守卫与版本无关，保留**：`installImeFallback` +
     `attachCustomKeyEventHandler` 在 6.0.0 上同样需要（合成 IME 事件实测仍复现候选字
     丢失），不要随回退一起删。
- **桌面端中文输入重复 = xterm 把同一次组合提交投递两遍，已在组件层去重**：一次
  composition 提交在 xterm 6.0.0 内部有两条互不知情的投递路线——① `input`(inputType=
  insertText) / `keypress` 事件触发的 `_inputEvent` / `_keyPress`，② `compositionend`
  之后 `setTimeout(0)` 再读 `<textarea>` 补投的 `CompositionHelper._finalizeComposition`
  ——于是敲「加油」PTY 收到「加油加油」（上游同类：xtermjs/xterm.js #3191 / #5023 /
  #6045 / #6049 / #6060 / #6078，6.0.0 与 master 均未修）。移动端不复现：软键盘输入法
  只走 composition 路径提交（候选字不另经 input/keypress 路线投递）。`TerminalPane.vue`
  的「组合提交去重」（`commitGuard` + `sendPty`）在 compositionend 后的小窗口内丢掉同一段
  提交文本的重复投递（整段 200ms / 逐字分片 50ms，`compositionstart` 复位）。改这段代码
  时注意三条：① `term.onData` 的发送必须走 `sendPty`（粘贴/快捷指令/辅助键仍直接
  `sock.send`，不受去重窗口影响）；② `installImeFallback` 的判重必须比对**窗口内发送内容的
  拼接**（`recentSent.slice(mark).join('')`），桌面端 keypress 路线会逐字投递（'加'、'油'），
  逐条 `includes` 会漏判成「没发过」而整段补发；③ 补发文本**优先取 `compositionend.data`**：
  某些桌面输入法最后一次 `compositionupdate` 携带的是拼音预编辑串（"jiayou"），把它当提交
  文本会往 PTY 里塞拼音（iOS 第三方键盘 `compositionend.data` 常为空串，才回退到
  compositionupdate）。复现/验证方式：让真实前端（vite dev 或后端产物）对接假 WS sink，在
  `.xterm-helper-textarea` 上派发合成事件序列并读回 `send()` 的内容（本次排查脚本临时放在
  `/tmp/imerepro/harness.mjs`，未入库）；沙箱里 `/dev/ptmx` 不可用，PTY 建不起来，端到端
  只能验到 API 层。
- **会话控制帧不写入 PTY**：`\x1b]resize;...\x07`、`\x1b]ping\x07` 与 `\x1b]id;` /
  `\x1b]ready\x07` / `\x1b]exit\x07` 均为前后端约定的 OSC 控制序列。
- **标签恢复的时序**：`App.onMounted` 先 `loadUserMode` 再 `restore`，保证无会话时
  新建标签按正确用户模式（custom → 登录用户）。
- **功能名折叠**：`labelsOn` 控制桌面功能名显示；仅**手动**展开（《》按钮），
  溢出**自动收起**；不要恢复旧的“宽度检测自动展开”逻辑。
- **验证会话环境时的沙箱陷阱（曾导致误判）**：在沙箱/CI 里验证 `buildSessionEnv`，
  测试进程（agent 的 shell）自带 `HOME`/`PWD`，二者会与函数构造的值**同名竞争**而
  掩盖真实行为。部署环境（`trim_app_center.service` 无 `User=`/`Environment=`，
  systemd 不注入 `HOME`）并不会这样。结论：验证这类逻辑必须**显式构造或清除**相关
  变量，不要拿"当前 shell 的环境"当部署环境；也不要仅凭沙箱现象就判定线上有 bug。
- **`appcenter-cli` 需要权限**：普通用户直接执行会
  `panic: dial unix /run/trim_app_cgi/rpcbroker: permission denied`（该 socket 属
  `OfficialAppUsers` 组）；后端以 root 运行时可正常执行，失败时 `apps.go` 会记录日志并
  **回退扫描 `/var/apps`**，功能不至于完全不可用。
- **应用用户的 HOME 必须自己赋予**：系统不会给应用用户设 HOME（`/etc/passwd` 里是
  `/home/<app>`，但该目录并不存在）。`buildSessionEnv` 把 `HOME`/`PWD` 都设为
  `/var/apps/<APP NAME>/home`——它既是 `cmd.Dir`，也是 `~` 的落点。两者必须一致，
  否则 bash 提示符不会显示 `~`。
- **HOME/PWD 必须唯一且不可被继承值覆盖**：环境变量重复时**后者生效**。后端由
  appcenter 脚本经 bash 启动，父进程会导出 `HOME`/`PWD`，若直接 `append(os.Environ())`
  就会顶掉我们设的值（实测后果：`HOME` 错误 + `PWD` 变成软链的物理路径
  `/vol1/@apphome/<app>`，提示符显示完整路径而非 `~`）。故 `buildSessionEnv` 先从
  继承环境里**剔除** `HOME`/`PWD` 再追加目标用户的值——新增需要强制的变量时请照此处理，
  不要直接把 `os.Environ()` 追加到末尾。
- **部分应用没有同名系统用户**（如 `Nvidia-Driver-580`、`fnpackup`、`trim.media`），
  无法 setuid，已在 `/api/apps` 列表中被过滤掉——不要"修好"成显示出来。
- **应用用户列表缓存（不要改回「每次打开弹窗都请求」）**：`stores/appUsers.ts` 把
  `/api/apps` 结果（`apps` + `homeTemplate`）缓存在**内存**里——仅 custom 模式冷启动时
  由 `App.onMounted` 调一次 `load()`（其他模式不加载），`UserPickDialog` 每次打开只走
  `ensureLoaded()`（有缓存即返回，正常不发请求；启动后从其他模式切到 custom 才按需请求
  一次），需要最新列表由弹窗的刷新按钮触发 `load()`。缓存**不落 localStorage**，所以
  「每次前端冷启动都重新拉取一遍」是天然结果；加载失败不置 `loaded`（避免用空列表假装
  加载成功），下次打开自动重试。`load()` 用 inflight promise 做并发去重：冷启动预取与
  弹窗首次打开同时触发也只有一次请求。刷新失败时保留旧列表（只在标题行提示错误），
  不要把 `apps` 清空。
- **外部部署行为**：本仓库构建产物可能被外部部署机制移动/重启
  （如 `/vol1/@appcenter/Terminal/bin/terminal`），工作区二进制消失/更新属外部流程，
  不要误判为构建失败。
- **paneControls 按 uid 注册**：`pc.register(uid, c)` / `pc.get(activeUid)`——顶栏按钮
  若再使用单一共享对象会导致操作作用于后台标签（历史 bug）。
- **`crypto.randomUUID()` 是 SecureContext-only，http 部署不可用**：本项目的典型访问
  方式正是 http 反代（`window.isSecureContext === false`），此时 `crypto.randomUUID`
  为 `undefined`，直接调用抛 `TypeError: crypto.randomUUID is not a function`
  （曾导致「http 访问下新增/编辑快捷指令失败」）。安全上下文无关的替代只有同一
  `crypto` 对象上的 `getRandomValues`；新增需要 id 的地方统一走
  `stores/quickCmds.ts` 的 `newCmdId()`（优先 `randomUUID`，否则手拼 v4 UUID），
  **不要再直接调用 `crypto.randomUUID()`**。同理 `navigator.clipboard` 在 http 下也不存在
  （剪贴板已有 `legacyCopy` 兜底，见 `TerminalPane.vue`）。
- **大屏触屏（iPad）不能被宽度断点判成桌面**：`md:`（768px）只表示「屏幕宽」，iPad 的 CSS
  宽度是 768 / 834 / 1024px（横屏更大），全部 ≥768 —— 于是被当成桌面，底部辅助键条
  （`KeypadBar`）被 `md:hidden` 整条隐藏，触屏上再也没有 ESC/Tab/Ctrl/Alt/方向键可用。
  所以键条显隐一律走 `composables/useMobileLayout.ts`（触屏 **或** 窄视口），
  **不要写回 `md:hidden`**；TabBar 的功能行仍按 `md` 断点（iPad 上显示更全的桌面行是刻意的，
  含移动端第二行没有的搜索）。另注意 `(hover: none) and (pointer: coarse)` 在接了鼠标/
  触控板后会失效（WebKit/Blink 把**主**指针改报成 `pointer: fine`），故该 composable 还有
  「移动/平板 UA + `maxTouchPoints > 0`」兜底。
  验证方式（本次用的）：真实后端产物 + `socat` 把 unix socket 转 TCP + headless Chromium 的
  `Emulation.setDeviceMetricsOverride` + `setTouchEmulationEnabled` 逐场景独立启动，
  判定要按 **可见**（`getBoundingClientRect().height > 0`）而非「DOM 里有没有」——
  旧实现的 `md:hidden` 会留下 DOM 只隐藏显示，只看 DOM 会得出假阳性。
- **虚拟键盘适配（iOS 底栏不跟随的根因）**：`index.html` 的
  `interactive-widget=resizes-content` **只对 Chrome/Firefox for Android 生效**；
  **Safari 至今不支持**（WebKit 已实现，尚未随版本发布），iOS 上键盘弹起只收缩
  「可视视口」（`visualViewport`），布局视口与 `100dvh` 都不变。因此**不要把壳层高度
  退回 `100dvh`**：`.app-shell` 由 `useViewportHeight()` 写入的 `--vvh` 驱动，
  并用 `position: fixed; top: var(--vvt)` 钉在可视视口矩形上（键盘弹起时 iOS 会把可视
  视口下移，靠 `top` 跟随；若改用 `margin/transform` 会让文档高于视口而产生多余滚动，
  反过来干扰 `offsetTop`）。`--vvh/--vvt/--kb-safe-bottom` 三者是一组，缺一个就会
  出现「底栏被键盘盖住 / 与键盘之间多一条安全区空隙 / 终端不收缩」。
  纯 CSS 无法替代：`env(keyboard-inset-height)` 需要 VirtualKeyboard API（未启用），
  `@supports (height: var(--vvh))` 恒为假（`@supports` 条件含 `var()` 按规范判不支持）。
  改动后必须**真机或可视视口可收缩的环境**验证；CDP 的
  `Emulation.setDeviceMetricsOverride/ setVisibleSize` 在部分环境**无法**改变
  `visualViewport`，不能用来判定修复是否生效。
- **移动端触摸与滚动的版本无关约束**（实现细节见上方「xterm 锁定 6.0.0 stable」）：
  1) `.term-container` 必须保持 `touch-action: none`：否则纵向拖动会被浏览器认领为整页平移，
     iOS 上就是「页面抖动/橡皮筋回弹」。自研滚动在 touchmove 里 `preventDefault`（`e.cancelable`
     判空防报错），与该声明双保险。
  2) 滚动位置由 `stickToBottom`（`term.onScroll` 维护）守护：后台输出/尺寸重排只在跟随底部时
     才 `scrollToBottom()`，用户主动输入才 `pinToBottom()`。自研 `term.scrollLines()` 默认
     `suppressEvent=false`，会触发 `onScroll`，该机制不受回退影响（实测滑块随动）。
  3) **不要依赖浏览器把触摸合成为鼠标事件**：xterm v6 的 browser 层只监听 `mousedown/mousemove/
     mouseup`，`SelectionService` 靠 `event.detail`(1/2/3) 区分单/双/三击选词，**没有任何触摸
     代码**。Android Chrome 会把双击合成为 `detail=2` 的 `mousedown`，所以「双击选词」能用；
     **iOS Safari 不合成**，轻触/长按/双击全都无反应——因此移动端选词由
     `TerminalPane.vue` 自行实现（`handleLongPress`），不依赖合成事件。
     注意 `term.select(col,row,len)` 的 `row` 是**缓冲区绝对行号**（视口内行 + `viewportY`，
     与 xterm 内部 `_getMouseBufferCoords` 的 `+ydisp` 一致），算错会导致选中错行。
     词首定位必须先**向左回退到词首**：落点在词中间时若直接以落点为起点，`world` 会截成 `rld`。
  4) **系统文本选择器/原生菜单已移除，不要加回来**：隐藏 textarea 的焦点会被 xterm 抢走
     （`CoreBrowserTerminal` 在 `mousedown` 里 `preventDefault + focus()`），iOS 上该路径本就不
     生效，且会打断输入法状态机。移动端的复制路径只有「长按选词 → `onSelectionChange` 自动
     复制」，桌面端为「鼠标框选自动复制」。
  5) **iOS 第三方输入法采用「兜底 + 守卫」双保险（与 xterm 6.0.0/beta 版本无关）**：
     - 兜底 `installImeFallback` **只能覆盖 composition 路径，不要放宽**：搜狗/百度/微信键盘等
       提交候选字后会**立即清空**隐藏 textarea，而 xterm 的 `CompositionHelper._finalizeComposition`
       是「`compositionend` 后用 `setTimeout(0)` 再读 `textarea.value.substring(...)`」，此时读到
       空串 → 中文候选字**永远发不出去**。`installImeFallback` 优先用 `compositionend.data`
       作为提交文本（**桌面端某些输入法最后一次 `compositionupdate` 携带的是拼音预编辑串**，
       拿它补发会把拼音塞进 PTY），`compositionend.data` 为空串时（iOS 第三方键盘的常态）
       才回退到 `compositionupdate.data`；在 `compositionend` 后延时检查「xterm 这段时间内
       是否已发出该文本」，只有没发过才补发。
       **实测边界（重要）**：`Input.insertText`、`keyDown/keyUp` 这些路径 xterm **本来就正常**，
       兜底一旦介入就会变成重复发送（踩过：范围放宽后英文变成双份 `["a","a"]`）。所以
       `installImeFallback` 只监听 `compositionstart/update/end` 三个事件，绝不监听
       `input`/`keydown`。两个实现约束：① 必须在 `term.open()` **之后**安装
       （`.xterm-helper-textarea` 由 `open()` 创建）；② 判重必须比对**内容**（窗口内发送内容的
       `join('')`）而非发送条数——且必须拼接后比对，见下方「桌面端中文输入重复」条。
     - 守卫 `attachCustomKeyEventHandler`（issue #4486 的 workaround 路线，同
       microsoft/vscode#320525）：中文 IME 输入「、」「。」等标点时，第三方键盘会把该按键
       映射成**替代码**（如 `\`）发一个非 229 的 keydown；xterm 的 `CompositionHelper` 看到
       非 229 的 keydown 会 `_finalizeComposition(false)` 结束组合并把**原始键码**当普通键发出
       （PTY 收到 `\` 而不是「、」）。守卫在 composition 期间对 keydown/keyup/keypress 一律
       返回 false，组合文本仍经 compositionend 正常提交。iOS 的 `KeyboardEvent.isComposing`
       不可靠，故同时维护 `compositionstart/end` 自设标志（`endGuard` 延时一拍放行），再叠加
       `ev.isComposing` 兜底。监听也挂在 textarea 上、随 `installImeFallback` 一起装
       （同样必须 `open()` 之后）。
  6) **`keyCode=229` 的重复发送是 xterm 固有行为，不是本项目的 bug**：CDP 派发
     `keyDown(229)+keyUp` 时会出现 `keydown:229 → keypress → input` 三个事件，xterm 会发送
     两次（`["x","x"]`）。已在 HEAD 基线上复核为完全相同的表现，不要试图在前端"修"它。
     注意沙箱里只能用 CDP 派发事件，无法复现 iOS 真实键盘的全部细节——涉及输入法的判断
     应以真机为准，不要仅凭沙箱现象下结论。
     （验证提示：合成 `KeyboardEvent` 在 Chromium 里**无法携带 keyCode**（构造器忽略之），
     xterm 收到 keyCode=0 会丢弃，故 Playwright/CDP 下 xterm 键盘路径**表现为无输入**——
     这是环境限制而非回归；守卫逻辑可在独立页面加载 xterm 后用
     `Object.defineProperty(e,'keyCode',{get:...})` 方式单元验证。触摸手势/滚动/长按选词/
     自动复制均可用 `hasTouch: true` + 合成 TouchEvent 端到端跑通。）

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
- [ ] 会话用户逻辑（`Tab.userSpec` → `user=` 参数）是否符合三个模式（含 `app:<APP NAME>`）？
- [ ] 应用用户：`HOME`、`PWD`、`cmd.Dir` 是否都取 `TERMINAL_APP_HOME_TEMPLATE` 的同一路径
      （不一致会导致提示符不显示 `~`）？新增环境变量是否会被继承值覆盖？
- [ ] 应用列表：新增过滤条件是否同步了 `usableAppUsers` 与 `/api/apps` 的语义？
- [ ] 关闭标签 / 清屏是否走了对应 API？
- [ ] 前端改动是否已重新构建（`make dev`）并验证？
- [ ] 新增 Pinia store / composable 是否遵循现有结构？
- [ ] `npm run type-check`（vue-tsc）与 `vite build` 是否都通过？
- [ ] 版本号是否经 `-ldflags` 注入？