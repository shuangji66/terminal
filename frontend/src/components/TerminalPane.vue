<script setup lang="ts">
// TerminalPane — 一个终端标签：持有独立的 xterm 实例 + WebSocket 会话。
// - 已有会话 id：后端在挂载时自动回放历史（→ \x1b]ready\x07），再进入实时流
// - 新会话（无 id）：后端回 \x1b]id;<id>\x07 控制帧回填会话 id
// - 浏览器断开只解挂载、不杀会话；「重连」重新挂载并再次回放历史
// - **单挂载点**：一个会话同时只有一个操作端。本端被其他设备接管时，后端发
//   \x1b]detached\x07 并以 WS close 4001 关闭连接 → 本标签进入 detached 状态
//   （提示 + 不自动重连，避免两台设备互相顶号）；用户点「重连」= 显式夺回。
import { onMounted, onBeforeUnmount, ref, computed, watch, nextTick } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { SearchAddon } from '@xterm/addon-search'
import { WebLinksAddon } from '@xterm/addon-web-links'
import {
  ClipboardAddon,
  Base64,
  type ClipboardSelectionType,
  type IClipboardProvider
} from '@xterm/addon-clipboard'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { useTheme } from '@/composables/useTheme'
import { t } from '@/i18n'
import { useSessionsStore, type Tab } from '@/stores/sessions'
import { usePaneControlsStore } from '@/stores/paneControls'
import { useToastStore } from '@/stores/toast'
import { useSettingsStore } from '@/stores/settings'
import {
  wsUrl,
  resizePayload,
  HEARTBEAT_PAYLOAD,
  DETACHED_PAYLOAD,
  WS_CLOSE_DETACHED,
  api
} from '@/serverapi'
import KeypadBar from './KeypadBar.vue'

const props = defineProps<{
  tab: Tab
  active: boolean
}>()

const store = useSessionsStore()
const pc = usePaneControlsStore()
const toast = useToastStore()
const settings = useSettingsStore()
const { isDark } = useTheme()

const el = ref<HTMLElement | null>(null)
// 本面板自己的提示气泡。**不能用 document.querySelector('.term-copy-toast')**：所有标签的
// 面板都留在 DOM 里（非激活面板只是 visibility:hidden），会命中第一个标签的气泡 → 提示
// 落在隐藏面板上，用户什么也看不到。
const toastEl = ref<HTMLElement | null>(null)

let term: Terminal | null = null
let fitAddon: FitAddon | null = null
// iOS 第三方输入法键盘守卫的监听安装器（initTerminal 里生成，installImeFallback 里挂到 textarea）
let imeGuard: {
  attach(textarea: HTMLTextAreaElement): void
  detach(textarea: HTMLTextAreaElement): void
} | null = null
let searchAddon: SearchAddon | null = null
let webglAddon: WebglAddon | null = null
let sock: WebSocket | null = null
let termOpened = false
let disposed = false
// 待补的聚焦意图：term.open() 之前 .xterm-helper-textarea 尚未创建，此时 term.focus()
// 是空操作（xterm 内部 `if (this.textarea)` 直接返回），静默丢弃这次聚焦。
let pendingFocus = false

// 心跳间隔：防止连接被代理 / NAT 空闲超时断开
const HEARTBEAT_INTERVAL_MS = 20000
let heartbeatTimer: number | null = null
let resizeObserver: ResizeObserver | null = null
let fitRetryTimer: number | null = null
let onPasteEvent: ((ev: ClipboardEvent) => void) | null = null
let toastTimer: number | null = null
let repeatTimer: number | null = null

// 渲染器调试开关：URL 带 ?nogl=1 时强制使用 xterm 内置 DOM 渲染器（不加载 WebglAddon）。
// 仅用于定位「WebGL 合成层相关」的显示问题——让排查者不必改代码重建就能 A/B 出到底是
// WebGL 渲染还是别的层。（iframe 内本来就是 DOM 渲染器，见 initTerminal 里的选择逻辑。）
function webglDisabled(): boolean {
  try {
    return new URLSearchParams(location.search).get('nogl') === '1'
  } catch {
    return false
  }
}

// 是否被桌面外壳嵌在 iframe 窗口里。只比较引用，不去读 top 的属性，跨域也安全。
function inEmbeddedFrame(): boolean {
  try {
    return window.self !== window.top
  } catch {
    return true
  }
}

// 会话不存在标记：收到 "session not found" 后丢弃旧 id，连接关闭时自动重建
let recreateOnClose = false

// 移动端辅助键的修饰键切换态
const ctrlPressed = ref(false)
const altPressed = ref(false)
const shiftPressed = ref(false)

// ---------- 搜索（xterm SearchAddon；仅桌面端有入口按钮） ----------
const searchOpen = ref(false)
const searchTerm = ref('')
const searchIndex = ref(-1)
const searchCount = ref(0)
const searchInput = ref<HTMLInputElement | null>(null)

// 高亮装饰色（黄=普通匹配 / 橙=当前匹配），两套主题通用
const SEARCH_DECORATIONS = {
  matchBackground: '#fde047',
  matchBorder: '#fbbf24',
  matchOverviewRuler: '#fbbf24',
  activeMatchBackground: '#f97316',
  activeMatchBorder: '#ea580c',
  activeMatchColorOverviewRuler: '#ea580c'
}

function resetSearchResults() {
  searchIndex.value = -1
  searchCount.value = 0
}

function toggleSearch() {
  searchOpen.value = !searchOpen.value
  if (searchOpen.value) {
    resetSearchResults()
    nextTick(() => searchInput.value?.focus())
  } else {
    closeSearch()
  }
}

function closeSearch() {
  searchOpen.value = false
  searchTerm.value = ''
  resetSearchResults()
  searchAddon?.clearDecorations()
  focusTerm()
}

function onSearchTermChange(val: string) {
  if (!searchAddon) return
  if (!val) {
    searchAddon.clearDecorations()
    resetSearchResults()
    return
  }
  // 输入即增量搜索，并高亮所有匹配（decorations）
  searchAddon.findNext(val, { decorations: SEARCH_DECORATIONS })
}

function searchNext() {
  if (!searchAddon || !searchTerm.value) return
  searchAddon.findNext(searchTerm.value, { decorations: SEARCH_DECORATIONS })
}

function searchPrev() {
  if (!searchAddon || !searchTerm.value) return
  searchAddon.findPrevious(searchTerm.value, { decorations: SEARCH_DECORATIONS })
}

function onSearchKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter') {
    e.preventDefault()
    if (e.shiftKey) searchPrev()
    else searchNext()
  } else if (e.key === 'Escape') {
    e.preventDefault()
    closeSearch()
  }
}

// 搜索结果计数显示："2/7"；超阈值时显示 "1000+"
const searchLabel = computed(() => {
  if (searchCount.value <= 0) return '0/0'
  if (searchIndex.value >= 0) return `${searchIndex.value + 1}/${searchCount.value}`
  return `${searchCount.value}+`
})

// ---------- xterm 主题配色（随应用主题切换） ----------
// 深色模式：黑底 #1A1A1A 背景，#4EC9B0 绿字（经典绿色终端风）
const DARK_PALETTE = {
  background: '#1A1A1A',
  foreground: '#4EC9B0',
  cursor: '#4EC9B0',
  cursorAccent: '#1A1A1A',
  selectionBackground: 'rgba(78, 201, 176, 0.35)',
  black: '#4d4d4d',
  red: '#ff5555',
  green: '#4EC9B0',
  yellow: '#ffdd33',
  blue: '#5555ff',
  magenta: '#ff55ff',
  cyan: '#55ffff',
  white: '#bbbbbb',
  brightBlack: '#878787',
  brightRed: '#ff7777',
  brightGreen: '#66ff88',
  brightYellow: '#ffee66',
  brightBlue: '#7777ff',
  brightMagenta: '#ff88ff',
  brightCyan: '#88ffff',
  brightWhite: '#ffffff'
}
// 浅色模式：温和的米白色背景 + 黑字
const LIGHT_PALETTE = {
  background: '#faf5e9',
  foreground: '#1a1814',
  cursor: '#1a1814',
  cursorAccent: '#faf5e9',
  selectionBackground: 'rgba(181, 141, 63, 0.35)',
  black: '#37352f',
  red: '#c14a3a',
  green: '#4d7f38',
  yellow: '#a5711f',
  blue: '#3b6ea5',
  magenta: '#8f4f9e',
  cyan: '#2e7d78',
  white: '#665f52',
  brightBlack: '#8a8377',
  brightRed: '#d96a55',
  brightGreen: '#5c9444',
  brightYellow: '#c08a2e',
  brightBlue: '#4f82bd',
  brightMagenta: '#a862b5',
  brightCyan: '#3d918b',
  brightWhite: '#948c7d'
}

