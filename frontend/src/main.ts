import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import zhTw from 'element-plus/es/locale/lang/zh-tw'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import '@/styles/theme.scss'
import App from './App.vue'
import router from './router'
import { useThemeStore } from '@/stores/theme'

const app = createApp(App).use(createPinia()).use(router).use(ElementPlus, { locale: zhTw })
useThemeStore() // 先建立，讓「跟隨系統」的監聽在掛載前就生效
app.mount('#app')
