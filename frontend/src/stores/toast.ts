import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface ToastItem {
  id: number
  msg: string
  type: 'success' | 'error' | 'info'
}

let seq = 0

export const useToastStore = defineStore('toast', () => {
  const toasts = ref<ToastItem[]>([])

  function show(msg: string, type: ToastItem['type'] = 'info') {
    const id = ++seq
    toasts.value.push({ id, msg, type })
    window.setTimeout(() => dismiss(id), 2600)
  }

  function dismiss(id: number) {
    toasts.value = toasts.value.filter((tb) => tb.id !== id)
  }

  return { toasts, show, dismiss }
})