// ---------- 渲染辅助 ----------
// 是否「跟随底部」：用户拖动看历史（viewportY < baseY）后置 false，回到最底才恢复 true。
// 由 term.onScroll 维护，见 initTerminal。自动滚动只在跟随底部时发生，否则会把用户刚翻上去
// 的历史立刻拽回底部（移动端拖动滚动会因此形同废设）。
let stickToBottom = true

// 自动滚动：仅在「跟随底部」时生效，用于后台输出/尺寸变化这类不应打断用户阅读的场景。
function scrollToBottom() {
  if (!term || !props.active || !stickToBottom) return
  nextTick(() => {
    if (stickToBottom) term?.scrollToBottom()
  })
}

// 强制滚到底：用于用户自己发起的操作（键入/粘贴/执行指令/清屏/切换标签），
// 此时意图明确就是要看最新内容。
function pinToBottom() {
  if (!term || !props.active) return
  stickToBottom = true
  nextTick(() => term?.scrollToBottom())
}

// 处理来自后端的数据：剥离 OSC 控制帧，其余写入终端
function handleData(raw: string) {
  if (!raw) return
  let rest = raw
  rest = rest.replace(/\x1b\]id;([0-9a-f]+)\x07/g, (_, id: string) => {
    store.adoptSessionId(props.tab.uid, id)
    return ''
  })
  rest = rest.replace(/\x1b\]ready\x07/g, () => {
    if (props.tab.restoring) {
      props.tab.restoring = false
    }
    return ''
  })
  // 被其他设备接管：用常量做纯字符串匹配（帧本身含 ESC/]，不必拼正则）
  if (rest.includes(DETACHED_PAYLOAD)) {
    rest = rest.split(DETACHED_PAYLOAD).join('')
    markDetached()
  }
  rest = rest.replace(/\x1b\]exit\x07/g, () => {
    store.setTabStatus(props.tab.uid, 'exited')
    return ''
  })
  // 会话已不存在（如服务端重启）：丢弃旧 id，稍后自动重建新会话
  if (rest.includes('session not found')) {
    recreateOnClose = true
    props.tab.id = null
    rest = '\r\n\x1b[33m' + t('conn_not_found') + '\x1b[0m\r\n'
  }
  if (rest) {
    term?.write(rest)
    scrollToBottom()
  }
}

// ---------- 被其他设备接管（单挂载点） ----------
// 只解挂载、不杀会话（会话继续在服务端运行并写历史文件），且**不自动重连**：
// 自动抢回会让两台设备来回顶号。用户点「重连」才夺回。
let detachedNotified = false

function markDetached() {
  if (detachedNotified) return
  detachedNotified = true
  stopHeartbeat()
  const c = myCtl()
  if (c) c.connected = false
  if (disposed) return
  store.setTabStatus(props.tab.uid, 'detached')
  term?.writeln('\r\n\x1b[33m' + t('conn_detached_hint') + '\x1b[0m')
  scrollToBottom()
  toast.show(t('conn_detached'), 'error')
}

// ---------- WebSocket 会话 ----------
function disconnect() {
  if (heartbeatTimer !== null) {
    clearInterval(heartbeatTimer)
    heartbeatTimer = null
  }
  if (sock) {
    sock.onmessage = null
    sock.onclose = null
    sock.onerror = null
    sock.onopen = null
    sock.close()
    sock = null
  }
}

function openSocket() {
  if (disposed) return
  disconnect()
  // 新连接对应新的 PTY（或重挂载的旧 PTY），上次下发的尺寸不再成立，需重新下发
  lastSentCols = 0
  lastSentRows = 0
  detachedNotified = false // 重新挂载即视为重新参与（可能是一次显式夺回）
  // 新建会话（无 id）时按本标签的 userSpec 决定运行用户：
  // root → user=root；app:<APP NAME> → user=app:<APP NAME>（NAS 应用用户）；
  // nas（默认）→ 不带 user 参数，后端读网关 X-Trim-Userid。
  const spec = props.tab.userSpec
  const userParam = props.tab.id || spec === 'nas' ? undefined : spec
  const url = wsUrl(props.tab.id ?? undefined, userParam)
  sock = new WebSocket(url)
  sock.binaryType = 'arraybuffer'

  sock.onopen = () => {
    const c = myCtl()
    if (c) c.connected = true
    store.setTabStatus(props.tab.uid, 'open')
    fitAndResize()
    startHeartbeat()
  }
  sock.onmessage = (ev) => {
    const data = typeof ev.data === 'string' ? ev.data : new TextDecoder().decode(ev.data)
    handleData(data)
  }
  sock.onclose = (ev: CloseEvent) => {
    stopHeartbeat()
    const c = myCtl()
    if (c) c.connected = false
    // 被其他设备接管：后端会先发 \x1b]detached\x07 再带这个关闭码（帧可能被代理吞掉，
    // 所以这里按码兜底），统一走 detached 分支，不打印通用的「连接已关闭」。
    if (ev?.code === WS_CLOSE_DETACHED) {
      markDetached()
      return
    }
    if (recreateOnClose) {
      // 旧会话已不存在：自动重建新会话
      recreateOnClose = false
      if (!disposed) {
        store.setTabStatus(props.tab.uid, 'connecting')
        openSocket()
      }
      return
    }
    if (!disposed) {
      term?.writeln('\r\n\x1b[31m' + t('conn_closed') + '\x1b[0m')
      scrollToBottom()
    }
  }
  sock.onerror = () => {
    const c = myCtl()
    if (c) c.connected = false
    if (!disposed) {
      term?.writeln('\r\n\x1b[31m' + t('ws_error') + '\x1b[0m')
      scrollToBottom()
    }
  }
}

const reconnect = () => {
  store.setTabStatus(props.tab.uid, 'connecting')
  // 重连 = 重置终端显示（清空缓冲），随后重新挂载会话——后端会从临时历史文件
  // 回放全部内容后再进入实时流，实现「重连同同步加载会话历史消息」。
  term?.reset()
  openSocket()
}

// ---------- 心跳保活（OSC 控制消息，不写入 PTY） ----------
function sendHeartbeat() {
  if (sock && sock.readyState === WebSocket.OPEN) sock.send(HEARTBEAT_PAYLOAD)
}
function startHeartbeat() {
  stopHeartbeat()
  sendHeartbeat()
  heartbeatTimer = window.setInterval(sendHeartbeat, HEARTBEAT_INTERVAL_MS)
}
function stopHeartbeat() {
  if (heartbeatTimer !== null) {
    clearInterval(heartbeatTimer)
    heartbeatTimer = null
  }
}

// ---------- 尺寸适配 ----------
// 桌面拖动窗口、移动端虚拟键盘起落都会让容器尺寸连续变化几十上百帧，而
// ResizeObserver 是逐帧回调的。若每次回调都 fit()，xterm 会逐帧 clear() 清屏、
// 重建 WebGL 字形图集并下发 resize（后端随即 SIGWINCH，shell 整屏重绘），
// 表现为终端区域闪烁 + 字体闪烁。因此把「尺寸变化」与「重排」解耦：
//   1) 变化期间只记录，不 fit、不发 resize（此时 canvas 被容器裁剪，画面静止）；
//   2) 尺寸稳定（静默 FIT_IDLE_MS）或连续变化超过 FIT_MAX_WAIT_MS 时，才 fit 一次；
//   3) 下发前比对 cols/rows，与上次相同则不发，避免无谓的 SIGWINCH 重绘。
const FIT_IDLE_MS = 160
const FIT_MAX_WAIT_MS = 800
let fitSettleTimer: number | null = null
let fitDeadlineTimer: number | null = null
// 最近一次下发给后端的 cols/rows：尺寸未变则不重复下发，避免无谓的 SIGWINCH 重绘
let lastSentCols = 0
let lastSentRows = 0

