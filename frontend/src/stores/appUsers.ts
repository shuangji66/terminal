import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type AppUserInfo } from '@/serverapi'

// 应用用户列表缓存（「新建终端」选人弹窗使用）
//  - 冷启动时由 App 预取一次，之后每次新建会话的选人弹窗直接读缓存，
//    不再逐次请求后端（后端 appcenter-cli 列表虽有短缓存，但每次仍要走一次 HTTP + 遍历）。
//  - 缓存只在内存里、不持久化：每次前端冷启动都是空缓存，启动预取即「刷新一遍」；
//    生命周期内如需最新列表，由弹窗的刷新按钮触发 load()。
//  - 加载失败不置 loaded，弹窗下次打开会重试；失败信息留在 loadError 供弹窗展示。
//  - 置顶列表（pinned）是**浏览器本地偏好**：只写 localStorage，不落后端、不影响后端排序，
//    用于把常用应用排到选人弹窗列表上方（详见文件末尾）。
export const useAppUsersStore = defineStore('appUsers', () => {
  const apps = ref<AppUserInfo[]>([])
  // 应用家目录模板（后端 TERMINAL_APP_HOME_TEMPLATE），用于提示该会话的 HOME
  const homeTemplate = ref('')
  const loading = ref(false)
  const loadError = ref('')
  const loaded = ref(false) // 是否已有可用缓存（失败不算，避免用空列表「假装」加载成功）

  // 并发去重：冷启动预取与弹窗首次打开可能同时触发
  let inflight: Promise<void> | null = null

  // 强制重新拉取（冷启动预取 / 手动刷新）
  function load(): Promise<void> {
    if (inflight) return inflight
    loading.value = true
    loadError.value = ''
    const p = (async () => {
      try {
        const res = await api.appUsers()
        apps.value = res.apps || []
        homeTemplate.value = res.homeTemplate || ''
        loaded.value = true
      } catch (e) {
        loadError.value = e instanceof Error ? e.message : String(e)
        console.warn('load app users error:', e)
      } finally {
        loading.value = false
        inflight = null
      }
    })()
    inflight = p
    return p
  }

  // 有缓存直接复用（弹窗每次打开走这条），没有才请求一次
  function ensureLoaded(): Promise<void> {
    if (loaded.value) return Promise.resolve()
    return load()
  }

  // ---------- 应用置顶（仅浏览器本地，不持久化到后端） ----------
  // 用数组而非 Set：数组顺序即「先置顶的排更上面」的顺序。
  // 不随应用列表加载做清理：/api/apps 失败时会回空列表，清理会把用户置顶全部抹掉；
  // 残留的失效名字只是死数据，不会影响排序（排序时按名字匹配不到就忽略）。
  const PIN_KEY = 'terminal-pinned-apps'

  function readPinned(): string[] {
    try {
      const raw: unknown = JSON.parse(localStorage.getItem(PIN_KEY) || '[]')
      if (!Array.isArray(raw)) return []
      const names = raw.filter((v): v is string => typeof v === 'string' && v !== '')
      return [...new Set(names)] // 去重（保留首次出现的顺序）
    } catch {
      return []
    }
  }

  const pinned = ref<string[]>(readPinned())

  function isPinned(name: string): boolean {
    return pinned.value.includes(name)
  }

  function togglePin(name: string) {
    pinned.value = isPinned(name)
      ? pinned.value.filter((n) => n !== name) // 取消置顶 → 回到列表原位（原位 = 后端顺序）
      : [...pinned.value, name] // 追加到末尾 → 先置顶的仍在更上面
    localStorage.setItem(PIN_KEY, JSON.stringify(pinned.value))
  }

  // 排序：置顶的按置顶先后排最前，其余保持传入顺序（后端已按应用名排序）。
  // 返回新数组，不修改 store 里的 apps。
  function orderByPin(list: AppUserInfo[]): AppUserInfo[] {
    const rank = new Map(pinned.value.map((name, i) => [name, i]))
    return [...list].sort((a, b) => {
      const ra = rank.get(a.name)
      const rb = rank.get(b.name)
      if (ra === rb) return 0 // 都未置顶（含都置顶但同名去重后不可能）→ 保持原顺序
      if (ra === undefined) return 1
      if (rb === undefined) return -1
      return ra - rb
    })
  }

  return {
    apps,
    homeTemplate,
    loading,
    loadError,
    loaded,
    load,
    ensureLoaded,
    pinned,
    isPinned,
    togglePin,
    orderByPin
  }
})
