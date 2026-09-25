// 底部辅助键条（KeypadBar）的实测高度 → <html> 的 CSS 变量 `--keypad-h`。
//
// 为什么需要：弹窗（快捷指令 / 新建终端 / 设置）要用「终端区域」定位——终端区域 =
// 壳层高（--vvh）减去顶栏（--tabbar-h）**再减去底部辅助键条**。顶栏由
// useViewportHeight() 实测；键条只在移动端布局下存在、且位于 TerminalPane 内部
// （每个标签一个实例，非激活面板用 visibility 隐藏），故由键条自己上报高度。
//
// 多实例处理：v-if="mobileLayout" 是所有实例共用的判据，通常同生共死；但标签关闭会
// 让个别实例先卸载。因此卸载后不直接写 0，而是 nextTick 重新同步一次——取任一「可见」
// 实例的高度，全都不可见（或都已卸载）时才归零。
const KEYPAD_H = '--keypad-h'

// 供 KeypadBar 挂载/尺寸变化/卸载时调用（也复用于测试与调试）
export function syncKeypadHeight(): void {
  const bars = document.querySelectorAll<HTMLElement>('[data-keypad-bar]')
  let h = 0
  for (const el of bars) {
    const box = el.getBoundingClientRect()
    if (box.height > 0) {
      h = box.height
      break
    }
  }
  document.documentElement.style.setProperty(KEYPAD_H, `${Math.round(h)}px`)
}