function sendResize(cols: number, rows: number) {
  if (cols === lastSentCols && rows === lastSentRows) return
  // 未连接时不记录：连接建立后（onopen → fitAndResize）会重新下发，
  // 否则会把「没发出去」当成「已发过」，导致前端尺寸与 PTY 尺寸不一致。
  if (!sock || sock.readyState !== WebSocket.OPEN) return
  lastSentCols = cols
  lastSentRows = rows
  sock.send(resizePayload(cols, rows))
}

// 重排一次：只在尺寸稳定后调用，或标签激活 / 字号变化 / 连接建立等明确的单次场景。
// fit() 内部在 cols/rows 变化时会 clear() 清屏并重建渲染模型，因此绝不能逐帧调用。
function fitAndResize() {
  // open() 之前没有可用的渲染维度，fit() 只会抛错刷日志（字体等待期可能被 RO 触发）
  if (!fitAddon || !term || !el.value || !termOpened) return
  try {
    const container = el.value
    if (container.clientWidth <= 0 || container.clientHeight <= 0) {
      if (fitRetryTimer === null) {
        fitRetryTimer = window.setTimeout(() => {
          fitRetryTimer = null
          fitAndResize()
        }, 120)
      }
      return
    }
    fitAddon.fit()
    const cols = term.cols
    const rows = term.rows
    if (cols > 0 && rows > 0) {
      sendResize(cols, rows)
      // 重排不应打断用户翻看历史：仅在跟随底部时回到最底
      scrollToBottom()
    }
  } catch (e) {
    console.warn('fitAndResize error:', e)
  }
}

function cancelPendingFit() {
  if (fitSettleTimer !== null) {
    clearTimeout(fitSettleTimer)
    fitSettleTimer = null
  }
  if (fitDeadlineTimer !== null) {
    clearTimeout(fitDeadlineTimer)
    fitDeadlineTimer = null
  }
}

// 尺寸变化入口（ResizeObserver / window.resize）：把「稳定后重排」的定时器不断后推，
// 变化期间不重排。连续变化超过 FIT_MAX_WAIT_MS 时兜底重排一次，避免长按拖动窗口
// 时终端长时间停在旧尺寸。
function debouncedFit() {
  if (disposed) return
  if (fitSettleTimer !== null) clearTimeout(fitSettleTimer)
  fitSettleTimer = window.setTimeout(() => {
    fitSettleTimer = null
    if (fitDeadlineTimer !== null) {
      clearTimeout(fitDeadlineTimer)
      fitDeadlineTimer = null
    }
    fitAndResize()
  }, FIT_IDLE_MS)
  if (fitDeadlineTimer === null) {
    fitDeadlineTimer = window.setTimeout(() => {
      fitDeadlineTimer = null
      if (fitSettleTimer !== null) {
        clearTimeout(fitSettleTimer)
        fitSettleTimer = null
      }
      fitAndResize()
    }, FIT_MAX_WAIT_MS)
  }
}

// ---------- 辅助键（KeypadBar 事件桥接） ----------
function sendKey(data: string) {
  focusTerm()
  if (sock && sock.readyState === WebSocket.OPEN) sock.send(data)
  pinToBottom()
}

function toggleModifier(mod: 'ctrl' | 'alt' | 'shift') {
  if (mod === 'ctrl') ctrlPressed.value = !ctrlPressed.value
  else if (mod === 'alt') altPressed.value = !altPressed.value
  else if (mod === 'shift') shiftPressed.value = !shiftPressed.value
  focusTerm()
}

function startRepeat(key: string) {
  if (repeatTimer) return
  sendKey(key)
  repeatTimer = window.setInterval(() => sendKey(key), 100)
}
function stopRepeat() {
  if (repeatTimer) {
    clearInterval(repeatTimer)
    repeatTimer = null
  }
}

// 复制没有按钮入口：桌面靠鼠标框选自动复制、移动端靠长按选词自动复制（见 onSelectionChange）。
// copyText 只被这两条路径调用。

// 复制 / 粘贴 / 清屏
// 剪贴板写入：优先异步 Clipboard API；非安全上下文（http 反代）时用 execCommand 兜底
function legacyCopy(text: string) {
  // 记下当前焦点：下面的临时 textarea 必须聚焦才能 select，而它会把焦点从终端
  // 的 .xterm-helper-textarea 抢走——移动端上「焦点离开输入框」即收起/拉不起软键盘，
  // 表现为「选中后键盘消失」。因此用完必须把焦点还给原来的元素。
  const prev = document.activeElement as HTMLElement | null
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.left = '-9999px'
  ta.style.top = '-9999px'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.focus()
  ta.select()
  try {
    document.execCommand('copy')
  } catch {
    /* 忽略 */
  }
  document.body.removeChild(ta)
  if (prev && prev !== ta && document.contains(prev) && typeof prev.focus === 'function') {
    prev.focus({ preventScroll: true })
  }
}

function copyText(text: string) {
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).catch(() => legacyCopy(text))
  } else {
    legacyCopy(text)
  }
}

// 复制按钮已移除：桌面由鼠标框选自动复制、移动端由长按选词自动复制取代。

async function pasteClipboard() {
  if (!sock || sock.readyState !== WebSocket.OPEN) {
    showToast(t('not_connected'))
    return
  }
  try {
    const text = await navigator.clipboard.readText()
    if (text) {
      sock.send(text)
      focusTerm()
      pinToBottom()
    }
  } catch {
    // 剪贴板不可读（非安全上下文 / 未授权）：提示用户改用系统键盘自带的粘贴
    showToast(t('paste_denied'))
  }
}

function clearTerminal() {
  term?.clear()
  pinToBottom()
  // 清屏同步清空后端临时历史文件：重连/刷新后不再回放已清除的内容
  if (props.tab.id) {
    api.clearSessionHistory(props.tab.id).catch((e) => console.warn('clear session history:', e))
  }
}

function showToast(msg: string) {
  const hint = toastEl.value
  if (!hint) return
  hint.textContent = msg
  hint.classList.remove('opacity-0', 'pointer-events-none')
  if (toastTimer) clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => {
    hint.classList.add('opacity-0', 'pointer-events-none')
    toastTimer = null
  }, 1400)
}

// ---------- 移动端触摸：自研单指滚动 + 滚动条拖动 + 长按选词复制 + 轻触聚焦 ----------
// 为什么全部自研：xterm 6.0.0（stable）的滚动由 VS Code 的 SmoothScrollableElement 用 JS
// 驱动（Viewport.ts），.xterm-viewport 的原生滚动是空壳（实测 scrollHeight === clientHeight，
// 设 scrollTop 无效），而 browser 层**没有任何触摸代码**、滚动条只监听 pointer 事件——
// 触屏上单指拖动无人响应、滚动条滑块抓不到。曾升级 6.1.0-beta 试图用其自带 Gesture 解决，
// 但 beta 的手势层 preventDefault 掉了浏览器的合成鼠标事件，安卓/iOS 上轻触拉不起键盘
// （已实测无效并回退），故滚动、滚动条拖动全部在本组件实现，不再依赖任何 xterm 触摸能力。
//
// 单指拖动 = 把纵向位移换算成行数调 term.scrollLines()（沿用回退前的自研实现）。
// 滚动条滑块**不需要**自研：xterm 6.0.0 在 .scrollbar 上监听 pointerdown，内部用
// setPointerCapture + window 级 pointermove，触摸/鼠标统一可用（曾实现过自接管版本，后证实
// 多余且与原生抢事件，已删）。本组件只负责 CSS 两件事：触屏下让滑块常驻可见、加宽热区
// （见 style.css 的 (hover: none) 段），否则 Auto 可见性依赖 hover、触屏永远等不到。
// 长按选词仍自研（xterm 浏览器层只监听鼠标事件，iOS 不合成）。
//
// 本组件的触摸职责四件：
//   - 按住不动超过 LONG_PRESS_MS → 长按选词（位移超过 TAP_SLOP 即取消，改为滚动）
//   - 单指纵向拖动 → 换算行数滚动
//   - 滚动条滑块拖动 → 交给 xterm 自带的 pointer 处理（本组件只保证触屏上能抓到，见 style.css）
//   - 其余 → 轻触：聚焦终端（Android 上这是拉起软键盘的**唯一**路径，见下）
//
// ⚠️ 轻触聚焦为何必须由本组件做：iOS Safari 本来就不把触摸合成鼠标事件（对
// user-select:none 的 canvas 完全不合成），xterm 聚焦 textarea 的 mousedown 路径在触屏上
// 不生效；Android 上虽然会合成，但一旦本组件在 touchstart/touchmove 里 preventDefault
// （拖动滚动必需），合成链就被切断，同样只剩 onTouchEnd 的 term.focus()。
// 阈值必须与 xterm 的 tap 判定（位移 <30px 且 <700ms）对齐：若用 10px，手指漂移 10~30px
// 时本组件判「拖动」不聚焦、xterm 又判 tap 不滚动 → 两端都不聚焦，键盘拉不起来
// （即「恢复会话后点终端不出键盘」的根因之一）。
const TAP_SLOP = 30 // px：位移超过即视为拖动（进入滚动），取消长按；≤30px 视为轻触
const LONG_PRESS_MS = 500 // ms：按住不动多久判定为长按

