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
  // 名称 trim，**命令内容原样提交**：前导/行尾空格与换行都是命令的一部分（多行脚本、
  // heredoc、`printf '  x'`），trim 掉就是改用户写下的命令，也会让后端「加密前后内容逐字节
  // 不变」那条承诺失去意义。这里只判「全是空白 = 空」（与后端 sanitizeQuickCmds 同一口径）。
  emit('save', { name: name.value.trim(), content: content.value, auto: auto.value })
  close()
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="fixed inset-0 z-50">
        <div class="absolute inset-0 bg-black/50" @click="close"></div>
        <!-- 面板在「终端区域」内垂直居中（见 style.css 的 .term-region-center）：与快捷指令
             列表等弹窗同一条定位带。这里尤其不能退回 fixed inset-0 居中——编辑框是文本域，
             iOS 键盘弹起时布局视口不缩，弹窗会被键盘盖住。 -->
        <div class="absolute left-0 right-0 term-region-center flex items-center justify-center px-4">
          <div
            class="relative w-full max-w-md max-h-[90%] overflow-y-auto pointer-events-auto bg-surface dark:bg-surface-dark border border-line dark:border-line-dark rounded-xl shadow-pop p-5"
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
              </label>
              <p v-if="error" class="text-xs text-danger">{{ error }}</p>
            </div>

            <div class="mt-5 flex justify-end gap-2">
              <button class="g-btn-ghost !h-9 !px-4 text-sm" @click="close">{{ t('qc_cancel') }}</button>
              <button class="g-btn-primary !h-9 px-5 text-sm" @click="save">{{ t('qc_save') }}</button>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>