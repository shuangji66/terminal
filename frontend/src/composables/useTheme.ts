// src/composables/useTheme.ts — 主题（浅色 / 深色 / 跟随系统），localStorage 持久化。
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'

const THEME_KEY = 'terminal-theme'

const themeMode = ref<ThemeMode>(
  (['light', 'dark', 'system'].includes(localStorage.getItem(THEME_KEY) || '')
    ? (localStorage.getItem(THEME_KEY) as ThemeMode)
    : 'system')
)
const systemPrefersDark = ref(
  typeof window !== 'undefined' && window.matchMedia('(prefers-color-scheme: dark)').matches
)

// 终端是否处于深色模式（供 xterm 配色切换）
const isDark = computed(
  () => themeMode.value === 'dark' || (themeMode.value === 'system' && systemPrefersDark.value)
)

function applyTheme(mode: ThemeMode) {
  const dark = mode === 'dark' || (mode === 'system' && systemPrefersDark.value)
  const root = document.documentElement
  if (dark) {
    root.classList.add('dark')
    root.style.colorScheme = 'dark'
  } else {
    root.classList.remove('dark')
    root.style.colorScheme = 'light'
  }
}

function setTheme(mode: ThemeMode) {
  themeMode.value = mode
  localStorage.setItem(THEME_KEY, mode)
  applyTheme(mode)
}

function cycleTheme() {
  const modes: ThemeMode[] = ['light', 'dark', 'system']
  const idx = modes.indexOf(themeMode.value)
  setTheme(modes[(idx + 1) % modes.length])
}

let mediaQuery: MediaQueryList | null = null

export function useTheme() {
  onMounted(() => {
    const saved = localStorage.getItem(THEME_KEY) as ThemeMode | null
    themeMode.value = saved && ['light', 'dark', 'system'].includes(saved) ? saved : 'system'
    applyTheme(themeMode.value)

    // 用**闭包里的** mql 注册与移除：mediaQuery 是模块级变量，每个面板都会覆盖它，
    // 从它上面 remove 是移除"别人"的监听（身份不匹配）= 每挂载一个标签就永久多一个监听。
    const mql = window.matchMedia('(prefers-color-scheme: dark)')
    mediaQuery = mql
    const handler = (e: MediaQueryListEvent) => {
      systemPrefersDark.value = e.matches
      if (themeMode.value === 'system') applyTheme('system')
    }
    mql.addEventListener('change', handler)
    onBeforeUnmount(() => mql.removeEventListener('change', handler))
  })

  return { themeMode, isDark, setTheme, cycleTheme, applyTheme }
}