let touchStartX = 0
let touchStartY = 0
let touchActive = false
let touchMoved = false // 本次触摸已判定为拖动（本组件接管滚动）
let scrollAccum = 0 // 不足一行的残余像素（跨 touchmove 累计，避免慢速拖动丢行）
let longPressTimer: number | null = null // 长按计时器
let longPressFired = false // 本次触摸是否已触发长按选词
// 最近发往 PTY 的数据（含 xterm 自身发送与兜底补发），仅用于 iOS 第三方输入法兜底判重
// （见 installImeFallback）。环形截断，避免无限增长。
const recentSent: string[] = []
function recordSent(data: string) {
  recentSent.push(data)
  if (recentSent.length > 64) recentSent.shift()
}

// ---------- 组合提交去重（桌面端 xterm 会把同一次提交投递两遍） ----------
// 现象：**桌面端**用输入法提交中文时，PTY 收到两份，如敲「加油」得到「加油加油」；移动端无此问题。
// 成因（xterm 6.0.0 的既有缺陷，非本项目引入）：一次组合提交在 xterm 内部有两条投递路线——
//   ① 提交文本随 `input`(inputType=insertText) 或 `keypress` 事件立即投递
//      （CoreBrowserTerminal._inputEvent / _keyPress 各有一条 triggerDataEvent），
//   ② compositionend 之后 setTimeout(0) 再读 <textarea> 补投一次
//      （CompositionHelper._finalizeComposition 的 waitForPropagation 分支），
//      两条路线互不知情，于是同一段文本发两遍。上游同类报告：xtermjs/xterm.js
//      #3191 / #5023 / #6045 / #6049 / #6060 / #6078（6.0.0 与 master 均未修）。
// 移动端为什么不复现：软键盘输入法只走 composition 路径提交（候选字不另经 input/keypress
// 路线投递），故只有一条路线。
// 处理：compositionend 时记下本次提交文本，随后一个小窗口内把「同一段提交文本的重复投递」
// 丢掉；窗口内的其它内容（提交后立刻键入的下一个字符等）原样放行，新的 compositionstart
// 立即复位窗口（连续提交同一个词不会被误杀）。
// 分片（逐字）重投的判定窗口收紧到 50ms——真实的重复投递发生在同一拍（<5ms），而人手
// 连打两次同一次击键间隔远大于 50ms；整段文本一致的重投才用 200ms 窗口（人手不可能一次
// 事件投递整段文本）。
type CommitGuard = {
  text: string // 本次提交文本
  buffer: string // 窗口内已放行的内容（拼接）
  dupTail: string // 正在丢弃的重复分片（应对 keypress 路线逐字重投）
  deliveredAt: number // 提交文本完整投递的时刻（dupTail 判定用它算窗口）
  until: number
}
const COMMIT_DEDUPE_MS = 200 // 整段重复投递的去重窗口
const COMMIT_DUP_PARTIAL_MS = 50 // 分片重复投递的去重窗口
let commitGuard: CommitGuard | null = null

function armCommitGuard(text: string) {
  commitGuard = text
    ? { text, buffer: '', dupTail: '', deliveredAt: 0, until: Date.now() + COMMIT_DEDUPE_MS }
    : null
}

// 是否属于「同一次组合提交的重复投递」——是则调用方应丢弃这次发送。
function isDuplicateCommitChunk(data: string): boolean {
  const g = commitGuard
  if (!g || !data) return false
  if (Date.now() > g.until) {
    commitGuard = null
    return false
  }
  // 已在丢弃某个重复分片：按提交文本的顺序继续丢（'加' 之后跟 '油'）
  if (g.dupTail) {
    if (g.text.startsWith(g.dupTail + data)) {
      g.dupTail += data
      if (g.dupTail === g.text) g.dupTail = ''
      return true
    }
    g.dupTail = ''
  }
  // 提交文本已完整投递过：
  //   整段重投 → 丢；
  //   按前缀逐字重投（keypress 路线）→ 仅在紧随投递的一拍内丢（收紧窗口，避免误杀手打）
  if (g.buffer.indexOf(g.text) >= 0) {
    if (data === g.text) return true
    if (
      g.text.startsWith(data) &&
      g.deliveredAt > 0 &&
      Date.now() - g.deliveredAt <= COMMIT_DUP_PARTIAL_MS
    ) {
      g.dupTail = data
      return true
    }
    return false
  }
  g.buffer += data
  if (g.buffer.indexOf(g.text) >= 0) g.deliveredAt = Date.now()
  return false
}

// 组合提交路径的统一发送入口（其余路径如粘贴/快捷指令/辅助键仍直接 sock.send，
// 避免把去重窗口扩散到用户主动发送的内容上）。
function sendPty(data: string): boolean {
  if (!sock || sock.readyState !== WebSocket.OPEN) return false
  if (isDuplicateCommitChunk(data)) return false
  sock.send(data)
  recordSent(data)
  return true
}

// 选区自动复制的去重/节流标记。放在组件作用域是为了让长按选词能重置去重，
// 否则「对同一个单词长按第二次」会被当成重复文本而既不复制也不提示。
let lastAutoCopied = ''
let lastAutoCopyToastAt = 0

// 一个单元格（行）的像素高度。term.dimensions 不在公开 typings 里（proposed API），
// 故用挂载层内 .xterm-screen 的实测高度 ÷ 行数求得；两者都取不到时按字号估算。
function cellHeight(): number {
  const screen = el.value?.querySelector('.xterm-screen') as HTMLElement | null
  const rows = term?.rows ?? 0
  if (screen && rows > 0) {
    const h = screen.getBoundingClientRect().height / rows
    if (h > 0) return h
  }
  return settings.fontSize * 1.2
}

// 把纵向位移（手指下滑 dy>0 = 看更早内容）换算成行数并滚动。
// 返回 true 表示本次确实滚动了内容（可用于区分「滚动」与「不可滚动的轻触」）。
function scrollByPixels(dy: number): boolean {
  if (!term) return false
  const ch = cellHeight()
  scrollAccum += dy
  const lines = Math.trunc(scrollAccum / ch)
  if (lines === 0) return false
  scrollAccum -= lines * ch
  term.scrollLines(-lines)
  return true
}

// 触摸是否落在 xterm 的滚动条上：滚动条拖动由本组件按 pointer 事件接管（见下），
// 不参与长按选词与单指滚动。
// ⚠️ 6.0.0 的 DOM：.xterm-scrollable-element > .visible/.invisible.scrollbar.vertical > .slider
// （horizontal 的横条正常不渲染，但限定 .vertical 更稳）
function isScrollbarTouch(target: EventTarget | null): boolean {
  return (
    target instanceof Element &&
    !!target.closest('.xterm-scrollable-element > .scrollbar.vertical')
  )
}

// ---------- 滚动条拖动 ----------
// 滑块拖动**不需要自研**：xterm 6.0.0 在 .scrollbar 节点上监听 pointerdown（_domNodePointerDown
// → _sliderPointerDown → GlobalPointerMoveMonitor），内部已做 setPointerCapture + window 级
// pointermove（含 preventDefault），触摸/鼠标统一可用——真实触摸拖动实测正常。
// 本组件要做的只有两件 CSS 事（见 style.css）：① (hover: none) 下让滑块常驻可见并恢复
// pointer-events（Auto 可见性靠 hover，触屏永远等不到，否则滑块抓不到）；② touch-action:none
// 防止拖滑块被浏览器认领成页面平移。触摸到滚动条时本组件不启动选词/滚动/聚焦即可。

