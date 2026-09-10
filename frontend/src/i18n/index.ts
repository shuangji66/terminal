// 语言偏好只保存在本浏览器（localStorage），不随后端持久化。
import { ref, type Ref } from 'vue'
import { zh } from './zh'
import { en } from './en'

export type Locale = 'zh' | 'en'

const LOCALE_KEY = 'terminal-language'

export function isLocale(v: string): v is Locale {
  return v === 'zh' || v === 'en'
}

function readStoredLocale(): Locale {
  const v = localStorage.getItem(LOCALE_KEY)
  return isLocale(v || '') ? (v as Locale) : 'zh'
}

// 当前语言（模块级共享，reactive）
const locale = ref<Locale>(readStoredLocale()) as Ref<Locale>

const dict: Record<Locale, Record<string, string>> = { zh, en }

export function setLocale(l: Locale) {
  locale.value = l
  localStorage.setItem(LOCALE_KEY, l)
}

export function t(key: string, params?: Record<string, string | number>): string {
  let s = dict[locale.value][key]
  if (s === undefined) s = dict.zh[key]
  if (s === undefined) s = key
  if (params) {
    for (const k of Object.keys(params)) {
      s = s.replace(new RegExp(`\\{${k}\\}`, 'g'), String(params[k]))
    }
  }
  return s
}

export function useI18n() {
  return { locale, t, setLocale }
}