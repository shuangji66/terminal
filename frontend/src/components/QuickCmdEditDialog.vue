<script setup lang="ts">
import { ref, watch } from 'vue'
import { t } from '@/i18n'

const props = defineProps<{
  visible: boolean
  cmd: { id: string; name: string; content: string; auto: boolean } | null
}>()

const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
  (e: 'save', payload: { name: string; content: string; auto: boolean }): void
}>()

const open = ref(props.visible)
watch(
  () => props.visible,
  (v) => {
    open.value = v
    if (v) {
      name.value = props.cmd?.name || ''
      content.value = props.cmd?.content || ''
      auto.value = props.cmd?.auto || false
    }
  }
)

const name = ref('')
const content = ref('')
const auto = ref(false)
const error = ref('')

function close() {
  open.value = false
  emit('update:visible', false)
  error.value = ''
}

function save() {
  if (!name.value.trim()) {
    error.value = t('qc_name_required')
    return
  }
  if (!content.value.trim()) {
    error.value = t('qc_content_required')
    return
  }
  emit('save', { name: name.value.trim(), content: content.value.trim(), auto: auto.value })
  close()
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center p-4">
        <div class="absolute inset-0 bg-black/50" @click="close"></div>
        <div
          class="relative w-full max-w-md bg-surface dark:bg-surface-dark border border-line dark:border-line-dark rounded-xl shadow-pop p-5"
        >
          <h3 class="font-display text-base font-semibold text-ink dark:text-ink-dark">
            {{ cmd ? t('qc_edit_title') : t('qc_add_title') }}
          </h3>

          <div class="mt-4 space-y-3">
            <div>
              <label class="block text-xs font-medium text-ink-soft dark:text-ink-soft-dark mb-1">{{ t('qc_name') }}</label>
              <input v-model="name" class="g-input" :placeholder="t('qc_name_placeholder')" @keydown.enter="save" />
            </div>
            <div>
              <label class="block text-xs font-medium text-ink-soft dark:text-ink-soft-dark mb-1">{{ t('qc_content') }}</label>
              <textarea
                v-model="content"
                class="g-input !py-2 font-mono text-xs resize-y"
                rows="3"
                :placeholder="t('qc_content_placeholder')"
                spellcheck="false"
              ></textarea>
            </div>
            <label class="flex items-center gap-2 cursor-pointer select-none">
              <input v-model="auto" type="checkbox" class="w-4 h-4 accent-brand" />
              <span class="text-sm text-ink dark:text-ink-dark">{{ t('qc_auto') }}</span>
              <span class="text-xs text-ink-faint dark:text-ink-faint-dark">{{ t('qc_auto_hint') }}</span>
            </label>
            <p v-if="error" class="text-xs text-danger">{{ error }}</p>
          </div>

          <div class="mt-5 flex justify-end gap-2">
            <button class="g-btn-ghost !h-9 !px-4 text-sm" @click="close">{{ t('qc_cancel') }}</button>
            <button class="g-btn-primary !h-9 px-5 text-sm" @click="save">{{ t('qc_save') }}</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>