// 触摸点 → 缓冲区坐标（列、绝对行号）。行号与 xterm 的 SelectionService 保持一致：
// 视口内行 + viewportY（其内部是 ydisp，等价）。取不到返回 null。
function pointToCell(clientX: number, clientY: number): { col: number; row: number } | null {
  const screen = el.value?.querySelector('.xterm-screen') as HTMLElement | null
  if (!screen || !term || term.cols <= 0 || term.rows <= 0) return null
  const rect = screen.getBoundingClientRect()
  if (rect.width <= 0 || rect.height <= 0) return null
  const cellW = rect.width / term.cols
  const cellH = rect.height / term.rows
  const col = Math.floor((clientX - rect.left) / cellW)
  const rowInViewport = Math.floor((clientY - rect.top) / cellH)
  if (col < 0 || col >= term.cols || rowInViewport < 0 || rowInViewport >= term.rows) return null
  return { col, row: term.buffer.active.viewportY + rowInViewport }
}

// 长按选词：选中该单元格所在的一个单词并返回文本；空白处返回空串。
// 单词边界按 xterm 的 wordSeparator（默认 ' ()[]{}\',"`'）判定，与 xterm 自身双击选词规则一致；
// 为覆盖中文/路径等无空格内容，额外把制表符也视为分隔符。
type WordSelection = { word: string; col: number; row: number; length: number }

// xterm 的默认 wordSeparator（OptionsService 的默认值），仅作为读取不到选项时的兜底
const DEFAULT_WORD_SEPARATOR = ' ()[]{}\',"`'

function selectWordAt(clientX: number, clientY: number): WordSelection | null {
  if (!term) return null
  const cell = pointToCell(clientX, clientY)
  if (!cell) return null
  const line = term.buffer.active.getLine(cell.row)
  if (!line) return null
  const text = line.translateToString(true) // 去掉右侧空白
  if (!text) return null
  const sep = term.options.wordSeparator ?? DEFAULT_WORD_SEPARATOR
  const isSep = (ch: string) => ch === '\t' || sep.includes(ch)

  // 定位落点所在单词的起止。两种情形：
  //  - 落点本身是单词字符 → 先向左回退到词首（否则从词中间起算会截断，如 world 的第 3 字符起 → "rld"）
  //  - 落点是分隔符（空格等）→ 向右吸附到下一个词首，与 xterm 双击选词的取向一致
  let idx = Math.min(cell.col, text.length - 1)
  if (idx < 0) return null
  if (isSep(text[idx])) {
    while (idx < text.length && isSep(text[idx])) idx++
    if (idx >= text.length) {
      // 落点右侧全是空白（点行尾空白）：向左回退到最近一个单词
      idx = Math.min(cell.col, text.length - 1)
      while (idx >= 0 && isSep(text[idx])) idx--
      if (idx < 0) return null
    }
  }
  let start = idx
  while (start > 0 && !isSep(text[start - 1])) start--
  let end = idx
  while (end < text.length && !isSep(text[end])) end++
  if (end <= start) return null
  // xterm 的 select(column, row, length)：column/row 为缓冲区绝对坐标，length 为字符数
  return { word: text.slice(start, end), col: start, row: cell.row, length: end - start }
}

// 长按选词 + 复制：先选中并自动复制（不依赖任何浏览器手势授权，最可靠），
// 再尝试调起系统原生菜单（可选增强，失败不影响复制结果）。
function handleLongPress(clientX: number, clientY: number) {
  const hit = selectWordAt(clientX, clientY)
  // 无论落点有没有单词，都必须把焦点交回终端：长按与轻触一样是「用户要点终端」的
  // 意图，键盘必须能拉起。曾经只在「落点无词」时 focus()，于是**恢复的标签**（满屏
  // 历史文字，落点几乎必中单词）长按后只选中不聚焦 → 焦点停在 BODY，键盘拉不起来；
  // 新建标签是空白屏，落点无词走 focus() 分支，所以看起来「只有恢复的标签不行」。
  focusTerm()
  if (!hit) {
    // 落点无单词：清掉可能存在的旧选区即可
    term?.clearSelection()
    return
  }
  // 重置去重标记：下面的 select() 会同步触发 onSelectionChange（见 initTerminal），
  // 由那里统一负责「复制到剪贴板 + toast」，避免两处重复复制。
  lastAutoCopied = ''
  lastAutoCopyToastAt = 0
  term?.select(hit.col, hit.row, hit.length)
  // 只做「选中 + 自动复制」：不调起系统文本选择器/原生菜单（已按需求移除）。
  // 复制由 onSelectionChange 统一完成（见 initTerminal）。
}

function cancelLongPress() {
  if (longPressTimer !== null) {
    clearTimeout(longPressTimer)
    longPressTimer = null
  }
}

function onTouchStart(e: TouchEvent) {
  cancelLongPress()
  longPressFired = false
  touchMoved = false
  scrollAccum = 0
  // 滚动条上的触摸交给滚动条拖动（本组件的 pointer 接管），不参与选词/滚动/轻触
  if (isScrollbarTouch(e.target)) {
    touchActive = false
    return
  }
  if (e.touches.length !== 1) {
    // 多指（捏合等）不参与选词与滚动
    touchActive = false
    return
  }
  const touch = e.touches[0]
  touchStartX = touch.clientX
  touchStartY = touch.clientY
  touchActive = true
  // 长按选词：按住不动到时长后触发（位移超阈值会在 onTouchMove 里取消）
  const { clientX, clientY } = touch
  longPressTimer = window.setTimeout(() => {
    longPressTimer = null
    if (!touchActive || touchMoved) return
    longPressFired = true
    handleLongPress(clientX, clientY)
  }, LONG_PRESS_MS)
}

function onTouchMove(e: TouchEvent) {
  if (!touchActive) return
  const touch = e.touches[0]
  if (!touch) return
  const dy = touch.clientY - touchStartY
  const dx = touch.clientX - touchStartX
  if (!touchMoved) {
    // 未越过阈值前不滚动：横向位移明显大于纵向时不判为拖动（保留系统文字选择手感）
    if (Math.abs(dy) < TAP_SLOP || Math.abs(dy) < Math.abs(dx)) return
    touchMoved = true
    cancelLongPress() // 已判定为拖动，本次触摸不再长按选词
    scrollAccum = 0
    touchStartY = touch.clientY // 以越过阈值处为滚动起点，避免起手跳一行
    return
  }
  // 自研滚动：纵向位移换算成行数（手指下滑 = 看更早内容）。preventDefault 阻止
  // 浏览器把本次拖动认领为页面平移（.term-container 已声明 touch-action: none 兜底）。
  if (e.cancelable) e.preventDefault()
  scrollByPixels(touch.clientY - touchStartY)
  touchStartY = touch.clientY
}

function onTouchEnd() {
  const wasLongPress = longPressFired
  const wasMoved = touchMoved
  cancelLongPress()
  touchActive = false
  touchMoved = false
  longPressFired = false
  if (wasLongPress || wasMoved) return
  // 轻触：聚焦终端。这是移动端拉起软键盘的可靠路径（iOS 不合成鼠标事件，Android 上
  // 本组件在 touchmove 里 preventDefault 会切断合成链，都只剩这里）。
  // 文字选择/复制由长按触发——见 handleLongPress。
  focusTerm()
}

// 聚焦终端。term.open() 之前 textarea 还不存在，此时 focus() 会被 xterm 静默丢弃
// （内部 `if (this.textarea)`），于是「首次触摸无效、再点一次才行」——恢复会话时
// open() 要等 document.fonts.ready（最坏 800ms 兜底），这个窗口相当长。
// 因此把聚焦意图记下来，open() 完成后补发一次。
function focusTerm() {
  if (termOpened) {
    term?.focus()
    return
  }
  pendingFocus = true
}

