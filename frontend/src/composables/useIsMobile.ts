import { onBeforeUnmount, ref } from 'vue'

/** 與版面斷點一致（768px） */
export function useIsMobile() {
  const mq = window.matchMedia('(max-width: 768px)')
  const isMobile = ref(mq.matches)
  const onChange = (e: MediaQueryListEvent) => (isMobile.value = e.matches)
  mq.addEventListener('change', onChange)
  onBeforeUnmount(() => mq.removeEventListener('change', onChange))
  return isMobile
}
