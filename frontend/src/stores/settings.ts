import { defineStore } from 'pinia'
import { ref } from 'vue'

// 终端偏好（字号 / 字体）：仅保存在浏览器 localStorage（不持久化到后端）
const FONT_KEY = 'terminal-font-size'
// 终端字体：内置 Maple Mono（默认）或系统等宽字体栈
export const FONT_FAMILY_KEY = 'terminal-font-family'
export type TerminalFont = 'maple' | 'system'
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

  // 终端字体：默认内置 Maple Mono；只有显式存过 'system' 才用系统字体
  const terminalFont = ref<TerminalFont>(
    localStorage.getItem(FONT_FAMILY_KEY) === 'system' ? 'system' : 'maple'
  )

  function setTerminalFont(v: TerminalFont) {
    terminalFont.value = v === 'system' ? 'system' : 'maple'
    localStorage.setItem(FONT_FAMILY_KEY, terminalFont.value)
  }

  return {
    fontSize,
    terminalFont,
    setFontSize,
    setTerminalFont
  }
})
