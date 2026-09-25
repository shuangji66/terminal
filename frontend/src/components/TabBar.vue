<script setup lang="ts">
// TabBar — 顶部标签栏：会话标签（点击激活 / 双击重命名 / ×关闭）+ 新建按钮，
// 以及右侧的快捷指令 / 主题 / 语言入口。无侧边栏与底栏，所有导航都在顶部。
import { ref, computed, nextTick, onMounted, watch } from 'vue'
import { t } from '@/i18n'
import { useSessionsStore } from '@/stores/sessions'
import { usePaneControlsStore } from '@/stores/paneControls'
import { useSettingsStore } from '@/stores/settings'
import type { UserSpec } from '@/serverapi'
import ConfirmDialog from './ConfirmDialog.vue'
import UserPickDialog from './UserPickDialog.vue'

const emit = defineEmits<{
  (e: 'quickCmds'): void
  (e: 'settings'): void
}>()

const store = useSessionsStore()
const pc = usePaneControlsStore()
const settings = useSettingsStore()

// 激活标签终端的操作入口（复制/粘贴/清屏/重连）
const activeControls = computed(() => pc.get(store.activeUid))

// ---------- 新建终端：按启动用户模式决定（custom → 弹窗选择用户） ----------
const userPickVisible = ref(false)

function onAddTab() {
  if (settings.userMode === 'custom') {
    userPickVisible.value = true
  } else {
    store.addTab({ userSpec: settings.userMode === 'root' ? 'root' : 'nas' })
  }
}

function onUserPicked(spec: UserSpec) {
  userPickVisible.value = false
  store.addTab({ userSpec: spec })
}

// ---------- 标签条：向左常驻“新建”按钮 + 拖拽/滚轮横滚 ----------
const tabStripRef = ref<HTMLElement | null>(null)

// 桌面端功能名（搜索/粘贴/清屏/重连/快捷指令/设置）显隐：
//  - 手动《》按钮折叠/展开（常驻搜索按钮左侧；展开只靠手动触发，无自动展开）
//  - 标签溢出（新增/还原多标签）时自动收起
// 展开时标签条可视区变窄，即“隐藏更多标签页”，属自然结果。
const labelsOn = ref(true)
let lastManualExpandAt = 0

function toggleLabels() {
  labelsOn.value = !labelsOn.value
  if (labelsOn.value) {
    lastManualExpandAt = Date.now()
  }
}

// 桌面功能行（搜索/粘贴/清屏/重连/快捷指令/设置所在的那一行）是否在屏幕上：
// 判据就是 Tailwind md 断点，别改成 window.innerWidth 之类的宽度数字——两者等价，
// 但这里真正要问的是「那一行可见吗」，且不应在 JS 里再写一个 768 魔数。
const desktopRow = window.matchMedia('(min-width: 768px)')

function updateLabelState() {
  const el = tabStripRef.value
  if (!el || !desktopRow.matches) return // 仅桌面功能行可见时处理
  // 标签溢出 → 自动收起功能名；手动展开后 2s 内不立即收回，避免点了立刻又被收起
  if (labelsOn.value && el.scrollWidth > el.clientWidth + 8) {
    if (Date.now() - lastManualExpandAt > 2000) {
      labelsOn.value = false
    }
  }
}

onMounted(() => {
  updateLabelState()
  if (tabStripRef.value && window.ResizeObserver) {
    const ro = new ResizeObserver(() => updateLabelState())
    ro.observe(tabStripRef.value)
  }
})

// 标签数量变化（新增/恢复/关闭）后检测是否需自动收起
watch(
  () => store.tabs.length,
  () => nextTick(updateLabelState)
)

// 鼠标拖动 / 滚轮横向滚动标签条（仅精确指针设备，如鼠标）
const isFinePointer =
  typeof window !== 'undefined' && window.matchMedia('(pointer: fine)').matches

let dragActive = false
let dragMoved = false
let dragStartX = 0
let dragStartScroll = 0

