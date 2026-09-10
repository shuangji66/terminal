<script setup lang="ts">
// UserPickDialog — 自定义启动用户模式下，每次新建终端时弹窗选择：
// 以「登录用户」还是「ROOT」建立该会话。
import { ref, watch } from 'vue'
import { t } from '@/i18n'
import type { UserSpec } from '@/serverapi'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
  (e: 'pick', spec: UserSpec): void
}>()

const open = ref(props.visible)
watch(
  () => props.visible,
  (v) => (open.value = v)
)

function close() {
  open.value = false
  emit('update:visible', false)
}
function pick(spec: UserSpec) {
  close()
  emit('pick', spec)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center p-4">
        <div class="absolute inset-0 bg-black/50" @click="close"></div>
        <div
          class="relative w-full max-w-xs bg-surface dark:bg-surface-dark border border-line dark:border-line-dark rounded-xl shadow-pop p-5"
        >
          <h3 class="font-display text-base font-semibold text-ink dark:text-ink-dark">{{ t('user_pick_title') }}</h3>
          <div class="mt-4 grid grid-cols-2 gap-2">
            <button
              class="rounded-md border px-3 py-2.5 text-sm font-medium text-center transition-colors border-line dark:border-line-dark text-ink dark:text-ink-dark hover:border-brand hover:bg-brand/5"
              @click="pick('nas')"
            >
              {{ t('settings_user_mode_nas') }}
            </button>
            <button
              class="rounded-md border px-3 py-2.5 text-sm font-medium text-center transition-colors border-line dark:border-line-dark text-ink dark:text-ink-dark hover:border-brand hover:bg-brand/5"
              @click="pick('root')"
            >
              {{ t('settings_user_mode_root') }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>