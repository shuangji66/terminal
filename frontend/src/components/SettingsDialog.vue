<script setup lang="ts">
// SettingsDialog — 设置弹窗：
//  - 主题 / 语言：图标按钮一行（标题下方置顶）
//  - 终端字号：数字显示框左减右加（10–26），保存在浏览器 localStorage，即时生效
//  - 终端字体：Maple Mono（内置，默认）/ 系统字体，同样只存浏览器、即时生效
//  - 关于：标题与「设置」标题同样式，右侧仅 GitHub 图标
// 会话以哪个用户启动不再有全局设置：新建终端时由 UserPickDialog 逐个选择。
import { ref, computed, watch } from 'vue'
import { t, setLocale, useI18n } from '@/i18n'
import { useTheme } from '@/composables/useTheme'
import { useSettingsStore, type TerminalFont } from '@/stores/settings'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
}>()

const settings = useSettingsStore()
const { themeMode, cycleTheme } = useTheme()
const { locale } = useI18n()

// 主题图标（随当前模式变化）
const themeIconPath = computed(() =>
  themeMode.value === 'dark'
    ? '<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>'
    : themeMode.value === 'light'
      ? '<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>'
      : '<rect width="14" height="10" x="5" y="4" rx="2"/><line x1="12" x2="12" y1="20" y2="16"/><line x1="8" x2="16" y1="20" y2="20"/>'
)

function cycleLanguage() {
  setLocale(locale.value === 'zh' ? 'en' : 'zh')
}

const open = ref(props.visible)
watch(
  () => props.visible,
  (v) => (open.value = v)
)

function close() {
  open.value = false
  emit('update:visible', false)
}

// ---------- 终端字体 ----------
const fontOptions = computed<{ value: TerminalFont; label: string }[]>(() => [
  { value: 'maple', label: t('settings_font_maple') },
  { value: 'system', label: t('settings_font_system') }
])

// ---------- 终端字号 ----------
function decFont() {
  settings.setFontSize(settings.fontSize - 1)
}
function incFont() {
  settings.setFontSize(settings.fontSize + 1)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="fixed inset-0 z-50">
        <div class="absolute inset-0 bg-black/50" @click="close"></div>
        <!-- 弹窗层 = 「终端区域」（已扣顶栏与底部辅助键条，见 style.css 的
             .term-region-center），面板在其中垂直居中、max-h-[90%] 限高，
             内容超出时自身滚动。 -->
        <div class="absolute left-0 right-0 term-region-center flex items-center justify-center px-4">
          <div
            class="relative w-full max-w-sm max-h-[90%] pointer-events-auto bg-surface dark:bg-surface-dark border border-line dark:border-line-dark rounded-xl shadow-pop p-5 overflow-y-auto"
          >
            <div class="flex items-center justify-between">
              <h3 class="font-display text-base font-semibold text-ink dark:text-ink-dark">{{ t('settings_title') }}</h3>
              <button class="g-btn-ghost !h-8 !px-2 text-lg leading-none" @click="close">×</button>
            </div>

            <!-- 主题 / 语言（图标+功能名，一行显示，标题下方置顶） -->
            <div class="mt-3 grid grid-cols-2 gap-2">
              <button
                class="flex items-center justify-center gap-2 rounded-md border border-line dark:border-line-dark text-ink dark:text-ink-dark hover:border-brand hover:bg-brand/5 transition-colors px-3 py-2 text-sm font-medium"
                :title="t('theme_label')"
                @click="cycleTheme"
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4" v-html="themeIconPath"></svg>
                <span>{{ t('theme_' + themeMode) }}</span>
              </button>
              <button
                class="flex items-center justify-center gap-2 rounded-md border border-line dark:border-line-dark text-ink dark:text-ink-dark hover:border-brand hover:bg-brand/5 transition-colors px-3 py-2 text-sm font-medium"
                :title="t('lang_label')"
                @click="cycleLanguage"
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="w-4 h-4">
                  <circle cx="12" cy="12" r="10" />
                  <path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20" />
                  <path d="M2 12h20" />
                </svg>
                <span>{{ locale === 'zh' ? t('lang_zh') : t('lang_en') }}</span>
              </button>
            </div>

            <!-- 终端字体：内置 Maple Mono / 系统字体（无描述） -->
            <div class="mt-4">
              <label class="block text-xs font-medium text-ink-soft dark:text-ink-soft-dark mb-1.5">
                {{ t('settings_font_family') }}
              </label>
              <div class="grid grid-cols-2 gap-2">
                <button
                  v-for="opt in fontOptions"
                  :key="opt.value"
                  class="rounded-md border px-3 py-2 text-sm font-medium transition-colors"
                  :class="
                    settings.terminalFont === opt.value
                      ? 'border-brand bg-brand/10 text-brand'
                      : 'border-line dark:border-line-dark text-ink dark:text-ink-dark hover:border-brand hover:bg-brand/5'
                  "
                  @click="settings.setTerminalFont(opt.value)"
                >
                  {{ opt.label }}
                </button>
              </div>
            </div>

            <!-- 终端字号 -->
            <div class="mt-4">
              <label class="block text-xs font-medium text-ink-soft dark:text-ink-soft-dark mb-1.5">
                {{ t('settings_font_size') }}
              </label>
              <div class="flex items-center gap-2">
                <!-- 左减 数字显示框 右加 -->
                <div class="flex items-center rounded-md border border-line dark:border-line-dark overflow-hidden">
                  <button
                    class="w-9 h-9 flex items-center justify-center text-lg text-ink-soft dark:text-ink-soft-dark hover:bg-black/5 dark:hover:bg-white/5 transition-colors"
                    :disabled="settings.fontSize <= 10"
                    :title="t('settings_font_size') + ' −'"
                    @click="decFont"
                  >
                    −
                  </button>
                  <span class="w-12 h-9 flex items-center justify-center text-sm font-mono text-ink dark:text-ink-dark border-x border-line dark:border-line-dark">
                    {{ settings.fontSize }}
                  </span>
                  <button
                    class="w-9 h-9 flex items-center justify-center text-lg text-ink-soft dark:text-ink-soft-dark hover:bg-black/5 dark:hover:bg-white/5 transition-colors"
                    :disabled="settings.fontSize >= 26"
                    :title="t('settings_font_size') + ' +'"
                    @click="incFont"
                  >
                    +
                  </button>
                </div>
                <span class="text-xs text-ink-faint dark:text-ink-faint-dark">{{ t('settings_font_size_hint') }}</span>
              </div>
            </div>

            <!-- 关于：标题与「设置」标题同样式，GitHub 仅图标、紧靠标题右侧 -->
            <div class="mt-4 pt-4 border-t border-line dark:border-line-dark">
              <div class="flex items-center gap-1.5">
                <h3 class="font-display text-base font-semibold text-ink dark:text-ink-dark">{{ t('about_title') }}</h3>
                <a
                  href="https://github.com/shuangji66/terminal"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="g-btn-ghost !h-8 !px-2"
                  :title="t('about_github')"
                  :aria-label="t('about_github')"
                >
                  <svg viewBox="0 0 16 16" fill="currentColor" class="w-4 h-4">
                    <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
                  </svg>
                </a>
              </div>
              <p class="mt-1.5 text-xs text-ink-soft dark:text-ink-soft-dark leading-relaxed">{{ t('about_desc') }}</p>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>