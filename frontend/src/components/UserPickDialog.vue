<script setup lang="ts">
// UserPickDialog — 自定义启动用户模式下，每次新建终端时弹窗选择：
//   1) 登录用户 / ROOT（原有能力，置于顶部）
//   2) NAS 应用用户列表（后端执行 appcenter-cli list 解析出的 APP NAME，
//      已过滤 trim.* 系统软件与不可用项）；选中后以 user=app:<APP NAME> 进入 bash，
//      HOME 与工作目录都为该应用家目录（系统不会给应用用户设 HOME，由后端赋予），
//      因此进入后 ~ 即该目录。路径模板由后端经 homeTemplate 下发，前端不硬编码。
import { computed, ref, watch } from 'vue'
import { t } from '@/i18n'
import { api, type AppUserInfo, type UserSpec } from '@/serverapi'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
  (e: 'pick', spec: UserSpec): void
}>()

const open = ref(props.visible)
const apps = ref<AppUserInfo[]>([])
// 应用家目录模板（后端 TERMINAL_APP_HOME_TEMPLATE），用于提示该会话的 HOME
const homeTemplate = ref('')
const loading = ref(false)
const loadError = ref('')

function homeOf(name: string): string {
  return homeTemplate.value ? homeTemplate.value.replace('%s', name) : ''
}
// 列表项提示：进入该应用用户会话后的家目录（HOME = ~ = 工作目录）
function appHint(name: string): string {
  const dir = homeOf(name)
  return dir ? `${t('user_pick_app_home')} ${dir}` : ''
}

// 应用名过滤（列表可能上百条，弹窗内直接筛选）；派生值用 computed，不额外维护状态
const filter = ref('')
const filteredApps = computed(() => {
  const kw = filter.value.trim().toLowerCase()
  if (!kw) return apps.value
  return apps.value.filter(
    (a) =>
      a.name.toLowerCase().includes(kw) || (a.displayName || '').toLowerCase().includes(kw)
  )
})

async function loadApps() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.appUsers()
    apps.value = res.apps || []
    homeTemplate.value = res.homeTemplate || ''
  } catch (e) {
    apps.value = []
    loadError.value = e instanceof Error ? e.message : String(e)
    console.warn('load app users error:', e)
  } finally {
    loading.value = false
  }
}

watch(
  () => props.visible,
  (v) => {
    open.value = v
    if (v) {
      filter.value = ''
      loadApps() // 每次打开都刷新：安装/卸载应用后列表即时生效（后端侧有短缓存）
    }
  }
)

function close() {
  open.value = false
  emit('update:visible', false)
}
function pick(spec: UserSpec) {
  close()
  emit('pick', spec)
}
// 应用用户标识：app:<APP NAME>（模板字面量类型，后端按该前缀分发到应用用户解析）
function pickApp(name: string) {
  pick(`app:${name}`)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center p-4">
        <div class="absolute inset-0 bg-black/50" @click="close"></div>
        <div
          class="relative w-full max-w-sm bg-surface dark:bg-surface-dark border border-line dark:border-line-dark rounded-xl shadow-pop p-5 flex flex-col max-h-[85vh]"
        >
          <h3 class="font-display text-base font-semibold text-ink dark:text-ink-dark">{{ t('user_pick_title') }}</h3>

          <!-- 常规用户 -->
          <div class="mt-4 grid grid-cols-2 gap-2 shrink-0">
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

          <!-- 应用用户 -->
          <div class="mt-4 flex items-center justify-between shrink-0">
            <span class="text-xs font-medium text-ink-soft dark:text-ink-soft-dark">
              {{ t('user_pick_apps') }}
            </span>
            <span v-if="!loading && !loadError" class="text-xs text-ink-faint">{{ apps.length }}</span>
          </div>

          <input
            v-if="apps.length > 6"
            v-model="filter"
            class="mt-2 shrink-0 w-full rounded-md border border-line dark:border-line-dark bg-transparent px-3 py-2 text-sm text-ink dark:text-ink-dark outline-none focus:border-brand"
            :placeholder="t('user_pick_filter')"
          />

          <div class="mt-2 flex-1 min-h-0 overflow-y-auto -mr-1 pr-1">
            <p v-if="loading" class="py-3 text-center text-xs text-ink-soft dark:text-ink-soft-dark">
              {{ t('loading') }}
            </p>
            <p
              v-else-if="loadError"
              class="py-3 text-center text-xs text-danger break-words"
            >
              {{ t('user_pick_apps_failed') }}<br />{{ loadError }}
            </p>
            <p
              v-else-if="filteredApps.length === 0"
              class="py-3 text-center text-xs text-ink-soft dark:text-ink-soft-dark"
            >
              {{ t('user_pick_apps_empty') }}
            </p>
            <div v-else class="grid grid-cols-1 gap-1.5">
              <button
                v-for="a in filteredApps"
                :key="a.name"
                class="rounded-md border px-3 py-2 text-left text-sm transition-colors border-line dark:border-line-dark text-ink dark:text-ink-dark hover:border-brand hover:bg-brand/5"
                :title="appHint(a.name)"
                @click="pickApp(a.name)"
              >
                <span class="block truncate font-medium">{{ a.name }}</span>
                <span v-if="a.displayName" class="block truncate text-xs text-ink-soft dark:text-ink-soft-dark">
                  {{ a.displayName }}
                </span>
              </button>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
