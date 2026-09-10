import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type NasUserInfo, type UserMode } from '@/serverapi'

// 终端字号偏好：仅保存在浏览器 localStorage（不持久化到后端）
const FONT_KEY = 'terminal-font-size'
const FONT_MIN = 10
const FONT_MAX = 26
const FONT_DEFAULT = 14

function clampFont(n: number): number {
  if (!Number.isFinite(n)) return FONT_DEFAULT
  return Math.min(FONT_MAX, Math.max(FONT_MIN, Math.round(n)))
}

export const useSettingsStore = defineStore('settings', () => {
  // 启动用户模式：nas（默认，X-Trim-Userid 传递的 NAS 用户） | root——持久化到后端文件
  const userMode = ref<UserMode>('nas')
  const userModePath = ref('')
  const defaultMode = ref('nas')
  const nasUser = ref<NasUserInfo | null>(null)

  // 终端字号：浏览器存储
  const fontSize = ref(clampFont(Number(localStorage.getItem(FONT_KEY)) || FONT_DEFAULT))

  function setFontSize(n: number) {
    fontSize.value = clampFont(n)
    localStorage.setItem(FONT_KEY, String(fontSize.value))
  }

  async function loadUserMode() {
    try {
      const res = await api.userMode()
      userMode.value = res.mode
      userModePath.value = res.path
      defaultMode.value = res.defaultMode
      nasUser.value = res.nasUser ?? null
    } catch (e) {
      console.warn('load user mode error:', e)
    }
  }

  async function saveUserMode(mode: UserMode): Promise<UserMode> {
    const res = await api.saveUserMode(mode)
    userMode.value = res.mode
    userModePath.value = res.path
    return res.mode
  }

  return {
    userMode,
    userModePath,
    defaultMode,
    nasUser,
    fontSize,
    setFontSize,
    loadUserMode,
    saveUserMode
  }
})