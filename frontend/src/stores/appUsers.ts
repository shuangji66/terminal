import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type AppUserInfo } from '@/serverapi'

// 应用用户列表缓存（「新建终端」选人弹窗使用）
//  - 冷启动时由 App 预取一次，之后每次新建会话的选人弹窗直接读缓存，
//    不再逐次请求后端（后端 appcenter-cli 列表虽有短缓存，但每次仍要走一次 HTTP + 遍历）。
//  - 缓存只在内存里、不持久化：每次前端冷启动都是空缓存，启动预取即「刷新一遍」；
//    生命周期内如需最新列表，由弹窗的刷新按钮触发 load()。
//  - 加载失败不置 loaded，弹窗下次打开会重试；失败信息留在 loadError 供弹窗展示。
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
  //（例如启动时为其他模式、之后在设置里切到 custom）
  function ensureLoaded(): Promise<void> {
    if (loaded.value) return Promise.resolve()
    return load()
  }

  return { apps, homeTemplate, loading, loadError, loaded, load, ensureLoaded }
})
