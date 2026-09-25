import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
// 终端字体（Maple Mono CN）只内置 **Regular 400** 一个字重：
// 终端字号范围 10–26px，Regular 是唯一必需的常规字重；粗体（\x1b[1m）交给浏览器合成，
// 不必再塞一个 ~9MB 的字重进来（要真粗体/中等，import 该包的 ./medium.css 即可）。
import '@automann/maple-mono-cn/regular.css'
import './style.css'
import '@xterm/xterm/css/xterm.css'

// 尽早开始加载终端字体：字体文件按 unicode-range 切片，这里带上中英样例文本，
// 让 latin 与首个 CJK 切片先在本机（unix socket）取回来。App 挂载、会话恢复、WS 握手
// 都要花时间，等终端面板真正 open() 时通常已就绪（TerminalPane 里另有一道等待与重测兜底）。
void document.fonts.load('16px "Maple Mono CN"', 'Aa中0')

createApp(App).use(createPinia()).mount('#app')
