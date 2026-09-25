<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import TabBar from '@/components/TabBar.vue'
import TerminalPane from '@/components/TerminalPane.vue'
import Toast from '@/components/Toast.vue'
import QuickCmdsListDialog from '@/components/QuickCmdsListDialog.vue'
import QuickCmdEditDialog from '@/components/QuickCmdEditDialog.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import SettingsDialog from '@/components/SettingsDialog.vue'
import { useTheme } from '@/composables/useTheme'
import { useViewportHeight } from '@/composables/useViewportHeight'
import { t } from '@/i18n'
import { useSessionsStore } from '@/stores/sessions'
import { useQuickCmdsStore } from '@/stores/quickCmds'
import { useToastStore } from '@/stores/toast'
import { usePaneControlsStore } from '@/stores/paneControls'
import { useAppUsersStore } from '@/stores/appUsers'
import type { QuickCmd } from '@/serverapi'

useTheme() // 初始化 / 跟随系统主题
// 移动端虚拟键盘适配：把可视视口几何写入 CSS 变量，壳层与底部键条据此让位
// （Safari 不支持 interactive-widget=resizes-content，只能自行跟随可视视口）。
useViewportHeight()
const store = useSessionsStore()
const qc = useQuickCmdsStore()
const toast = useToastStore()
const pc = usePaneControlsStore()
const appUsers = useAppUsersStore()

// 激活标签对应的终端控制（复制/粘贴/清屏/重连/发送/连接状态）
const activeControls = computed(() => pc.get(store.activeUid))

const settingsVisible = ref(false)

onMounted(async () => {
  document.title = t('app_name')
  // 先加载运行信息，再恢复会话：无会话可恢复时固定以当前登录用户（NAS 用户）建立会话。
  await store.loadInfo()
  // 冷启动预取一次应用用户列表，之后「新建终端」的选择弹窗直接读缓存。
  // 缓存只存在内存里（不落 localStorage），因此每次冷启动都是空缓存 → 天然重新拉取一遍。
  // 这里不 await：预取只为加速弹窗，不应拖慢会话恢复。
  void appUsers.load()
  const n = await store.restore()
  if (n > 0) {
    toast.show(t('restore_done', { n }), 'success')
  }
})

// ---------- 快捷指令弹窗状态 ----------
const qcVisible = ref(false)
const editVisible = ref(false)
const editingCmd = ref<QuickCmd | null>(null)
// 删除用独立显隐开关 + 待删目标：弹窗的 update:visible 只同步显隐，
// 不提前清空 deleteTarget，否则 confirm 触发时目标已被置空导致删除失效
// （与 TabBar 关闭标签的 closeTargetUid 同一坑）。
const deleteTarget = ref<QuickCmd | null>(null)
const deleteDialogVisible = ref(false)

// 点击命令卡片：把内容发送到当前激活标签的终端，并把光标聚焦回终端
function runQuickCmd(cmd: QuickCmd) {
  const ctl = activeControls.value
  if (!ctl?.connected) {
    toast.show(t('qc_not_connected'), 'error')
    return
  }
  const payload = cmd.content.replace(/\r?\n/g, '\r') + (cmd.auto ? '\r' : '')
  ctl.send?.(payload)
  ctl.focus?.()
  qcVisible.value = false
}

function onAddCmd() {
  editingCmd.value = null
  editVisible.value = true
}
function onEditCmd(cmd: QuickCmd) {
  editingCmd.value = cmd
  editVisible.value = true
}
function onDeleteCmd(cmd: QuickCmd) {
  deleteTarget.value = cmd
  deleteDialogVisible.value = true
}

async function saveQuickCmd(payload: { name: string; content: string; auto: boolean }) {
  try {
    if (editingCmd.value) await qc.update(editingCmd.value, payload)
    else await qc.add(payload)
    toast.show(t('qc_saved'), 'success')
  } catch {
    toast.show(t('qc_save_failed'), 'error')
  }
  editingCmd.value = null
}

async function confirmDeleteQuickCmd() {
  deleteDialogVisible.value = false
  const target = deleteTarget.value
  deleteTarget.value = null
  if (!target) return
  try {
    await qc.remove(target.id)
    // 删除后 store.commands 已更新，v-for 以 id 为 key 会随之重渲染，
    // 每项的 idx（上下移动可用性）自动刷新，无需额外处理。
    toast.show(t('qc_deleted'), 'success')
  } catch {
    toast.show(t('qc_save_failed'), 'error')
  }
}

function cancelDeleteQuickCmd() {
  deleteDialogVisible.value = false
  deleteTarget.value = null
}

</script>

<template>
  <div class="app-shell flex flex-col bg-bg dark:bg-bg-dark text-ink dark:text-ink-dark overflow-hidden">
    <TabBar @quick-cmds="qcVisible = true" @settings="settingsVisible = true" />

    <!-- 终端面板区：每个标签一个面板，非激活用 visibility 隐藏（保持尺寸与 WS 存活） -->
    <main class="flex-1 min-h-0 relative">
      <TerminalPane
        v-for="tab in store.tabs"
        :key="tab.uid"
        :tab="tab"
        :active="store.activeUid === tab.uid"
        class="absolute inset-0"
        :class="store.activeUid === tab.uid ? '' : 'invisible'"
      />

      <!-- 启动恢复中提示 -->
      <div
        v-if="store.restoring"
        class="absolute inset-0 z-20 flex items-center justify-center bg-bg/70 dark:bg-bg-dark/70 backdrop-blur-sm text-sm text-ink-soft dark:text-ink-soft-dark"
      >
        {{ t('restoring') }}
      </div>
    </main>

    <Toast />

    <QuickCmdsListDialog
      v-model:visible="qcVisible"
      @run="runQuickCmd"
      @add="onAddCmd"
      @edit="onEditCmd"
      @delete="onDeleteCmd"
    />
    <QuickCmdEditDialog v-model:visible="editVisible" :cmd="editingCmd" @save="saveQuickCmd" />
    <SettingsDialog v-model:visible="settingsVisible" />
    <ConfirmDialog
      :visible="deleteDialogVisible"
      :title="t('qc_delete_confirm_title')"
      :message="t('qc_delete_confirm_msg', { name: deleteTarget?.name || '' })"
      :confirm-text="t('qc_delete')"
      :cancel-text="t('confirm_cancel')"
      @confirm="confirmDeleteQuickCmd"
      @cancel="cancelDeleteQuickCmd"
      @update:visible="(v) => { if (!v) deleteDialogVisible = false }"
    />
  </div>
</template>