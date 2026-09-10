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
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center p-4">
        <div class="absolute inset-0 bg-black/50" @click="close"></div>
        <div
          class="relative w-full max-w-lg bg-surface dark:bg-surface-dark border border-line dark:border-line-dark rounded-xl shadow-pop flex flex-col"
        >
          <!-- 顶部：标题 + 新增 / 关闭（文字按钮，并排右对齐） -->
        <div class="flex items-center justify-between px-5 py-3 border-b border-line dark:border-line-dark">
          <h3 class="font-display text-base font-semibold text-ink dark:text-ink-dark">{{ t('qc_title') }}</h3>
          <div class="flex items-center gap-2">
            <button class="g-btn-primary !h-8 px-4 text-sm" @click="emit('add')">{{ t('qc_add') }}</button>
            <button class="g-btn-ghost !h-8 px-3 text-sm" @click="close">{{ t('qc_close') }}</button>
          </div>
        </div>

          <!-- 命令卡片列表 -->
          <div class="flex-1 overflow-y-auto px-5 py-4 space-y-3 max-h-[50vh]">
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
              v-for="c in store.commands"
              :key="c.id"
              class="group rounded-lg border border-line dark:border-line-dark hover:border-brand/50 cursor-pointer transition-colors"
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
              <div class="flex items-center gap-2 px-3.5 py-2 border-t border-line dark:border-line-dark">
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
              </div>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>