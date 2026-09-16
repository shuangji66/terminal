<script setup lang="ts">
// TerminalPane — 一个终端标签：持有独立的 xterm 实例 + WebSocket 会话。
// - 已有会话 id：后端在挂载时自动回放历史（→ \x1b]ready\x07），再进入实时流
// - 新会话（无 id）：后端回 \x1b]id;<id>\x07 控制帧回填会话 id
// - 浏览器断开只解挂载、不杀会话；「重连」重新挂载并再次回放历史
import { onMounted, onBeforeUnmount, ref, computed, watch, nextTick } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { SearchAddon } from '@xterm/addon-search'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { ClipboardAddon } from '@xterm/addon-clipboard'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { SerializeAddon } from '@xterm/addon-serialize'
import { ImageAddon } from '@xterm/addon-image'
import { useTheme } from '@/composables/useTheme'
import { t } from '@/i18n'
import { useSessionsStore, type Tab } from '@/stores/sessions'
import { usePaneControlsStore } from '@/stores/paneControls'
import { useToastStore } from '@/stores/toast'
import { useSettingsStore } from '@/stores/settings'
import { wsUrl, resizePayload, HEARTBEAT_PAYLOAD, api } from '@/serverapi'
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

let term: Terminal | null = null
let fitAddon: FitAddon | null = null
let searchAddon: SearchAddon | null = null
let sock: WebSocket | null = null
let termOpened = false
let disposed = false

// 心跳间隔：防止连接被代理 / NAT 空闲超时断开
const HEARTBEAT_INTERVAL_MS = 20000
let heartbeatTimer: number | null = null
let resizeObserver: ResizeObserver | null = null
let fitRetryTimer: number | null = null
let onPasteEvent: ((ev: ClipboardEvent) => void) | null = null
let toastTimer: number | null = null
let repeatTimer: number | null = null
let pasteHelper: HTMLTextAreaElement | null = null
let touchStartX = 0
let touchStartY = 0
let touchStartTime = 0

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
  term?.focus()
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
function scrollToBottom() {
  if (!term || !props.active) return
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
  rest = rest.replace(/\x1b\]exit\x07/g, () => {
    const c = myCtl()
    if (c) c.exited = true
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
  sock.onclose = () => {
    stopHeartbeat()
    const c = myCtl()
    if (c) c.connected = false
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
  const c = myCtl()
  if (c) c.exited = false
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
  if (!fitAddon || !term || !el.value) return
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
      if (props.active) term.scrollToBottom()
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
  term?.focus()
  if (sock && sock.readyState === WebSocket.OPEN) sock.send(data)
  scrollToBottom()
}

function toggleModifier(mod: 'ctrl' | 'alt' | 'shift') {
  if (mod === 'ctrl') ctrlPressed.value = !ctrlPressed.value
  else if (mod === 'alt') altPressed.value = !altPressed.value
  else if (mod === 'shift') shiftPressed.value = !shiftPressed.value
  term?.focus()
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

// 复制已由桌面鼠标选中自动复制 + 移动端系统文字工具取代，不再提供按钮入口；
// 此处保留供可能的程序化调用，但顶栏不显示复制按钮。

// 复制 / 粘贴 / 清屏
// 剪贴板写入：优先异步 Clipboard API；非安全上下文（http 反代）时用 execCommand 兜底
function legacyCopy(text: string) {
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
}

function copyText(text: string) {
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).catch(() => legacyCopy(text))
  } else {
    legacyCopy(text)
  }
}

// 复制按钮已移除：桌面由选中自动复制取代，移动端由系统文字工具取代。

async function pasteClipboard() {
  if (!sock || sock.readyState !== WebSocket.OPEN) {
    showToast(t('not_connected'))
    return
  }
  try {
    const text = await navigator.clipboard.readText()
    if (text) {
      sock.send(text)
      term?.focus()
      scrollToBottom()
    }
  } catch {
    openSystemTextTool()
  }
}

function clearTerminal() {
  term?.clear()
  scrollToBottom()
  // 清屏同步清空后端临时历史文件：重连/刷新后不再回放已清除的内容
  if (props.tab.id) {
    api.clearSessionHistory(props.tab.id).catch((e) => console.warn('clear session history:', e))
  }
}

function showToast(msg: string) {
  const hint = document.querySelector('.term-copy-toast') as HTMLElement | null
  if (!hint) return
  hint.textContent = msg
  hint.classList.remove('opacity-0', 'pointer-events-none')
  if (toastTimer) clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => {
    hint.classList.add('opacity-0', 'pointer-events-none')
    toastTimer = null
  }, 1400)
}

