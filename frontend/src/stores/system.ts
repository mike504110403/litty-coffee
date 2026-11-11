import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { DigestReady, DistillReady, SystemAlert } from '@/api/system'

// 系統通知串流（/api/system/stream）：system.alert（燈號轉紅／恢復）、digest.ready（每日摘要產生）、distill.ready（每週自動蒸餾完成）。
// 與收件匣串流分開，不需收件匣權限；後端依權限與可見帳號過濾。
export const useSystemStore = defineStore('system', () => {
  const connected = ref(false)
  let es: EventSource | null = null

  type AlertHandler = (a: SystemAlert) => void
  type DigestHandler = (d: DigestReady) => void
  const alertHandlers = new Set<AlertHandler>()
  const digestHandlers = new Set<DigestHandler>()
  type DistillHandler = (d: DistillReady) => void
  const distillHandlers = new Set<DistillHandler>()

  function onAlert(fn: AlertHandler) {
    alertHandlers.add(fn)
    return () => alertHandlers.delete(fn)
  }
  function onDigest(fn: DigestHandler) {
    digestHandlers.add(fn)
    return () => digestHandlers.delete(fn)
  }

  function onDistill(fn: DistillHandler) {
    distillHandlers.add(fn)
    return () => distillHandlers.delete(fn)
  }

  function connect() {
    if (es) return
    es = new EventSource('/api/system/stream', { withCredentials: true })
    es.onopen = () => {
      connected.value = true
    }
    es.onerror = () => {
      connected.value = false
    }
    es.addEventListener('system.alert', (e) => {
      const a = JSON.parse((e as MessageEvent).data) as SystemAlert
      alertHandlers.forEach((fn) => fn(a))
    })
    es.addEventListener('digest.ready', (e) => {
      const d = JSON.parse((e as MessageEvent).data) as DigestReady
      digestHandlers.forEach((fn) => fn(d))
    })
    es.addEventListener('distill.ready', (e) => {
      const d = JSON.parse((e as MessageEvent).data) as DistillReady
      distillHandlers.forEach((fn) => fn(d))
    })
  }

  function disconnect() {
    es?.close()
    es = null
    connected.value = false
  }

  return { connected, connect, disconnect, onAlert, onDigest, onDistill }
})