// ---------- iOS 第三方输入法兜底 ----------
// 现象（仅 iOS 第三方键盘，原生键盘正常）：中文输入一会儿能用一会儿失效；英文偶发丢字符。
//
// 在 HEAD 基线上穷举真实事件形态（CDP 派发）后的结论：
//   - `Input.insertText`（第三方键盘英文/数字常见形态）→ xterm 正常发出，**不需要干预**
//   - `keyDown/keyUp` → 正常发出，**不需要干预**
//   - IME 组合提交（中文）→ **完全丢失**（PTY 收不到任何内容），这才是真正的问题
// 实测事件序列：
//   compositionstart → compositionupdate:ni → input:ni:insertCompositionText
//   → compositionupdate:你 → input:你:insertCompositionText
//   → compositionupdate("") → input:deleteContentBackward → compositionend
// 即第三方键盘提交候选字后**立即清空** textarea，而 xterm 的
// CompositionHelper._finalizeComposition 是「compositionend 后用 setTimeout(0) 再读
// textarea.value.substring(start)」——此时已读到空串，候选字永远发不出去。
// 且 compositionend.data 在 iOS 上常为空串，只能靠 compositionupdate 的候选文本。
//
// 因此兜底**只覆盖 composition 路径**，绝不介入 insertText/keydown —— 那些路径 xterm 本来
// 就正常，介入只会造成重复发送（这一点已在实现中踩过：范围放宽后英文变成双份）。
//
// 兜底的判据与「桌面端重复投递去重」见上「组合提交去重」：桌面端同一次提交会被 xterm 投递
// 两遍，靠那里的 commitGuard 丢掉多出来的一份；这里只负责 iOS 那种「一条路线都没发出」的补发，
// 判重按窗口内发送内容的**拼接**比对（桌面端 keypress 路线可能逐字投递，逐条比对会漏判）。
function installImeFallback() {
  const textarea = el.value?.querySelector('.xterm-helper-textarea') as HTMLTextAreaElement | null
  if (!textarea) return

  // 补发窗口：必须晚于 xterm 的 composition 发送时机（其内部为 setTimeout(0)）。
  // 50ms 足以让它落地，又远短于人眼可感的延迟。
  const FLUSH_DELAY_MS = 50

  let candidate = '' // 最近一次 compositionupdate 的候选文本（iOS 提交后唯一可靠的来源）

  const onStart = () => {
    candidate = ''
    // 新的组合开始 → 上一次提交的去重窗口立即失效（连续提交同一个词不算重复）
    commitGuard = null
  }
  const onUpdate = (e: CompositionEvent) => {
    if (e.data) candidate = e.data
  }
  const onEnd = (e: CompositionEvent) => {
    // compositionend.data 是浏览器给出的「权威提交文本」，但它并非所有平台都可用
    // ——iOS 第三方键盘上常为空串（见上），此时退回 compositionupdate 的候选文本。
    // 两者的取舍很关键：桌面端某些输入法的最后一次 compositionupdate 携带的是**拼音**
    // 预编辑串（如 "jiayou"），提交文本只在 compositionend.data 里，若把它当作提交文本
    // 去补发，就会在正确的中文后面多出一串拼音。
    const endData = e.data || ''
    const text = endData || candidate
    const candidates = endData && candidate && endData !== candidate ? [endData, candidate] : [text]
    candidate = ''
    // 布防：桌面端 xterm 会把同一次提交投递两遍，重复的那一份在 term.onData 里丢掉
    armCommitGuard(text)
    if (!text) return
    // 判据：xterm 在这段时间内是否已把提交文本发出去（它正常时会自行发出）。
    // 必须把窗口内的发送**拼接**后再比对：桌面端的 keypress 路线可能逐字投递（'加'、'油'），
    // 逐条 includes 会漏判成「没发过」，于是整段再补发一次 → 重复。
    const mark = recentSent.length
    setTimeout(() => {
      const joined = recentSent.slice(mark).join('')
      if (candidates.some((t) => joined.indexOf(t) >= 0)) return // xterm 已发出
      sendPty(text)
    }, FLUSH_DELAY_MS)
  }

  textarea.addEventListener('compositionstart', onStart, true)
  textarea.addEventListener('compositionupdate', onUpdate, true)
  textarea.addEventListener('compositionend', onEnd, true)

  // 键盘守卫（issue #4486 workaround，见 initTerminal）也挂在同一个 textarea 上，
  // 二者合用一次「open() 之后」的安装时机。
  imeGuard?.attach(textarea)
}

// ---------- 初始化 ----------
// ---------- 终端字体（内置 Maple Mono CN，见 src/main.ts） ----------
// 只内置 Regular 400；身后是系统等宽/中文兜底，供 Maple 未覆盖的字形（emoji 等）使用。
const TERMINAL_FONT_FAMILY =
  '"Maple Mono CN", ui-monospace, SFMono-Regular, Menlo, Consolas, "Cascadia Mono", "Noto Sans Mono CJK SC", "PingFang SC", "Microsoft YaHei", monospace'
// 等价写法（家族名不带引号）：xterm 只在 fontFamily/fontSize 的**值发生变化**时才重新测量
// 字符尺寸（OptionsService 里 `rawOptions[k] !== v && fire`），重复赋同一个字符串不会触发——
// 字体迟到时靠它强制重测一次。
const TERMINAL_FONT_FAMILY_RETRY = TERMINAL_FONT_FAMILY.replace('"Maple Mono CN"', 'Maple Mono CN')
// 等字体的兜底时限：超时也先让终端可用，之后字体就绪时再重测一次（见下）
const FONT_WAIT_MS = 800
// 探测文本要同时覆盖 latin 与中文，才会把对应 unicode-range 切片都取回来
const FONT_PROBE_TEXT = 'Aa中0'

// 等终端字体就绪。**必须在 open() 之前**：xterm 只在 open() 里量一次字符尺寸，
// 那一刻若还在用兜底字体，算出的行列数与 Maple 的真实字宽不符——渲染错位，
// 下发给 PTY 的 cols/rows 也是错的。
async function waitTerminalFont(size: number): Promise<boolean> {
  if (typeof document === 'undefined' || !document.fonts) return true
  const spec = `${size}px "Maple Mono CN"`
  if (document.fonts.check(spec, FONT_PROBE_TEXT)) return true
  const timeout = new Promise<boolean>((resolve) =>
    window.setTimeout(() => resolve(false), FONT_WAIT_MS)
  )
  const loaded = document.fonts
    .load(spec, FONT_PROBE_TEXT)
    .then(() => true)
    .catch(() => false)
  const ok = await Promise.race([loaded, timeout])
  return ok && document.fonts.check(spec, FONT_PROBE_TEXT)
}

