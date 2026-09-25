import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, type RuntimeInfo, type UserSpec } from '@/serverapi'
import { t } from '@/i18n'

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
  const tabs = ref<Tab[]>([])
  const activeUid = ref<string | null>(null)
  const restoring = ref(false)
  const info = ref<RuntimeInfo | null>(null)

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

  // 关闭标签：若已有后端会话则终止之（这是唯一终止会话的路径）
  async function closeTab(uid: string) {
    const idx = tabs.value.findIndex((tb) => tb.uid === uid)
    const tab = tabs.value[idx]
    if (!tab) return
    if (tab.id) {
      api.closeSession(tab.id).catch((e) => console.warn('close session error:', e))
    }
    tabs.value.splice(idx, 1)
    if (activeUid.value === uid) {
      const next = tabs.value[Math.min(idx, tabs.value.length - 1)]
      activeUid.value = next ? next.uid : null
    }
    // 注意：不再在这里自动新开标签——由 TabBar 在全部关闭后重新新建
    // （需弹窗选择用户，store 内无法弹窗）。
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

  // 前端启动：先从后端恢复活动会话；无会话则新建一个——固定以当前登录用户（nas）建立。
  // （新建标签页另走 TabBar 的用户选择弹窗，与本函数无关。）
  async function restore() {
    restoring.value = true
    try {
      const res = await api.sessions()
      const list = res.sessions || []
      if (list.length === 0) {
        addTab({ userSpec: 'nas' })
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
      addTab({ userSpec: 'nas' })
      return 0
    } finally {
      restoring.value = false
    }
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
    titleFor,
    labelForSpec,
    loadInfo,
    addTab,
    setActive,
    adoptSessionId,
    persistTitle,
    closeTab,
    setTabStatus,
    restore
  }
})