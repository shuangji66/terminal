<script setup lang="ts">
// 移动端辅助键条（仅在 md 以下显示）：
//   第一行：ESC | ↑ | Tab | Ctrl | Alt | Shift | Insert（↑ 在第二位，与第二行的
//            ← ↓ → 组成倒 T 型方向键组合；Insert 在最右）
//   第二行：← | ↓ | → | . | / | - | = | "
// 方向键支持长按连发；所有按键统一 emit('key') 交给终端面板发送。
// 交互策略：@click 覆盖桌面/部分移动端；同时用 @touchstart.prevent 作为移动端
//   保底（避免浏览器在 prevent.stop 容器中不合成 click 导致按键无效）。
// 底部内边距：默认安全区 + 8px；键盘弹起时 --kb-safe-bottom 为 0（安全区已被键盘覆盖，
//   见 composables/useViewportHeight.ts），否则键条与键盘之间会多出一条安全区的空隙。
const props = defineProps<{
  ctrl: boolean
  alt: boolean
  shift: boolean
}>()

const emit = defineEmits<{
  (e: 'key', data: string): void
  (e: 'toggle', mod: 'ctrl' | 'alt' | 'shift'): void
}>()

// 方向键长按连发
let repeatTimer: number | null = null

function startRepeat(key: string) {
  if (repeatTimer) return
  emit('key', key)
  repeatTimer = window.setInterval(() => emit('key', key), 100)
}
function stopRepeat() {
  if (repeatTimer) {
    clearInterval(repeatTimer)
    repeatTimer = null
  }
}

// 单次按键：@click + @touchstart.prevent 双保险
// touchstart 用于移动端保底；touchend 时不重复触发（点击只触发一次）。
// click 是桌面端主要路径；touchstart 在移动端会抢先触发一次，click 因
// 被 prevent 默认行为抑制后不合成，恰好保证只发一次。
function send(data: string) {
  emit('key', data)
}

// 键码映射（避免模板内联引号转义问题）
const K = {
  esc: '\x1b',
  tab: '\t',
  insert: '\x1b[2~',
  left: '\x1b[D',
  up: '\x1b[A',
  down: '\x1b[B',
  right: '\x1b[C',
  dot: '.',
  slash: '/',
  dash: '-',
  eq: '=',
  dquote: '"'
}

const keyCls =
  'px-2 py-1.5 text-[11px] font-medium min-w-8 bg-black/5 dark:bg-white/10 rounded-full text-ink-soft dark:text-ink-soft-dark select-none transition touch-manipulation'
const modOnCls = '!bg-brand !text-white'
</script>

<template>
  <div
    class="flex md:hidden shrink-0 flex-col gap-1 px-2 pt-1.5 bg-bg dark:bg-bg-dark border-t border-line dark:border-line-dark select-none"
    style="touch-action: manipulation; padding-bottom: calc(var(--kb-safe-bottom, env(safe-area-inset-bottom, 0px)) + 8px)"
    @touchstart.prevent.stop
  >
    <!-- 第一行：ESC | ↑ | Tab | Ctrl | Alt | Shift | Insert（↑ 为倒 T 方向键的顶端，Insert 最右） -->
    <div class="flex items-center justify-around gap-1 flex-wrap">
      <!-- ESC：@click + @touchstart.prevent 双保险 -->
      <button
        class="rounded-full"
        :class="keyCls"
        @click="send(K.esc)"
        @touchstart.prevent="send(K.esc)"
      >
        ESC
      </button>
      <!-- ↑ 方向键：长按连发 -->
      <button
        class="rounded-full"
        :class="keyCls"
        @mousedown="startRepeat(K.up)"
        @mouseup="stopRepeat"
        @mouseleave="stopRepeat"
        @touchstart.prevent="startRepeat(K.up)"
        @touchend="stopRepeat"
        @touchcancel="stopRepeat"
      >
        ↑
      </button>
      <!-- Tab -->
      <button class="rounded-full" :class="keyCls" @click="send(K.tab)" @touchstart.prevent="send(K.tab)">Tab</button>
      <!-- 修饰键切换 -->
      <button
        class="rounded-full"
        :class="[keyCls, ctrl ? modOnCls : '']"
        @click="emit('toggle', 'ctrl')"
        @touchstart.prevent="emit('toggle', 'ctrl')"
      >
        Ctrl
      </button>
      <button
        class="rounded-full"
        :class="[keyCls, alt ? modOnCls : '']"
        @click="emit('toggle', 'alt')"
        @touchstart.prevent="emit('toggle', 'alt')"
      >
        Alt
      </button>
      <button
        class="rounded-full"
        :class="[keyCls, shift ? modOnCls : '']"
        @click="emit('toggle', 'shift')"
        @touchstart.prevent="emit('toggle', 'shift')"
      >
        Shift
      </button>
      <!-- Insert -->
      <button
        class="rounded-full"
        :class="keyCls"
        @click="send(K.insert)"
        @touchstart.prevent="send(K.insert)"
      >
        Ins
      </button>
    </div>

    <!-- 第二行：← | ↓ | →（倒 T 下方三键）+ 常用符号 -->
    <div class="flex items-center justify-around gap-1 flex-wrap">
      <button class="rounded-full" :class="keyCls" @mousedown="startRepeat(K.left)" @mouseup="stopRepeat" @mouseleave="stopRepeat" @touchstart.prevent="startRepeat(K.left)" @touchend="stopRepeat" @touchcancel="stopRepeat">←</button>
      <button class="rounded-full" :class="keyCls" @mousedown="startRepeat(K.down)" @mouseup="stopRepeat" @mouseleave="stopRepeat" @touchstart.prevent="startRepeat(K.down)" @touchend="stopRepeat" @touchcancel="stopRepeat">↓</button>
      <button class="rounded-full" :class="keyCls" @mousedown="startRepeat(K.right)" @mouseup="stopRepeat" @mouseleave="stopRepeat" @touchstart.prevent="startRepeat(K.right)" @touchend="stopRepeat" @touchcancel="stopRepeat">→</button>
      <!-- 常用符号：@click + @touchstart.prevent 双保险 -->
      <button class="rounded-full" :class="keyCls" @click="send(K.dot)" @touchstart.prevent="send(K.dot)">.</button>
      <button class="rounded-full" :class="keyCls" @click="send(K.slash)" @touchstart.prevent="send(K.slash)">/</button>
      <button class="rounded-full" :class="keyCls" @click="send(K.dash)" @touchstart.prevent="send(K.dash)">-</button>
      <button class="rounded-full" :class="keyCls" @click="send(K.eq)" @touchstart.prevent="send(K.eq)">=</button>
      <button class="rounded-full" :class="keyCls" @click="send(K.dquote)" @touchstart.prevent="send(K.dquote)">"</button>
    </div>
  </div>
</template>