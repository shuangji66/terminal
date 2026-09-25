import { defineStore } from 'pinia'
import { reactive } from 'vue'

// 各标签终端暴露给顶栏按钮的命令入口（复制/粘贴/清屏/重连/发送）。
// 按标签 uid 登记到注册表；App 通过「激活标签的 uid」解析对应控制，
// 避免后台/后挂载标签覆盖掉激活标签的控制（旧实现导致按钮全部失效）。
export interface PaneControls {
  paste: () => void
  clear: () => void
  reconnect: () => void
  search: () => void
  focus: () => void
  send: (data: string) => boolean
  connected: boolean
}

export const usePaneControlsStore = defineStore('paneControls', () => {
  const registry = reactive<Record<string, PaneControls>>({})

  function register(uid: string, c: PaneControls) {
    registry[uid] = c
  }

  function unregister(uid: string) {
    delete registry[uid]
  }

  function get(uid: string | null | undefined): PaneControls | undefined {
    return uid ? registry[uid] : undefined
  }

  return { registry, register, unregister, get }
})