<script setup lang="ts">
import { ref, watch } from 'vue'
import { t } from '@/i18n'
import { useQuickCmdsStore } from '@/stores/quickCmds'
import type { QuickCmd } from '@/serverapi'

const props = defineProps<{ visible: boolean }>()

const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
  (e: 'run', cmd: QuickCmd): void
  (e: 'add'): void
  (e: 'edit', cmd: QuickCmd): void
  (e: 'delete', cmd: QuickCmd): void
}>()

const store = useQuickCmdsStore()

const open = ref(props.visible)
const loadingReorder = ref(false)
watch(
  () => props.visible,
  (v) => {
    open.value = v
    if (v) store.load()
  }
)

function close() {
  open.value = false
  emit('update:visible', false)
}

// 交换相邻两个命令的顺序并持久化
// 注意：Pinia setup store 会把 ref 自动解包，store.commands 直接是数组（不能加 .value）
async function moveUp(idx: number) {
  if (idx <= 0) return
  const cmds = [...store.commands]
  const [moved] = cmds.splice(idx, 1)
  cmds.splice(idx - 1, 0, moved)
  loadingReorder.value = true
  try {
    await store.reorder(cmds.map((c) => c.id))
  } finally {
    loadingReorder.value = false
  }
}

async function moveDown(idx: number) {
  if (idx >= store.commands.length - 1) return
  const cmds = [...store.commands]
  const [moved] = cmds.splice(idx, 1)
  cmds.splice(idx + 1, 0, moved)
  loadingReorder.value = true
  try {
    await store.reorder(cmds.map((c) => c.id))
  } finally {
    loadingReorder.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="fixed inset-0 z-50">
        <!-- 遮罩仍铺满全屏（维持模态语义：点任意处关闭，含顶栏区域） -->
        <div class="absolute inset-0 bg-black/50" @click="close"></div>
        <!-- 面板在「终端区域」的 90% 高度带内居中（见 style.css 的 .term-region-center）：
             高度随内容自适应、上限为终端区域的 90%，超出时列表内部滚动；
             宽度仍由 max-w-lg 居中限制（卡片式，不满宽）。 -->
        <div class="absolute left-0 right-0 term-region-center flex items-center justify-center p-4">
          <div
            class="relative w-full max-w-lg max-h-full pointer-events-auto bg-surface dark:bg-surface-dark border border-line dark:border-line-dark rounded-xl shadow-pop flex flex-col"
          >
            <!-- 顶部：标题 + 新增 / 关闭 -->
            <div class="flex items-center justify-between px-5 py-3 border-b border-line dark:border-line-dark shrink-0">
              <h3 class="font-display text-base font-semibold text-ink dark:text-ink-dark">{{ t('qc_title') }}</h3>
              <div class="flex items-center gap-2">
                <button class="g-btn-primary !h-8 px-4 text-sm" @click="emit('add')">{{ t('qc_add') }}</button>
                <button class="g-btn-ghost !h-8 px-3 text-sm" @click="close">{{ t('qc_close') }}</button>
              </div>
            </div>

            <!-- 命令卡片列表：占满面板剩余高度，超出时内部滚动（面板本身不再撑满整个终端区） -->
            <div class="flex-1 min-h-0 overflow-y-auto px-5 py-4 space-y-3">
            <div v-if="store.loading" class="text-sm text-ink-soft dark:text-ink-soft-dark text-center py-8">
              {{ t('loading') }}
            </div>
            <div
              v-else-if="!store.commands.length"
              class="text-sm text-ink-soft dark:text-ink-soft-dark text-center py-8"
            >
              {{ t('qc_empty') }}
            </div>
            <div
              v-for="(c, idx) in store.commands"
              :key="c.id"
              class="group rounded-lg border border-line dark:border-line-dark hover:border-brand/50 transition-colors cursor-pointer"
              :title="c.content"
              @click="emit('run', c)"
            >
              <div class="px-3.5 py-2.5">
                <div class="flex items-center gap-2 min-w-0">
                  <span class="text-sm font-semibold text-ink dark:text-ink-dark truncate">{{ c.name }}</span>
                  <span
                    v-if="c.auto"
                    class="shrink-0 inline-flex items-center px-1.5 py-0.5 rounded-full text-[10px] font-medium bg-brand-soft text-brand"
                  >
                    {{ t('qc_auto_tag') }}
                  </span>
                </div>
                <p class="mt-1 text-xs font-mono text-ink-soft dark:text-ink-soft-dark truncate">{{ c.content }}</p>
              </div>
              <!-- 第二行：左侧 编辑/删除，右侧 上移/下移（SVG 图标） -->
              <div class="flex items-center gap-1 px-3.5 py-2 border-t border-line dark:border-line-dark">
                <button class="g-btn-ghost !h-7 !px-2.5 text-xs" :title="t('qc_edit')" @click.stop="emit('edit', c)">
                  {{ t('qc_edit') }}
                </button>
                <button
                  class="g-btn-ghost !h-7 !px-2.5 text-xs !text-danger hover:!bg-danger/10"
                  :title="t('qc_delete')"
                  @click.stop="emit('delete', c)"
                >
                  {{ t('qc_delete') }}
                </button>
                <div class="flex-1"></div>
                <!-- 向上移动按钮（第一项不可上移） -->
                <button
                  v-if="idx > 0"
                  class="g-btn-ghost !h-7 !px-2"
                  :title="t('qc_move_up')"
                  :disabled="loadingReorder"
                  @click.stop="moveUp(idx)"
                >
                  <svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M18 15l-6-6-6 6" />
                  </svg>
                </button>
                <!-- 向下移动按钮（最后一项不可下移） -->
                <button
                  v-if="idx < store.commands.length - 1"
                  class="g-btn-ghost !h-7 !px-2"
                  :title="t('qc_move_down')"
                  :disabled="loadingReorder"
                  @click.stop="moveDown(idx)"
                >
                  <svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </button>
              </div>
            </div>
          </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>