function onStripMouseDown(e: MouseEvent) {
  if (!isFinePointer || e.button !== 0) return
  dragActive = true
  dragMoved = false
  dragStartX = e.clientX
  dragStartScroll = tabStripRef.value?.scrollLeft ?? 0
}
function onStripMouseMove(e: MouseEvent) {
  if (!dragActive || !tabStripRef.value) return
  const dx = e.clientX - dragStartX
  if (Math.abs(dx) > 5) dragMoved = true
  tabStripRef.value.scrollLeft = dragStartScroll - dx
}
function onStripMouseUp() {
  dragActive = false
}
function onStripMouseLeave() {
  dragActive = false
}
function onStripWheel(e: WheelEvent) {
  const el = tabStripRef.value
  if (!el || !isFinePointer) return
  // 滚轮纵向增量转横向滚动标签条（shift+滚轮/触控板横向亦可用）
  const dx = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY
  el.scrollLeft += dx
  e.preventDefault()
}

function onTabClick(uid: string) {
  if (dragMoved) {
    dragMoved = false
    return
  }
  store.setActive(uid)
}

// 双击重命名
const renamingUid = ref<string | null>(null)
const renameValue = ref('')
const renameInput = ref<HTMLInputElement | null>(null)

function startRename(uid: string) {
  const tab = store.tabs.find((tb) => tb.uid === uid)
  if (!tab) return
  renamingUid.value = uid
  renameValue.value = tab.title
  nextTick(() => {
    renameInput.value?.focus()
    renameInput.value?.select()
  })
}

function commitRename() {
  if (renamingUid.value) {
    store.persistTitle(renamingUid.value, renameValue.value.trim())
  }
  renamingUid.value = null
}

// 关闭标签：已有后端会话（id 非空）时二次确认；新会话可直接关闭。
// 注意：确认弹窗的 update:visible 只负责同步弹窗显隐，真正关闭在 confirmClose 中
// 执行（此前 closeTargetUid 被 update:visible 提前清空导致标签永远关不掉）。
const closeDialogVisible = ref(false)
const closeTargetUid = ref<string | null>(null)

function requestClose(uid: string) {
  const tab = store.tabs.find((tb) => tb.uid === uid)
  if (!tab) return
  if (tab.id) {
    closeTargetUid.value = uid
    closeDialogVisible.value = true
  } else {
    store.closeTab(uid)
  }
}

async function confirmClose() {
  closeDialogVisible.value = false
  if (closeTargetUid.value) {
    await store.closeTab(closeTargetUid.value)
  }
  closeTargetUid.value = null
  // 全部标签关闭后重新开一个会话（自定义模式下会弹窗选择用户）
  if (store.tabs.length === 0) {
    onAddTab()
  }
}

function cancelClose() {
  closeDialogVisible.value = false
  closeTargetUid.value = null
}

function titleOf(uid: string): string {
  const tab = store.tabs.find((tb) => tb.uid === uid)
  return tab ? store.titleFor(tab) : ''
}
</script>

