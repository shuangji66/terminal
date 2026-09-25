<script setup lang="ts">
// UserPickDialog — 每次新建终端时弹窗选择以哪个用户启动：
//   1) 登录用户 / ROOT（常规选项，置于顶部）
//   2) NAS 应用用户列表（后端执行 appcenter-cli list 解析出的 APP NAME，
//      已过滤 trim.* 系统软件与不可用项）；选中后以 user=app:<APP NAME> 进入 bash，
//      HOME 与工作目录都为该应用家目录（系统不会给应用用户设 HOME，由后端赋予），
//      因此进入后 ~ 即该目录。路径模板由后端经 homeTemplate 下发，前端不硬编码。
//  列表来自 appUsers store 的内存缓存（冷启动已预取），每次打开弹窗只读缓存、
//  不再请求后端；需要最新列表时用右上角刷新按钮。
//  关闭方式（× 按钮 / 点弹窗外侧）只关弹窗、不新建会话；ROOT 需二次确认。
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { t } from '@/i18n'
import type { UserSpec } from '@/serverapi'
import { useAppUsersStore } from '@/stores/appUsers'
import ConfirmDialog from './ConfirmDialog.vue'
import { useSessionsStore } from '@/stores/sessions'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
  (e: 'pick', spec: UserSpec): void
}>()

const open = ref(props.visible)
const appUsers = useAppUsersStore()
const { apps, homeTemplate, loading, loadError } = storeToRefs(appUsers)
const sessions = useSessionsStore()

// 常规选项直接显示当前登录用户（NAS 用户名，来自 /api/info 的 nasUser），
// 解析不到时才退回「当前登录用户」这类说明文案。
const currentUserName = computed(() => sessions.info?.nasUser?.username || '')

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

// 手动刷新：安装/卸载应用后取最新列表（store 的 load 会覆盖缓存）
function refreshApps() {
  void appUsers.load()
}

watch(
  () => props.visible,
  (v) => {
    open.value = v
    if (v) {
      filter.value = ''
      // 只读缓存：冷启动已预取，正常不会发请求；仅在预取失败（缓存为空）时补一次。
      void appUsers.ensureLoaded()
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

// ROOT 需二次确认（红字警示）：确认后才以 root 建会话，取消则留在本弹窗继续选。
const confirmRoot = ref(false)

function askRoot() {
  confirmRoot.value = true
}
function confirmPickRoot() {
  confirmRoot.value = false
  pick('root')
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div v-if="open" class="fixed inset-0 z-50">
        <div class="absolute inset-0 bg-black/50" @click="close"></div>
        <!-- 弹窗层对齐「终端区域」的 90% 高度带（见 style.css 的 .term-region-center），
             面板 max-h-full 限高：常规选项与筛选框固定，应用列表内部滚动。 -->
        <div class="absolute left-0 right-0 term-region-center flex items-center justify-center p-4">
          <div
            class="relative w-full max-w-sm max-h-full pointer-events-auto bg-surface dark:bg-surface-dark border border-line dark:border-line-dark rounded-xl shadow-pop p-5 flex flex-col"
          >
            <div class="flex items-center justify-between shrink-0">
              <h3 class="font-display text-base font-semibold text-ink dark:text-ink-dark">{{ t('user_pick_title') }}</h3>
              <!-- × 关闭：只关弹窗、不新建会话（点弹窗外侧同为关闭） -->
              <button class="g-btn-ghost !h-8 !px-2 text-lg leading-none" :title="t('qc_close')" @click="close">×</button>
            </div>

            <!-- 常规用户 -->
            <div class="mt-4 grid grid-cols-2 gap-2 shrink-0">
              <!-- 登录用户：直接显示当前 NAS 用户名（如 niubi），解析不到时退回说明文案 -->
              <button
                class="rounded-md border px-3 py-2.5 text-sm font-medium text-center truncate transition-colors border-line dark:border-line-dark text-ink dark:text-ink-dark hover:border-brand hover:bg-brand/5"
                :title="currentUserName || t('user_pick_current')"
                @click="pick('nas')"
              >
                {{ currentUserName || t('user_pick_current') }}
              </button>
              <!-- ROOT：红边框红字（不填充），点击需二次确认 -->
              <button
                class="rounded-md border px-3 py-2.5 text-sm font-medium text-center transition-colors border-danger text-danger hover:bg-danger/10"
                @click="askRoot"
              >
                {{ t('user_pick_root') }}
              </button>
            </div>

            <!-- 应用用户：标题与刷新按钮紧邻（不显示数量；只在加载失败时右侧提示） -->
            <div class="mt-4 flex items-center gap-1.5 shrink-0">
              <span class="text-xs font-medium text-ink-soft dark:text-ink-soft-dark">
                {{ t('user_pick_apps') }}
              </span>
              <button
                class="g-btn-ghost !px-1 !h-6"
                :title="t('user_pick_refresh')"
                :disabled="loading"
                @click="refreshApps"
              >
                <svg
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.5"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  class="w-3.5 h-3.5"
                  :class="loading ? 'animate-spin' : ''"
                >
                  <path d="M21 12a9 9 0 1 1-2.64-6.36" />
                  <path d="M21 3v5h-5" />
                </svg>
              </button>
              <span
                v-if="loadError"
                class="ml-auto text-xs text-danger truncate max-w-[9rem]"
                :title="loadError"
                >{{ t('user_pick_apps_failed') }}</span
              >
            </div>

            <input
              v-if="apps.length > 6"
              v-model="filter"
              class="mt-2 shrink-0 w-full rounded-md border border-line dark:border-line-dark bg-transparent px-3 py-2 text-sm text-ink dark:text-ink-dark outline-none focus:border-brand"
              :placeholder="t('user_pick_filter')"
            />

            <div class="mt-2 flex-1 min-h-0 overflow-y-auto -mr-1 pr-1">
              <!-- 刷新失败但缓存仍在时不覆盖列表，错误只在标题行提示 -->
              <p
                v-if="loading && apps.length === 0"
                class="py-3 text-center text-xs text-ink-soft dark:text-ink-soft-dark"
              >
                {{ t('loading') }}
              </p>
              <p
                v-else-if="loadError && apps.length === 0"
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
      </div>
    </Transition>

    <!-- 以 ROOT 启动的二次确认 -->
    <ConfirmDialog
      :visible="confirmRoot"
      :title="t('user_pick_root_confirm_title')"
      :message="t('user_pick_root_confirm_msg')"
      :confirm-text="t('user_pick_root')"
      :cancel-text="t('confirm_cancel')"
      @confirm="confirmPickRoot"
      @cancel="confirmRoot = false"
      @update:visible="(v) => { if (!v) confirmRoot = false }"
    />
  </Teleport>
</template>
