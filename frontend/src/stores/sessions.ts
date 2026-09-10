import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, type RuntimeInfo, type UserSpec } from '@/serverapi'
import { t } from '@/i18n'

export type TabStatus = 'connecting' | 'open' | 'exited' | 'error'

export interface Tab {
  uid: string // 前端稳定标识（标签状态机的本地主键）
  id: string | null // 后端会话 id；新建会话在 WS 控制帧回填
  title: string // 自定义标题（'' = 使用默认「终端 N」）
  userSpec: UserSpec // 本标签会话以哪个用户运行（nas=登录用户 / root）
  createdAt: number
  status: TabStatus
  restoring: boolean // 是否正在回放历史
}

const TITLES_KEY = 'terminal-tab-titles'

function readTitleMap(): Record<string, string> {
  try {
    return JSON.parse(localStorage.getItem(TITLES_KEY) || '{}')
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

  // 显示标题：自定义标题 > 默认「终端 N」
  function titleFor(tab: Tab): string {
    if (tab.title) return tab.title
    const idx = tabs.value.findIndex((tb) => tb.uid === tab.uid)
    return t('tab_placeholder', { n: idx + 1 })
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
    const tab: Tab = {
      uid: newUid(),
      id: preload?.id ?? null,
      title: preload?.title ?? '',
      userSpec: preload?.userSpec ?? 'nas',
      createdAt: preload?.createdAt ?? Date.now(),
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
    // 注意：不再在这里自动新开标签——由 TabBar 在全部关闭后按用户模式决定
    // （自定义模式下需弹窗选择用户，store 内无法弹窗）。
  }

  function setTabStatus(uid: string, status: TabStatus) {
    const tab = tabs.value.find((tb) => tb.uid === uid)
    if (tab) tab.status = status
  }

  // 前端启动：先从后端恢复活动会话；无会话则新建一个（defaultSpec 决定新会话用户）。
  // custom 模式下启动无会话时固定以登录用户（nas）建立会话，由调用方传入。
  async function restore(defaultSpec: UserSpec = 'nas') {
    restoring.value = true
    try {
      const res = await api.sessions()
      const list = res.sessions || []
      if (list.length === 0) {
        addTab({ userSpec: defaultSpec })
      } else {
        for (const s of list) {
          addTab({ id: s.id, createdAt: new Date(s.createdAt).getTime(), restoring: true, status: 'connecting' })
        }
        // 恢复历史内容（回放）由各 TerminalPane 负责：先取历史写入 xterm，再挂载 WS
        activeUid.value = tabs.value[0]?.uid ?? null
      }
      return list.length
    } catch (e) {
      console.warn('restore sessions error:', e)
      addTab({ userSpec: defaultSpec })
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