<template>
  <header
    class="shrink-0 bg-white dark:bg-surface-dark border-b border-line dark:border-line-dark"
  >
    <!-- 第一行：新建（常驻左侧）+ 标签条 + 右侧控制。
         顶部安全区作为额外高度加入，避免刘海屏上固定 h-12 后内容被压向第二栏。 -->
    <div class="tabbar-primary flex items-center gap-1.5 sm:gap-3 px-2 sm:px-3">
      <!-- 新建标签：常驻左侧，不随标签增多被滚动隐藏 -->
      <div class="flex items-center shrink-0">
        <button
          class="w-8 h-8 rounded-md flex items-center justify-center text-ink-soft dark:text-ink-soft-dark hover:bg-black/5 dark:hover:bg-white/5 hover:text-brand transition-colors"
          :title="t('tab_new')"
          @click="onAddTab"
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" class="w-4 h-4">
            <line x1="12" y1="5" x2="12" y2="19" />
            <line x1="5" y1="12" x2="19" y2="12" />
          </svg>
        </button>
      </div>

      <!-- 标签条：支持鼠标拖动 / 滚轮横向滚动 -->
      <div
        ref="tabStripRef"
        class="flex items-center gap-1 overflow-x-auto no-scrollbar flex-1 min-w-0 py-1 select-none md:cursor-grab md:active:cursor-grabbing"
        @mousedown="onStripMouseDown"
        @mousemove="onStripMouseMove"
        @mouseup="onStripMouseUp"
        @mouseleave="onStripMouseLeave"
        @wheel="onStripWheel"
      >
        <div
          v-for="tab in store.tabs"
          :key="tab.uid"
          class="flex items-center gap-1.5 pl-2.5 pr-1 h-8 rounded-md text-xs font-medium cursor-pointer transition-all duration-150 shrink-0 max-w-[180px] sm:max-w-[220px]"
          :class="
            store.activeUid === tab.uid
              ? 'bg-brand text-white shadow-glow'
              : 'text-ink-soft dark:text-ink-soft-dark hover:bg-black/5 dark:hover:bg-white/5'
          "
          :title="t('tab_rename')"
          @click="onTabClick(tab.uid)"
          @dblclick="startRename(tab.uid)"
        >
          <!-- 状态点：连接中=琥珀 / 已连接=绿 / 退出=红 / 错误=红 -->
          <span
            class="w-1.5 h-1.5 rounded-full shrink-0"
            :class="
              tab.status === 'open'
                ? 'bg-success'
                : tab.status === 'connecting' || tab.restoring
                  ? 'bg-warning'
                  : 'bg-danger'
            "
          ></span>

          <!-- 重命名输入 / 标题 -->
          <input
            v-if="renamingUid === tab.uid"
            ref="renameInput"
            v-model="renameValue"
            class="w-24 bg-transparent outline-none border-b border-current text-xs font-medium"
            :placeholder="t('tab_title_placeholder')"
            @click.stop
            @keydown.enter="commitRename"
            @keydown.esc="renamingUid = null"
            @blur="commitRename"
          />
          <span v-else class="truncate">{{ titleOf(tab.uid) }}</span>

          <!-- 关闭按钮（常驻显示，不依赖悬停） -->
          <button
            class="w-4 h-4 rounded-full flex items-center justify-center transition-colors"
            :class="store.activeUid === tab.uid ? 'text-white/80 hover:bg-white/20' : 'text-ink-faint hover:bg-black/10 dark:hover:bg-white/10'"
            :title="t('tab_close')"
            @click.stop="requestClose(tab.uid)"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" class="w-3 h-3">
              <line x1="18" y1="6" x2="6" y2="18" />
              <line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>
      </div>

      <!-- 桌面端：《》折叠/展开 + 搜索/粘贴/清屏/重连/快捷指令/设置（搜索仅桌面端，移动端屏蔽） -->
      <div class="hidden md:flex items-center gap-0.5 sm:gap-1 shrink-0">
        <!-- 折叠/展开功能名：手动切换；标签溢出时会自动收起 -->
        <button
          class="g-btn-ghost !px-1 sm:!px-1.5 !h-8"
          :title="labelsOn ? t('labels_collapse') : t('labels_expand')"
          @click="toggleLabels"
        >
          <!-- 展开（功能名显示）时显示 》：点击折叠；收起时显示 《：点击展开 -->
          <svg v-if="labelsOn" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="w-3.5 h-3.5">
            <path d="m6 17 5-5-5-5" />
            <path d="m13 17 5-5-5-5" />
          </svg>
          <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="w-3.5 h-3.5">
            <path d="m11 17-5-5 5-5" />
            <path d="m18 17-5-5 5-5" />
          </svg>
        </button>
        <button class="g-btn-ghost !px-1.5 sm:!px-2 !h-8" :title="t('act_search')" @click="activeControls?.search?.()">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4">
            <circle cx="11" cy="11" r="8" />
            <line x1="21" y1="21" x2="16.65" y2="16.65" />
          </svg>
          <span :class="labelsOn ? 'hidden md:inline' : 'hidden'">{{ t('act_search') }}</span>
        </button>
        <button class="g-btn-ghost !px-1.5 sm:!px-2 !h-8" :title="t('act_paste')" @click="activeControls?.paste?.()">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4">
            <path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2" />
            <rect x="8" y="2" width="8" height="4" rx="1" />
          </svg>
          <span :class="labelsOn ? 'hidden md:inline' : 'hidden'">{{ t('act_paste') }}</span>
        </button>
        <button class="g-btn-ghost !px-1.5 sm:!px-2 !h-8" :title="t('act_clear')" @click="activeControls?.clear?.()">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4">
            <rect x="3" y="3" width="18" height="18" rx="2" />
            <path d="m9 9 6 6M15 9l-6 6" />
          </svg>
          <span :class="labelsOn ? 'hidden md:inline' : 'hidden'">{{ t('act_clear') }}</span>
        </button>
        <button class="g-btn-ghost !px-1.5 sm:!px-2 !h-8" :title="t('act_reconnect')" @click="activeControls?.reconnect?.()">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4">
            <path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8" />
            <path d="M21 3v5h-5" />
            <path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16" />
            <path d="M3 21v-5h5" />
          </svg>
          <span :class="labelsOn ? 'hidden md:inline' : 'hidden'">{{ t('act_reconnect') }}</span>
        </button>
        <button class="g-btn-ghost !px-2 sm:!px-2.5 !h-8 text-xs sm:text-sm" :title="t('act_quick_cmds')" @click="emit('quickCmds')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4">
            <polyline points="4 17 10 11 4 5" />
            <line x1="12" y1="19" x2="20" y2="19" />
          </svg>
          <span :class="labelsOn ? 'hidden md:inline' : 'hidden'">{{ t('act_quick_cmds') }}</span>
        </button>
        <!-- 设置（功能名自适应显示，移动端固定隐藏仅图标） -->
        <button class="g-btn-ghost !px-1.5 sm:!px-2 !h-8" :title="t('settings_title')" @click="emit('settings')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4">
            <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
            <circle cx="12" cy="12" r="3" />
          </svg>
          <span :class="labelsOn ? 'hidden md:inline' : 'hidden'">{{ t('settings_title') }}</span>
        </button>
      </div>
    </div>

    <!-- 移动端第二行：左侧设置；右侧粘贴/清屏/重连/快捷指令。
         使用明确最小高度和上下内边距，让按钮与分隔线始终留出空间。 -->
    <div
      class="flex md:hidden min-h-10 items-center justify-between gap-0.5 px-2 py-1.5 bg-black/[0.03] dark:bg-white/[0.04] border-t border-line/70 dark:border-line-dark/70"
    >
      <!-- 左：设置（主题/语言已移入设置弹窗） -->
      <div class="flex items-center gap-0.5">
        <button class="g-btn-ghost !px-1.5 !h-7" :title="t('settings_title')" @click="emit('settings')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-3.5 h-3.5">
            <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
            <circle cx="12" cy="12" r="3" />
          </svg>
        </button>
      </div>

      <!-- 右：粘贴/清屏/重连/快捷指令（不含搜索与复制，复制已由桌面自动复制+移动端系统文字工具取代） -->
      <div class="flex items-center gap-0.5">
      <button class="g-btn-ghost !px-2 !h-7" :title="t('act_paste')" @click="activeControls?.paste?.()">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-3.5 h-3.5">
          <path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2" />
          <rect x="8" y="2" width="8" height="4" rx="1" />
        </svg>
      </button>
      <button class="g-btn-ghost !px-2 !h-7" :title="t('act_clear')" @click="activeControls?.clear?.()">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-3.5 h-3.5">
          <rect x="3" y="3" width="18" height="18" rx="2" />
          <path d="m9 9 6 6M15 9l-6 6" />
        </svg>
      </button>
      <button class="g-btn-ghost !px-2 !h-7" :title="t('act_reconnect')" @click="activeControls?.reconnect?.()">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-3.5 h-3.5">
          <path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8" />
          <path d="M21 3v5h-5" />
          <path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16" />
          <path d="M3 21v-5h5" />
        </svg>
      </button>
      <button class="g-btn-ghost !px-2 !h-7 text-xs" :title="t('act_quick_cmds')" @click="emit('quickCmds')">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-3.5 h-3.5">
          <polyline points="4 17 10 11 4 5" />
          <line x1="12" y1="19" x2="20" y2="19" />
        </svg>
        <span class="hidden sm:inline">{{ t('act_quick_cmds') }}</span>
      </button>
      </div>
    </div>

    <!-- 关闭标签确认 -->
    <ConfirmDialog
      :visible="closeDialogVisible"
      :title="t('confirm_close_tab_title')"
      :message="t('confirm_close_tab_msg', { title: closeTargetUid ? titleOf(closeTargetUid) : '' })"
      :confirm-text="t('confirm_ok')"
      :cancel-text="t('confirm_cancel')"
      @confirm="confirmClose"
      @cancel="cancelClose"
      @update:visible="(v) => { if (!v) closeDialogVisible = false }"
    />

    <!-- 自定义模式下新建终端：弹窗选择用户 -->
    <UserPickDialog
      :visible="userPickVisible"
      @pick="onUserPicked"
      @update:visible="(v) => { if (!v) userPickVisible = false }"
    />
  </header>
</template>