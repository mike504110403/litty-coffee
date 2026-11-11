import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

export type ThemeMode = 'light' | 'dark'

// 與 index.html 的首繪腳本共用同一個 key
export const THEME_KEY = 'lc-theme'

// 只有淺色／深色；第一次預設淺色。舊版存過的 'system' 一律當淺色
function readStored(): ThemeMode {
  try {
    if (localStorage.getItem(THEME_KEY) === 'dark') return 'dark'
  } catch {
    // 隱私模式／被封鎖時讀不到，當作預設
  }
  return 'light'
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStored())
  const isDark = computed(() => mode.value === 'dark')

  watch(
    isDark,
    (dark) => {
      document.documentElement.classList.toggle('dark', dark)
    },
    { immediate: true },
  )

  function setMode(next: ThemeMode) {
    mode.value = next
    try {
      localStorage.setItem(THEME_KEY, next)
    } catch {
      // 寫不進去就只在本次有效
    }
  }

  return { mode, isDark, setMode }
})
