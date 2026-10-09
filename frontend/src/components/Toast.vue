<script setup lang="ts">
import { useToastStore } from '@/stores/toast'

const toast = useToastStore()
</script>

<template>
  <Teleport to="body">
    <!-- 定位带 = 顶栏**第一行**（标签条那一行）：top 跟随可视视口位移（--vvt，与 .app-shell
         同一条基准），带高取第一行实测高度（--tabbar-primary-h，见 useViewportHeight），
         toast 在其中垂直居中 —— 桌面/平板/移动端都居中在标签栏上，不再写死 top 值骑到
         分隔线上。内容高于第一行（长文案换行 / 多条堆叠）时靠 min-height 向下扩展，
         不会溢出到屏幕顶部之外。 -->
    <div
      class="fixed inset-x-0 z-[100] flex flex-col items-center justify-center gap-2 pointer-events-none px-4"
      style="top: var(--vvt, 0px); min-height: var(--tabbar-primary-h, calc(3rem + env(safe-area-inset-top, 0px)))"
    >
      <TransitionGroup
        enter-active-class="transition duration-200 ease-out"
        enter-from-class="opacity-0 -translate-y-2"
        leave-active-class="transition duration-150 ease-in"
        leave-to-class="opacity-0"
      >
        <div
          v-for="tb in toast.toasts"
          :key="tb.id"
          class="max-w-full px-4 py-2 rounded-lg text-sm font-medium shadow-pop border"
          :class="
            tb.type === 'success'
              ? 'bg-success text-white border-transparent'
              : tb.type === 'error'
                ? 'bg-danger text-white border-transparent'
                : 'bg-surface dark:bg-surface-dark text-ink dark:text-ink-dark border-line dark:border-line-dark'
          "
        >
          {{ tb.msg }}
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>