// 移动端：点击/长按终端文本调起系统文字工具（原生文本菜单）。
// 通过隐藏 textarea 获得系统焦点与原生菜单：有选中内容则载入并全选（可用系统
// 选择/复制工具），并桥接输入（键入/退格/回车/粘贴均转发到会话），菜单调起后仍可正常输入。
function openSystemTextTool() {
  if (pasteHelper) return // 已处于输入/文本菜单状态
  if (!sock || sock.readyState !== WebSocket.OPEN || !term) {
    term?.focus()
    return
  }
  const sel = term.getSelection() || ''
  let lastVal = ''
  const cleanup = () => {
    if (pasteHelper) {
      pasteHelper.removeEventListener('input', onInput)
      pasteHelper.removeEventListener('keydown', onKeydown)
      pasteHelper.removeEventListener('paste', onPaste)
      pasteHelper.removeEventListener('blur', onBlur)
      document.body.removeChild(pasteHelper)
      pasteHelper = null
    }
  }
  const onInput = () => {
    const ta = pasteHelper
    if (!ta) return
    const v = ta.value
    if (v.length > lastVal.length) {
      const added = v.slice(lastVal.length)
      if (sock && sock.readyState === WebSocket.OPEN) sock.send(added)
    } else if (v.length < lastVal.length) {
      // 退格删除
      if (sock && sock.readyState === WebSocket.OPEN) sock.send('\x7f')
    }
    lastVal = v
    scrollToBottom()
  }
  const onKeydown = (e: KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      if (sock && sock.readyState === WebSocket.OPEN) sock.send('\r')
    } else if (e.key === 'Tab') {
      e.preventDefault()
      if (sock && sock.readyState === WebSocket.OPEN) sock.send('\t')
    } else if (e.key === 'Escape') {
      cleanup()
      term?.focus()
    }
  }
  const onPaste = (ev: ClipboardEvent) => {
    const text = ev.clipboardData?.getData('text/plain')
    ev.preventDefault()
    if (text && sock && sock.readyState === WebSocket.OPEN) {
      sock.send(text)
      scrollToBottom()
    }
  }
  const onBlur = () => {
    setTimeout(cleanup, 200)
  }

  const ta = document.createElement('textarea')
  ta.value = sel
  ta.style.position = 'fixed'
  ta.style.left = '0'
  ta.style.top = '0'
  ta.style.width = '2px'
  ta.style.height = '2px'
  ta.style.opacity = '0'
  ta.style.pointerEvents = 'none'
  ta.setAttribute('autocorrect', 'off')
  ta.setAttribute('autocapitalize', 'off')
  ta.setAttribute('spellcheck', 'false')
  document.body.appendChild(ta)
  pasteHelper = ta
  lastVal = ta.value
  ta.addEventListener('input', onInput)
  ta.addEventListener('keydown', onKeydown)
  ta.addEventListener('paste', onPaste)
  ta.addEventListener('blur', onBlur)
  ta.focus()
  if (sel) ta.setSelectionRange(0, sel.length)
  // 兜底：长时间无交互则清理，避免残留
  setTimeout(() => {
    if (pasteHelper) cleanup()
  }, 30000)
}

function onTouchStart(e: TouchEvent) {
  if (e.touches.length !== 1) return
  const touch = e.touches[0]
  touchStartX = touch.clientX
  touchStartY = touch.clientY
  touchStartTime = Date.now()
}

function onTouchMove(e: TouchEvent) {
  // 滑动/滚动期间取消“调起文字工具”
  const touch = e.touches[0]
  if (Math.abs(touch.clientX - touchStartX) > 12 || Math.abs(touch.clientY - touchStartY) > 12) {
    touchStartTime = 0
  }
}

function onTouchEnd() {
  if (touchStartTime === 0) return
  touchStartTime = 0
  // 点击（轻触）或长按终端文本均调起系统文字工具
  openSystemTextTool()
}

