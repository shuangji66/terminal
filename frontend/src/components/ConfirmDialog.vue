<script setup lang="ts">
import { ref, watch } from 'vue'
import { t } from '@/i18n'

const props = defineProps<{
  visible: boolean
  title: string
  message: string
  confirmText?: string
  cancelText?: string
  danger?: boolean
}>()

const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
  (e: 'confirm'): void
  (e: 'cancel'): void
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
function onConfirm() {
  close()
  emit('confirm')
}
function onCancel() {
  close()
  emit('cancel')
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center p-4">
        <div class="absolute inset-0 bg-black/50" @click="onCancel"></div>
        <div
          class="relative w-full max-w-sm bg-surface dark:bg-surface-dark border border-line dark:border-line-dark rounded-xl shadow-pop p-5"
        >
          <h3 class="font-display text-base font-semibold text-ink dark:text-ink-dark">{{ title }}</h3>
          <p class="mt-2 text-sm text-ink-soft dark:text-ink-soft-dark break-words">{{ message }}</p>
          <div class="mt-5 flex justify-end gap-2">
            <button class="g-btn-ghost !h-9 !px-4 text-sm" @click="onCancel">
              {{ cancelText || t('confirm_cancel') }}
            </button>
            <button class="g-btn-ghost !h-9 !px-4 text-sm text-danger" @click="onConfirm">
              {{ confirmText || t('confirm_ok') }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>