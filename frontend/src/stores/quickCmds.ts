import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type QuickCmd } from '@/serverapi'

// 快捷指令：持久化到后端文件（TERMINAL_QUICK_CMDS_FILE），前端整体保存。
export const useQuickCmdsStore = defineStore('quickCmds', () => {
  const commands = ref<QuickCmd[]>([])
  const loading = ref(false)

  async function load() {
    loading.value = true
    try {
      const res = await api.listQuickCmds()
      commands.value = res.commands || []
    } finally {
      loading.value = false
    }
  }

  // 整体写回持久化文件；失败时回滚本地列表（深拷贝快照）
  async function persist() {
    const snapshot = commands.value.map((c) => ({ ...c }))
    try {
      const res = await api.saveQuickCmds(commands.value)
      commands.value = res.commands || commands.value
    } catch (e) {
      commands.value = snapshot
      throw e
    }
  }

  function add(payload: { name: string; content: string; auto: boolean }) {
    commands.value.push({
      id: crypto.randomUUID(),
      name: payload.name,
      content: payload.content,
      auto: payload.auto
    })
    return persist()
  }

  function update(cmd: QuickCmd, payload: { name: string; content: string; auto: boolean }) {
    cmd.name = payload.name
    cmd.content = payload.content
    cmd.auto = payload.auto
    return persist()
  }

  function remove(id: string) {
    commands.value = commands.value.filter((c) => c.id !== id)
    return persist()
  }

  // 根据 id 列表重新排序命令
  function reorder(orderedIds: string[]) {
    const ordered = orderedIds.map((id) => commands.value.find((c) => c.id === id)).filter((c): c is QuickCmd => !!c)
    commands.value = ordered
    return persist()
  }

  return { commands, loading, load, persist, add, update, remove, reorder }
})