import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import './style.css'
import '@xterm/xterm/css/xterm.css'

createApp(App).use(createPinia()).mount('#app')