// ---------- 初始化 ----------
function initTerminal() {
  if (!el.value || term) return
  term = new Terminal({
    cursorBlink: true,
    fontSize: settings.fontSize,
    fontFamily:
      'ui-monospace, SFMono-Regular, Menlo, Consolas, "Cascadia Mono", "Noto Sans Mono CJK SC", "PingFang SC", "Microsoft YaHei", "WenQuanYi Micro Hei", monospace',
    theme: isDark.value ? DARK_PALETTE : LIGHT_PALETTE,
    scrollback: 2000,
    letterSpacing: 0,
    allowProposedApi: true
  })

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
  try {
    term.loadAddon(new WebglAddon())
  } catch (e) {
    // WebGL 不可用时回退到 xterm 内置 DOM 渲染器
    console.warn('webgl addon:', e)
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
    term.loadAddon(new ClipboardAddon())
  } catch (e) {
    console.warn('clipboard addon:', e)
  }
  try {
    term.loadAddon(new SerializeAddon())
  } catch (e) {
    console.warn('serialize addon:', e)
  }
  try {
    term.loadAddon(new ImageAddon())
  } catch (e) {
    console.warn('image addon:', e)
  }

  // 等待等宽字体加载后再 open + fit，保证首测单元格宽度准确
  const openAndFit = () => {
    try {
      term?.open(el.value as HTMLElement)
      termOpened = true
    } catch {
      /* 已 open 则忽略 */
    }
    nextTick(() => {
      requestAnimationFrame(() => fitAndResize())
    })
  }
  if (typeof document !== 'undefined' && document.fonts && document.fonts.ready) {
    const fallback = window.setTimeout(() => {
      if (!termOpened) openAndFit()
    }, 800)
    document.fonts.ready.then(() => {
      window.clearTimeout(fallback)
      if (!termOpened) openAndFit()
    })
  } else {
    openAndFit()
  }

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
      scrollToBottom()
    }
  }
  el.value.addEventListener('paste', onPasteEvent)

  term.onData((data) => {
    if (!sock || sock.readyState !== WebSocket.OPEN) return
    let toSend = data
    const hadModifier = ctrlPressed.value || altPressed.value || shiftPressed.value
    if (ctrlPressed.value && data.length === 1) {
      const code = data.charCodeAt(0)
      if (code >= 97 && code <= 122) toSend = String.fromCharCode(code - 96)
      else if (code >= 65 && code <= 90) toSend = String.fromCharCode(code - 64)
    } else if (altPressed.value && data.length === 1) {
      toSend = '\x1b' + data
    }
    sock.send(toSend)
    // 修饰键输入一次后自动解除：点击修饰键 → 键入任意按键 → 修饰键复位
    if (hadModifier) {
      ctrlPressed.value = false
      altPressed.value = false
      shiftPressed.value = false
    }
  })

  // 桌面端：鼠标框选（或双击选词）后自动把选中文本复制进剪贴板，并 toast 提示。
  // 连续选择变化时按文本去重 + 1.2s 节流，避免刷屏。
  let lastAutoCopied = ''
  let lastAutoCopyToastAt = 0
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
    focus: () => term?.focus(),
    send: (data: string) => {
      if (sock && sock.readyState === WebSocket.OPEN) {
        sock.send(data)
        scrollToBottom()
        return true
      }
      return false
    },
    connected: false,
    exited: false
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
        term?.focus()
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
  if (fitRetryTimer) clearTimeout(fitRetryTimer)
  if (toastTimer) clearTimeout(toastTimer)
  if (repeatTimer) clearInterval(repeatTimer)
  if (onPasteEvent && el.value) {
    el.value.removeEventListener('paste', onPasteEvent)
    onPasteEvent = null
  }
  if (pasteHelper) {
    document.body.removeChild(pasteHelper)
    pasteHelper = null
  }
  disconnect()
  if (resizeObserver) {
    resizeObserver.disconnect()
    resizeObserver = null
  } else {
    window.removeEventListener('resize', debouncedFit)
  }
  cancelPendingFit()
  term?.dispose()
  term = null
})
</script>

<template>
  <div class="flex flex-col h-full min-h-0 overflow-hidden bg-bg dark:bg-bg-dark">
    <!-- 终端容器（相对定位，承载复制提示气泡、搜索悬浮框、系统文字工具） -->
    <!-- 深色模式下 terminal-area 背景为 #1A1A1A、文字为 #4EC9B0；浅色模式下背景为米黄色#faf5e9、文字为#1a1814
         左侧与下方各加 10px 边框（颜色跟随终端区域颜色），无分隔线 -->
    <div
      ref="el"
      class="term-container flex-1 min-h-0 relative bg-[#faf5e9] dark:bg-[#1A1A1A] dark:text-[#4EC9B0]
             border-l-[10px] border-b-[10px]
             border-[#faf5e9] dark:border-[#1A1A1A]"
      @touchstart="onTouchStart"
      @touchmove="onTouchMove"
      @touchend="onTouchEnd"
      @touchcancel="onTouchEnd"
    >
      <div
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