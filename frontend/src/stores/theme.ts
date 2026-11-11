import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

export type ThemeMode = 'system' | 'light' | 'dark'

// 與 index.html 的首繪腳本共用同一個 key
export const THEME_KEY = 'lc-theme'
const MODES: ThemeMode[] = ['system', 'light', 'dark']

function readStored(): ThemeMode {
  try {
    const v = localStorage.getItem(THEME_KEY)
    if (v && (MODES as string[]).includes(v)) return v as ThemeMode
  } catch {
    // 隱私模式／被封鎖時讀不到，當作預設
  }
  return 'system'
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStored())
  const query = typeof window !== 'undefined' && window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null
  const systemDark = ref(query?.matches ?? false)
  // 「跟隨系統」時，系統外觀一變就更新
  query?.addEventListener('change', (e) => {
    systemDark.value = e.matches
  })

  const isDark = computed(() => (mode.value === 'system' ? systemDark.value : mode.value === 'dark'))

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
