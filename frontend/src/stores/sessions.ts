import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, type RuntimeInfo, type UserSpec } from '@/serverapi'
import { t } from '@/i18n'
import { useToastStore } from '@/stores/toast'

// 'detached'：该会话已被其他设备接管（单挂载点语义）——只解挂载，会话仍在服务端运行；
// 本端不自动重连，用户点「重连」即可显式夺回。
export type TabStatus = 'connecting' | 'open' | 'exited' | 'error' | 'detached'

export interface Tab {
  uid: string // 前端稳定标识（标签状态机的本地主键）
  id: string | null // 后端会话 id；新建会话在 WS 控制帧回填
  title: string // 自定义标题（'' = 使用默认「终端 N:<用户>」）
  userSpec: UserSpec // 本标签会话以哪个用户运行（nas=登录用户 / root / app:<APP NAME>）
  userLabel: string // 该用户的显示名（登录用户名 / root / 应用 APP NAME），用于默认标签名
  status: TabStatus
  restoring: boolean // 是否正在回放历史
}

const TITLES_KEY = 'terminal-tab-titles'

function readTitleMap(): Record<string, string> {
  // localStorage 是用户可改的：形状不对（null / 字符串 / 数组 / 值非字符串）时不能直接当
  // map 用——`map[id] = x` 会抛 TypeError 并打断整个恢复流程。
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(TITLES_KEY) || '{}')
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {}
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
      if (typeof v === 'string') out[k] = v
    }
    return out
  } catch {
    return {}
  }
}

let uidSeq = 0
function newUid(): string {
  uidSeq += 1
  return `tab-${Date.now().toString(36)}-${uidSeq}`
}