function initTerminal() {
  if (!el.value || term) return
  term = new Terminal({
    cursorBlink: true,
    fontSize: settings.fontSize,
    fontFamily: TERMINAL_FONT_FAMILY,
    theme: isDark.value ? DARK_PALETTE : LIGHT_PALETTE,
    scrollback: 2000,
    letterSpacing: 0,
    allowProposedApi: true
  })

  // ---------- iOS 第三方输入法的键盘守卫（issue #4486 的 workaround 路线） ----------
  // 现象：中文 IME 输入「、」「。」等标点时，第三方键盘会把该按键映射成替代码（如 `\`）
  // 发一个非 229 的 keydown。xterm 的 CompositionHelper 看到非 229 的 keydown 会先
  // _finalizeComposition(false) 结束组合，再由 _keyDown 把**原始键码**当普通键发出 ——
  // 于是 PTY 收到 `\` 而不是「、」，且组合态被破坏。原生键盘无此问题。
  // 解法（同 microsoft/vscode#320525、code-by-wire 988a78c）：composition 期间让 xterm
  // 忽略一切键盘事件。自定义 handler 在 _keyDown/_keyUp/_keyPress 里**先于**其他处理被调用，
  // 返回 false 即全部拦截；组合文本仍会经 compositionend 正常提交，不受影响。
  // ① 自维护 composing 标志：iOS 第三方键盘的 KeyboardEvent.isComposing 并不可靠，
  //    以 compositionstart/end 为准更稳；② 同时读 e.isComposing 兜底。
  // 该守卫只影响「组合期间的物理键」，输入结束后立即放行，不影响英文/数字直输路径。
  let imeComposing = false
  const startGuard = () => {
    imeComposing = true
  }
  const endGuard = () => {
    // compositionend 后浏览器可能还有一帧同组合的事件，延后一拍再放行
    window.setTimeout(() => {
      imeComposing = false
    }, 0)
  }
  term.attachCustomKeyEventHandler((ev) => {
    if (ev.type !== 'keydown' && ev.type !== 'keyup' && ev.type !== 'keypress') return true
    return !(imeComposing || ev.isComposing)
  })
  // 标志的维护挂在 textarea 的组合事件上，须在 term.open() 之后（见 installImeFallback）。
  imeGuard = {
    attach(textarea) {
      textarea.addEventListener('compositionstart', startGuard, true)
      textarea.addEventListener('compositionend', endGuard, true)
    },
    detach(textarea) {
      textarea.removeEventListener('compositionstart', startGuard, true)
      textarea.removeEventListener('compositionend', endGuard, true)
    }
  }

  // 各种 addon：各自独立 try/catch，避免单个失败拖垮终端
  try {
    const u11 = new Unicode11Addon()
    term.loadAddon(u11)
    if (term.unicode) term.unicode.activeVersion = '11'
  } catch (e) {
    console.warn('unicode11 addon:', e)
  }
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  // 渲染器选择：**嵌在 iframe 里时（桌面外壳窗口）默认用 xterm 内置 DOM 渲染器**。
  // 原因：外壳「拖动窗口」= 移动 iframe 元素 → 合成器每帧要把 iframe 内容重新光栅化，而
  // WebGL 的绘图缓冲在每次合成后即被清空，那一帧 canvas 就是空的 —— 表现为拖动时文字/光标
  // 整块闪（背景看着正常，因为主题背景同时画在 .xterm-viewport 的 CSS 背景上）。开
  // preserveDrawingBuffer 也压不住（真机实测仍闪），换 DOM 渲染器即不闪（DOM 内容不依赖
  // 绘图缓冲）。顶层标签页不复现——平移顶层页面是纯合成操作，不需要重光栅——继续用 WebGL
  // 保吞吐。所以这里不要改成「一律 DOM」，也不要退回「一律 WebGL」；详见 AGENTS.md。
  // ?nogl=1 可把顶层也强制成 DOM（二分显示问题用）。
  if (webglDisabled()) {
    console.warn('renderer: DOM (forced by ?nogl=1)')
  } else if (inEmbeddedFrame()) {
    console.warn('renderer: DOM (embedded in an iframe)')
  } else {
    try {
      const addon = new WebglAddon()
      // 上下文丢失（GPU 驱动重置 / 图层重建失败）后渲染器已不可用，不摘除会永久停在黑屏：
      // 摘掉 addon 后 xterm 自动换回内置 DOM 渲染器并重绘一次（上游推荐用法）。
      addon.onContextLoss(() => {
        console.warn('webgl context loss → fallback to DOM renderer')
        if (webglAddon === addon) webglAddon = null
        addon.dispose()
        term?.refresh(0, (term.rows || 1) - 1)
      })
      term.loadAddon(addon)
      webglAddon = addon
    } catch (e) {
      // WebGL 不可用时回退到 xterm 内置 DOM 渲染器
      console.warn('webgl addon:', e)
    }
  }
  try {
    term.loadAddon(new WebLinksAddon())
  } catch (e) {
    console.warn('web-links addon:', e)
  }
  try {
    searchAddon = new SearchAddon()
    term.loadAddon(searchAddon)
    searchAddon.onDidChangeResults((res) => {
      searchIndex.value = res.resultIndex
      searchCount.value = res.resultCount
    })
  } catch (e) {
    console.warn('search addon:', e)
  }
  try {
    // OSC 52 剪贴板：**只写不读**。
    // 默认 provider（BrowserClipboardProvider）在程序发 `ESC]52;c;?BEL` 时会用
    // navigator.clipboard.readText() 读走系统剪贴板，再经 terminal.input() 把内容当**键盘输入**
    // 灌回 PTY —— 任何被 cat/curl 出来的内容都能这样读到你的剪贴板（多数终端默认禁止该「读」）。
    // 这里读一律返回空串；写改走本组件自己的 copyText（https 用 Clipboard API、http 用 legacyCopy），
    // 于是 tmux/vim 的「复制到系统剪贴板」照常可用，且 http 部署下 OSC 52 写入也不再报错。
    const clipboardProvider: IClipboardProvider = {
      readText: () => '',
      writeText: (_selection: ClipboardSelectionType, text: string) => {
        copyText(text)
      }
    }
    term.loadAddon(new ClipboardAddon(new Base64(), clipboardProvider))
  } catch (e) {
    console.warn('clipboard addon:', e)
  }
  // 刻意不加载 SerializeAddon（无导出需求）与 ImageAddon（iip/sixel 默认 storageLimit 128MB、
  // pixelLimit 数百万像素，终端里 cat 一个恶意文件就能驱动解码/缓存）。要用再按需加回并显式限流。

  // open() 要等终端字体就绪（见 waitTerminalFont）：终端字号 10–26px、字宽因字体而异，
  // 拿兜底字体量出来的行列是错的。等待上限 FONT_WAIT_MS（字体已由 main.ts 预取，正常几乎
  // 立即返回）；超时也照常 open()，随后字体真就绪时再强制重测一次。
  //
  // 注意窗口期变长带来的老问题：open() 之前 .xterm-helper-textarea 不存在，`term.focus()`
  // 会被 xterm 静默丢弃（内部 `if (this.textarea)` 直接返回），表现为「首次点终端没反应，
  // 再点一次才行」。这里沿用既有的 pendingFocus 机制（轻触先把意图挂起，open() 后立即补发）；
  // iOS 上补发无效（回调不在用户手势内，弹不出键盘），所以字体等待必须短——FONT_WAIT_MS 兜底。
  void (async () => {
    const fontReady = await waitTerminalFont(settings.fontSize)
    if (disposed || !term || !el.value) return
    try {
      term.open(el.value as HTMLElement)
      termOpened = true
    } catch (e) {
      console.warn('term.open:', e)
    }
    // 必须在 term.open() 之后：.xterm-helper-textarea 由 open() 创建
    installImeFallback()
    // open() 之前若有轻触把聚焦意图挂起，此刻立即补发
    if (pendingFocus) {
      pendingFocus = false
      term.focus()
    }
    nextTick(() => {
      requestAnimationFrame(() => fitAndResize())
    })
    if (!fontReady) {
      // 字体迟到：等它真正加载完成，用**不同的字符串**再赋一次同族字体以触发 xterm 重测，
      // 然后按新字宽 fit（sendResize 只在 cols/rows 变化时下发，不会重复打扰 PTY）。
      void document.fonts.ready.then(() => {
        if (disposed || !term) return
        term.options.fontFamily = TERMINAL_FONT_FAMILY_RETRY
        requestAnimationFrame(() => fitAndResize())
      })
    }
  })()

  if (window.ResizeObserver) {
    resizeObserver = new ResizeObserver(debouncedFit)
    resizeObserver.observe(el.value)
  } else {
    window.addEventListener('resize', debouncedFit)
  }

  // 桌面端原生粘贴（Ctrl+Shift+V / 右键粘贴）
  onPasteEvent = (ev: ClipboardEvent) => {
    if (!sock || sock.readyState !== WebSocket.OPEN) return
    const text = ev.clipboardData?.getData('text/plain')
    if (text) {
      ev.preventDefault()
      sock.send(text)
      pinToBottom()
    }
  }
  el.value.addEventListener('paste', onPasteEvent)

  term.onData((data) => {
    if (!sock || sock.readyState !== WebSocket.OPEN) return
    let toSend = data
    const hadModifier = ctrlPressed.value || altPressed.value
    if (ctrlPressed.value && data.length === 1) {
      const code = data.charCodeAt(0)
      if (code >= 97 && code <= 122) toSend = String.fromCharCode(code - 96)
      else if (code >= 65 && code <= 90) toSend = String.fromCharCode(code - 64)
    } else if (altPressed.value && data.length === 1) {
      toSend = '\x1b' + data
    }
    // 桌面端同一次组合提交会被 xterm 投递两遍（input/keypress 路线 + compositionend 延迟
    // finalize），第二份在这里丢掉；其余输入不受影响（见上「组合提交去重」）。
    sendPty(toSend) // 供 iOS 第三方输入法兜底判断「xterm 是否已自行发出」
    // 修饰键输入一次后自动解除：点击修饰键 → 键入任意按键 → 修饰键复位。
    // **Shift 例外**：它是辅助键条的「上档锁定」，要一直有效到再点一次 Shift 才解除
    // （见 KeypadBar 的 SHIFT_MAP），因此不参与这里的自动复位。
    if (hadModifier) {
      ctrlPressed.value = false
      altPressed.value = false
    }
  })

  // 维护「跟随底部」标记：滚动位置不在最底（含用户拖动/滚轮/翻页）时停止自动跟随，
  // 回到最底时恢复。这样后台输出与尺寸重排不会把用户翻上去的历史顶掉。
  term.onScroll(() => {
    if (!term) return
    const buf = term.buffer.active
    stickToBottom = buf.viewportY >= buf.baseY
  })

  // iOS 第三方输入法（搜狗/百度/微信键盘等）组合输入修复。
  //
  // 现象：中文只能输入一次、之后再也输入不进去；英文要敲好几次才出一个字符（原生输入法正常）。
  //
  // 实测事件序列（CDP Input.imeSetComposition 复现真实 IME）：
  //   compositionstart → compositionupdate:ni → input:ni:insertCompositionText
  //   → compositionupdate:你 → input:你:insertCompositionText
  //   → compositionupdate("") → input:deleteContentBackward → compositionend
  // 即：**提交候选字后输入法立刻把 textarea 内容删掉**，且 compositionend.data 为空串。
  // 而 xterm 的 CompositionHelper._finalizeComposition 是「compositionend 后用
  // setTimeout(0) 再读 textarea.value.substring(start)」——那时内容已被删除，读到空串，
  // 于是候选字永远不会发往 PTY（第二轮起更是连组合起点都被污染）。
  //
  // 修法：在 compositionupdate 阶段记下候选文本（那是唯一可靠的来源），
  // compositionend 时若发现 xterm 没有把它发出去，就补发一次。
  // 只补发「未发出」的内容，因此对原生输入法（xterm 正常发送）不会造成重复。
  // 判重口径在 2026-09 修正为「窗口内发送内容的拼接」并优先采用 compositionend.data，
  // 桌面端同一次提交的重复投递由「组合提交去重」的 commitGuard 处理（见上）。
  // 安装点见 openAndFit（必须等 term.open() 建出 .xterm-helper-textarea）。

  // 桌面端鼠标框选 / 移动端长按选词后，自动把选中文本复制进剪贴板并 toast 提示。
  // 连续选择变化时按文本去重 + 1.2s 节流，避免刷屏。
  term.onSelectionChange(() => {
    const sel = term?.getSelection()
    if (!sel || !sel.trim() || sel === lastAutoCopied) return
    lastAutoCopied = sel
    copyText(sel)
    const now = Date.now()
    if (now - lastAutoCopyToastAt > 1200) {
      lastAutoCopyToastAt = now
      toast.show(t('copied'))
    }
  })
}

