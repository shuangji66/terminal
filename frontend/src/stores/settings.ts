import { defineStore } from 'pinia'
import { ref } from 'vue'

// 终端字号偏好：仅保存在浏览器 localStorage（不持久化到后端）
const FONT_KEY = 'terminal-font-size'
const FONT_MIN = 10
const FONT_MAX = 26
const FONT_DEFAULT = 16

function clampFont(n: number): number {
  if (!Number.isFinite(n)) return FONT_DEFAULT
  return Math.min(FONT_MAX, Math.max(FONT_MIN, Math.round(n)))
}

export const useSettingsStore = defineStore('settings', () => {
  // 终端字号：浏览器存储（无保存值时用默认 16，已自定义过的保持用户选择）
  const fontSize = ref(clampFont(Number(localStorage.getItem(FONT_KEY)) || FONT_DEFAULT))

  function setFontSize(n: number) {
    fontSize.value = clampFont(n)
    localStorage.setItem(FONT_KEY, String(fontSize.value))
  }

  return {
    fontSize,
    setFontSize
  }
})
