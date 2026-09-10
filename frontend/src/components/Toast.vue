<script setup lang="ts">
import { useToastStore } from '@/stores/toast'

const toast = useToastStore()
</script>

<template>
  <Teleport to="body">
    <div class="fixed top-3 left-1/2 -translate-x-1/2 z-[100] flex flex-col items-center gap-2 pointer-events-none w-full px-4">
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