// 注册顶栏命令入口（复制/粘贴/清屏/重连/发送）——按标签 uid 登记，
// App 依据激活标签 uid 解析，避免后台标签覆盖。
function myCtl() {
  return pc.registry[props.tab.uid]
}

function regControls() {
  pc.register(props.tab.uid, {
    paste: pasteClipboard,
    clear: clearTerminal,
    reconnect,
    search: toggleSearch,
    focus: focusTerm,
    send: (data: string) => {
      if (sock && sock.readyState === WebSocket.OPEN) {
        sock.send(data)
        pinToBottom()
        return true
      }
      return false
    },
    connected: false
  })
}

watch(
  () => isDark.value,
  (d) => {
    if (term) term.options.theme = d ? DARK_PALETTE : LIGHT_PALETTE
  }
)

// 终端字号（设置弹窗调整，保存在浏览器存储）：即时生效并重新适配尺寸
watch(
  () => settings.fontSize,
  (n) => {
    if (!term) return
    term.options.fontSize = n
    nextTick(() => requestAnimationFrame(fitAndResize))
  }
)

// 搜索词变化 → 增量搜索；清空 → 清除高亮
watch(searchTerm, onSearchTermChange)

watch(
  () => props.active,
  (v) => {
    if (v) {
      nextTick(() => {
        requestAnimationFrame(() => fitAndResize())
        focusTerm()
        // 切到本标签时用户意图是继续操作，回到最新内容
        stickToBottom = true
        term?.scrollToBottom()
      })
    }
  }
)

onMounted(() => {
  initTerminal()
  openSocket()
  regControls()
})

onBeforeUnmount(() => {
  disposed = true
  pc.unregister(props.tab.uid)
  stopHeartbeat()
  cancelLongPress()
  // 键盘守卫的 composition 监听挂在 textarea 上，textarea 随 term.dispose() 一并销毁，
  // 无需手动解绑；这里仅清空引用，防止 dispose 后再被误用。
  imeGuard = null
  if (fitRetryTimer) clearTimeout(fitRetryTimer)
  if (toastTimer) clearTimeout(toastTimer)
  if (repeatTimer) clearInterval(repeatTimer)
  if (onPasteEvent && el.value) {
    el.value.removeEventListener('paste', onPasteEvent)
    onPasteEvent = null
  }
  disconnect()
  if (resizeObserver) {
    resizeObserver.disconnect()
    resizeObserver = null
  } else {
    window.removeEventListener('resize', debouncedFit)
  }
  cancelPendingFit()
  // webglAddon 由 term.dispose() 一并释放；这里只清引用，避免 dispose 后再被误用
  webglAddon = null
  term?.dispose()
  term = null
})
</script>

<template>
  <div class="flex flex-col h-full min-h-0 overflow-hidden bg-bg dark:bg-bg-dark">
    <!-- 终端容器：只负责外层视觉留白与裁剪，xterm 挂在内层挂载层上 -->
    <!-- 深色模式下 terminal-area 背景为 #1A1A1A、文字为 #4EC9B0；浅色模式下背景为米黄色#faf5e9、文字为#1a1814
         左侧与下方各加 10px 边框（颜色跟随终端区域颜色），无分隔线 -->
    <div
      class="term-container flex-1 min-h-0 relative bg-[#faf5e9] dark:bg-[#1A1A1A] dark:text-[#4EC9B0]
             border-l-[10px] border-b-[10px]
             border-[#faf5e9] dark:border-[#1A1A1A]"
      @touchstart="onTouchStart"
      @touchmove="onTouchMove"
      @touchend="onTouchEnd"
      @touchcancel="onTouchEnd"
    >
      <!-- xterm 挂载层：绝对定位铺满外层内容盒，自身无 border / padding。
           FitAddon 按本层尺寸换算行列，故外层的 10px 视觉边框不会被算进可用高度。 -->
      <div ref="el" class="term-mount">
        <div
          ref="toastEl"
          class="term-copy-toast absolute bottom-2 right-2 z-30 px-3 py-1.5 rounded-md bg-black/70 text-white text-xs font-medium shadow-card opacity-0 pointer-events-none transition-opacity duration-200 whitespace-nowrap"
        ></div>

        <!-- 搜索悬浮框（仅桌面端按钮触发）：输入框 + ↑↓ + 计数(2/7) + 关闭 -->
        <div
          v-if="searchOpen"
          class="absolute top-2 right-2 z-40 flex items-center gap-1 rounded-lg bg-surface dark:bg-surface-dark border border-line dark:border-line-dark shadow-pop px-2 py-1.5"
          @mousedown.stop
        >
          <input
            ref="searchInput"
            v-model="searchTerm"
            class="w-28 sm:w-44 bg-transparent outline-none text-sm text-ink dark:text-ink-dark placeholder:text-ink-faint"
            :placeholder="t('act_search')"
            spellcheck="false"
            autocomplete="off"
            @keydown="onSearchKeydown"
          />
          <span class="text-xs font-mono text-ink-faint dark:text-ink-faint-dark whitespace-nowrap">{{ searchLabel }}</span>
          <button
            class="w-7 h-7 flex items-center justify-center rounded-md text-ink-soft dark:text-ink-soft-dark hover:bg-black/5 dark:hover:bg-white/5"
            :title="t('act_search') + ' ↑'"
            @click="searchPrev"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4">
              <path d="m18 15-6-6-6 6" />
            </svg>
          </button>
          <button
            class="w-7 h-7 flex items-center justify-center rounded-md text-ink-soft dark:text-ink-soft-dark hover:bg-black/5 dark:hover:bg-white/5"
            :title="t('act_search') + ' ↓'"
            @click="searchNext"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4">
              <path d="m6 9 6 6 6-6" />
            </svg>
          </button>
          <button
            class="w-7 h-7 flex items-center justify-center rounded-md text-ink-soft dark:text-ink-soft-dark hover:bg-black/5 dark:hover:bg-white/5"
            :title="t('tab_close')"
            @click="closeSearch"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" class="w-4 h-4">
              <line x1="18" y1="6" x2="6" y2="18" />
              <line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>
      </div>
    </div>

    <!-- 移动端辅助键条 -->
    <KeypadBar
      :ctrl="ctrlPressed"
      :alt="altPressed"
      :shift="shiftPressed"
      @key="sendKey"
      @toggle="toggleModifier"
    />
  </div>
</template>