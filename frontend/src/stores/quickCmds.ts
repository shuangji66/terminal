import { defineStore } from 'pinia'
import { ref } from 'vue'
import { t } from '@/i18n'
import { api, type QuickCmd } from '@/serverapi'
import { useToastStore } from '@/stores/toast'

// crypto.randomUUID 是 SecureContext-only API：http 反代部署（isSecureContext=false）
// 下它是 undefined，直接调用会抛 TypeError，表现为「新增/编辑快捷指令失败」。
// 兜底用 crypto.getRandomValues（非安全上下文同样可用）自行拼出 v4 UUID。
function newCmdId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  const b = new Uint8Array(16)
  crypto.getRandomValues(b)
  b[6] = (b[6] & 0x0f) | 0x40 // version 4
  b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

// 快捷指令：持久化到后端文件（TERMINAL_QUICK_CMDS_FILE），前端整体保存。
export const useQuickCmdsStore = defineStore('quickCmds', () => {
  const commands = ref<QuickCmd[]>([])
  const loading = ref(false)
  // 加载失败标志：对话框据此显示「加载失败」而不是「暂无快捷指令」——否则用户会以为数据丢了
  const loadError = ref(false)

  async function load() {
    loading.value = true
    loadError.value = false
    try {
      const res = await api.listQuickCmds()
      commands.value = res.commands || []
    } catch (e) {
      loadError.value = true
      useToastStore().show(t('qc_load_failed'), 'error')
      console.warn('quick cmds load error:', e)
    } finally {
      loading.value = false
    }
  }

  // 改动前的深拷贝快照：**必须在改动之前**拍。曾经在 persist() 内部拍快照，而 add/update/
  // remove/reorder 都是先改 store 再调 persist —— 失败时"回滚"回去的正是保存失败的那一份，
  // UI（以及已关闭的编辑弹窗）看起来是保存成功的。
  const snapshot = () => commands.value.map((c) => ({ ...c }))

  // 整体写回持久化文件；失败时回滚到 before（调用方在改动前拍好的快照）
  async function persist(before: QuickCmd[]) {
    try {
      const res = await api.saveQuickCmds(commands.value)
      commands.value = res.commands || commands.value
    } catch (e) {
      commands.value = before
      throw e
    }
  }

  function add(payload: { name: string; content: string; auto: boolean }) {
    const before = snapshot()
    commands.value.push({
      id: newCmdId(),
      name: payload.name,
      content: payload.content,
      auto: payload.auto
    })
    return persist(before)
  }

  function update(cmd: QuickCmd, payload: { name: string; content: string; auto: boolean }) {
    const before = snapshot()
    cmd.name = payload.name
    cmd.content = payload.content
    cmd.auto = payload.auto
    return persist(before)
  }

  function remove(id: string) {
    const before = snapshot()
    commands.value = commands.value.filter((c) => c.id !== id)
    return persist(before)
  }

  // 根据 id 列表重新排序命令
  function reorder(orderedIds: string[]) {
    const before = snapshot()
    const ordered = orderedIds.map((id) => commands.value.find((c) => c.id === id)).filter((c): c is QuickCmd => !!c)
    commands.value = ordered
    return persist(before)
  }

  return { commands, loading, loadError, load, persist, add, update, remove, reorder }
})