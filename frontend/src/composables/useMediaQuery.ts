import { onBeforeUnmount, ref } from 'vue'

/** 任意 media query 的響應式布林（例如 '(min-width: 1280px)'） */
export function useMediaQuery(query: string) {
  const mq = window.matchMedia(query)
  const matches = ref(mq.matches)
  const onChange = (e: MediaQueryListEvent) => (matches.value = e.matches)
  mq.addEventListener('change', onChange)
  onBeforeUnmount(() => mq.removeEventListener('change', onChange))
  return matches
}