export const useSessionsStore = defineStore('sessions', () => {
  const toast = useToastStore()
  const tabs = ref<Tab[]>([])
  const activeUid = ref<string | null>(null)
  const restoring = ref(false)
  const info = ref<RuntimeInfo | null>(null)
  // 本页记下的后端进程实例 id（/api/info 的 bootId）：它一变就说明后端重启过，
  // 本页所有标签的会话都不复存在（见 probeBackend / resetForRestart）。
  const bootId = ref<string | null>(null)
  // 「需要用户重新选择会话用户」：冷启动无会话可恢复、或后端重启清空了标签时置位，
  // 由 TabBar 消费（弹出选人窗，与「关掉最后一个标签」同一交互）。
  // 前端**绝不**自行决定以哪个用户进入 bash。
  const needsUserPick = ref(false)

  function requestUserPick() {
    needsUserPick.value = true
  }
  function clearUserPick() {
    needsUserPick.value = false
  }

  const activeIndex = computed(() => tabs.value.findIndex((tb) => tb.uid === activeUid.value))
  const active = computed(
    () => tabs.value.find((tb) => tb.uid === activeUid.value) ?? null
  )

  // userSpec → 显示名：登录用户取后端解析出的 NAS 用户名，ROOT 固定 root，应用用户取 APP NAME
  function labelForSpec(spec: UserSpec): string {
    if (spec === 'root') return 'root'
    if (spec.startsWith('app:')) return spec.slice('app:'.length)
    return info.value?.nasUser?.username || 'nas'
  }

  // 显示标题：自定义标题 > 默认「终端 N:<用户>」（用户未知时退回「终端 N」）
  function titleFor(tab: Tab): string {
    if (tab.title) return tab.title
    const idx = tabs.value.findIndex((tb) => tb.uid === tab.uid)
    const n = idx + 1
    return tab.userLabel
      ? t('tab_placeholder_user', { n, user: tab.userLabel })
      : t('tab_placeholder', { n })
  }

  async function loadInfo() {
    try {
      const res = await api.info()
      info.value = res.runtime
      bootId.value = res.runtime.bootId || null
    } catch (e) {
      console.warn('load info error:', e)
    }
  }

  function addTab(preload?: Partial<Tab>): Tab {
    const userSpec = preload?.userSpec ?? 'nas'
    const tab: Tab = {
      uid: newUid(),
      id: preload?.id ?? null,
      title: preload?.title ?? '',
      userSpec,
      userLabel: preload?.userLabel ?? labelForSpec(userSpec),
      status: preload?.status ?? 'connecting',
      restoring: preload?.restoring ?? false
    }
    tabs.value.push(tab)
    if (preload?.id) {
      const map = readTitleMap()
      if (map[preload.id]) tab.title = map[preload.id]
    }
    activeUid.value = tab.uid
    return tab
  }

  function setActive(uid: string) {
    if (tabs.value.some((tb) => tb.uid === uid)) activeUid.value = uid
  }

  // 新建会话：后端通过 \x1b]id;<id>\x07 控制帧回填会话 id
  function adoptSessionId(uid: string, id: string) {
    const tab = tabs.value.find((tb) => tb.uid === uid)
    if (!tab) return
    const map = readTitleMap()
    if (tab.title && !map[id]) {
      map[id] = tab.title
      localStorage.setItem(TITLES_KEY, JSON.stringify(map))
    }
    tab.id = id
  }

  function persistTitle(uid: string, title: string) {
    const tab = tabs.value.find((tb) => tb.uid === uid)
    if (!tab) return
    tab.title = title
    if (tab.id) {
      const map = readTitleMap()
      if (title) map[tab.id] = title
      else delete map[tab.id]
      localStorage.setItem(TITLES_KEY, JSON.stringify(map))
    }
  }

  // 从标签列表移除。closeRemote=true 时会终止后端会话（关闭标签是**唯一**终止会话的
  // 路径）；false 用于「后端已经没有这个会话了」——此时发 DELETE 只会拿到 404。
  // 注意：不在这里自动新开标签——由 TabBar 在全部关闭后重新新建
  // （需弹窗选择用户，store 内无法弹窗）。
  function removeTab(uid: string, closeRemote: boolean): Tab | null {
    const idx = tabs.value.findIndex((tb) => tb.uid === uid)
    const tab = tabs.value[idx]
    if (!tab) return null
    if (tab.id) {
      // 标签没了，它记在 localStorage 里的自定义标题也该清掉，否则映射只增不减
      const map = readTitleMap()
      if (map[tab.id]) {
        delete map[tab.id]
        localStorage.setItem(TITLES_KEY, JSON.stringify(map))
      }
    }
    if (closeRemote && tab.id) {
      api.closeSession(tab.id).catch((e) => console.warn('close session error:', e))
    }
    tabs.value.splice(idx, 1)
    if (activeUid.value === uid) {
      const next = tabs.value[Math.min(idx, tabs.value.length - 1)]
      activeUid.value = next ? next.uid : null
    }
    return tab
  }

  // 关闭标签：若已有后端会话则终止之（这是唯一终止会话的路径）
  async function closeTab(uid: string) {
    removeTab(uid, true)
  }

  function setTabStatus(uid: string, status: TabStatus) {
    const tab = tabs.value.find((tb) => tb.uid === uid)
    if (tab) tab.status = status
  }

  // 后端上报的原始 user= 参数 → 前端 UserSpec。**恢复的标签必须沿用原 spec**：
  // 会话被后端重启带走后 TerminalPane 会自动重建，若这里退化成 'nas'，root/应用用户的标签
  // 会悄悄变成登录用户 shell（而标签名还显示原用户）。老后端没有该字段时才回退 nas。
  function specForUser(raw: string | undefined): UserSpec {
    if (raw === 'root') return 'root'
    if (raw && raw.startsWith('app:')) return raw as UserSpec
    return 'nas'
  }

  // 前端启动：恢复后端仍在运行的活动会话（历史回放由各 TerminalPane 负责）。
  // **只恢复真正还能交互的会话**：shell 已经退出的会话（exited）PTY 已死，恢复成标签只会
  // 得到「看着已连接、打字没反应」的僵尸标签——后端只在进程退出那一刻给当时挂载的连接发
  // \x1b]exit\x07，重新挂载时不会再发，状态点还会变绿。这类会话由后端在宽限期后回收。
  // **无会话可恢复时不自动建会话**：前端不替用户决定以谁进入 bash，改为置位
  // needsUserPick，由 TabBar 弹选人窗（与「关掉最后一个标签」同一交互）。
  async function restore() {
    restoring.value = true
    try {
      const res = await api.sessions()
      const list = (res.sessions || []).filter((s) => !s.exited)
      if (list.length === 0) {
        requestUserPick()
      } else {
        for (const s of list) {
          // 恢复的会话以标签级 userLabel 记录其后端上报的运行用户（老后端无 user 字段时
          // 退回默认登录用户显示名），仅用于标签展示；挂载本身仍按 id 进行。
          addTab({
            id: s.id,
            userSpec: specForUser(s.userSpec),
            restoring: true,
            status: 'connecting',
            userLabel: s.user || labelForSpec(specForUser(s.userSpec))
          })
        }
        // 恢复历史内容（回放）由各 TerminalPane 负责：先取历史写入 xterm，再挂载 WS
        activeUid.value = tabs.value[0]?.uid ?? null
      }
      return list.length
    } catch (e) {
      console.warn('restore sessions error:', e)
      // 恢复失败（后端未就绪等）：同样不猜用户，交给用户选
      requestUserPick()
      return 0
    } finally {
      restoring.value = false
    }
  }

  // ---------- 后端重启探测（页面未关闭时的判定） ----------
  // 后端进程重启后，本页所有标签的会话都不复存在，而 WebSocket 只是「断开」。
  // 页面若在原地傻等，用户回来看到的是一排永远没有输出的死标签。判据是 /api/info 的
  // bootId：值变了 = 后端换了进程。新进程可能还没起来（请求失败），故探测有界重试；
  // 窗口内没探出来（后端长时间不回来）则由「刷新页面 / 点重连」时的判定兜底。
  const PROBE_DELAYS_MS = [0, 1000, 3000, 8000, 15000, 30000]

  type BackendProbe = 'restarted' | 'same' | 'unreachable'
  let probeInflight: Promise<BackendProbe> | null = null

  // 问一次后端「你是不是换了进程」。并发去重：多个标签同时断开/同时报「会话不存在」
  // 时只发一次请求。
  function probeBackend(): Promise<BackendProbe> {
    if (probeInflight) return probeInflight
    const p = (async (): Promise<BackendProbe> => {
      try {
        const res = await api.info()
        const cur = res.runtime.bootId || ''
        info.value = res.runtime
        // 老后端没有 bootId / 本页没记下基线：无从判定，按「没重启」处理
        if (!cur) return 'same'
        const prev = bootId.value
        if (!prev) {
          bootId.value = cur
          return 'same'
        }
        if (cur === prev) return 'same'
        bootId.value = cur
        return 'restarted'
      } catch (e) {
        console.warn('probe backend info error:', e)
        return 'unreachable'
      } finally {
        probeInflight = null
      }
    })()
    probeInflight = p
    return p
  }

  let probeLoopRunning = false

  // WS 掉线后主动探测后端是否已重启（TerminalPane 在已挂载过会话的连接断开时调用）。
  // 探测循环全局只有一个；已清空标签就没必要再探。
  function watchBackendRestart() {
    if (probeLoopRunning) return
    probeLoopRunning = true
    void (async () => {
      try {
        for (const delay of PROBE_DELAYS_MS) {
          if (delay > 0) await new Promise((r) => window.setTimeout(r, delay))
          if (tabs.value.length === 0) return // 标签已空（用户关了/已被清空），不必再探
          const r = await probeBackend()
          if (r === 'restarted') {
            resetForRestart()
            return
          }
          // 后端活着且没换进程：不是重启（网络/代理抖动），无需继续
          if (r === 'same') return
        }
      } finally {
        probeLoopRunning = false
      }
    })()
  }

  // 后端已重启：旧会话在新进程里全都不存在（退出时会话临时目录已被整目录删除），
  // 本地标签全部作废。这里**不发** DELETE /api/session——新进程里没有这些会话。
  function resetForRestart() {
    if (tabs.value.length === 0) return
    tabs.value = []
    activeUid.value = null
    needsUserPick.value = true
    toast.show(t('conn_backend_restarted'), 'error')
  }

  // TerminalPane 上报：本标签的后端会话不存在（挂载时后端回 "session not found"）。
  // 先判定是不是后端重启：是 → 清空全部标签、弹窗重新选择用户；
  // 不是（如会话被其他设备关掉）→ 只清掉这个标签，其他标签不动。
  // 两条路都**不自动重建会话**：前端不替用户决定以谁进入 bash。
  async function handleSessionGone(uid: string) {
    const r = await probeBackend()
    // 等待期间可能已被并发处理（例如另一个标签先探到重启、已清空全部标签）
    if (!tabs.value.some((tb) => tb.uid === uid)) return
    if (r === 'restarted') {
      resetForRestart()
      return
    }
    // 'unreachable' 也走这里：既然后端刚回过 "session not found"，这个会话确实没了
    removeTab(uid, false)
    toast.show(t('conn_session_gone'), 'error')
    if (tabs.value.length === 0) needsUserPick.value = true
  }

  // 快捷指令执行写入口：把内容发给当前激活标签的终端
  // （真正的发送由 paneControls 注册的 send 完成）

  return {
    tabs,
    activeUid,
    active,
    activeIndex,
    restoring,
    info,
    needsUserPick,
    requestUserPick,
    clearUserPick,
    titleFor,
    labelForSpec,
    loadInfo,
    addTab,
    setActive,
    adoptSessionId,
    persistTitle,
    closeTab,
    setTabStatus,
    restore,
    watchBackendRestart,
    handleSessionGone
  }
})