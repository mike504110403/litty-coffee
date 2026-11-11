// 會自己重連的 SSE。瀏覽器原生 EventSource 遇到非 200 回應（後端重啟時 nginx 回 502、Cloudflare 回錯誤頁）
// 會直接放棄、不再重連，畫面就停在舊資料。這裡在連線被關掉時依退避時間重開，
// 分頁回到前景或網路恢復時立刻重連；每次重新連上都呼叫 onReopen 讓呼叫端補抓斷線期間漏掉的資料。
export interface ResilientSSE {
  close: () => void
}

const BACKOFF = [1000, 2000, 5000, 10_000, 30_000]

export function resilientSSE(
  url: string,
  opts: {
    listen: (es: EventSource) => void // 註冊事件（每次重開都會再呼叫）
    onState?: (connected: boolean) => void
    onReopen?: () => void // 第二次以後連上時呼叫
  },
): ResilientSSE {
  let es: EventSource | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let attempt = 0
  let everOpened = false
  let closed = false

  function open() {
    if (closed) return
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
    es?.close()
    es = new EventSource(url, { withCredentials: true })
    es.onopen = () => {
      attempt = 0
      opts.onState?.(true)
      if (everOpened) opts.onReopen?.()
      everOpened = true
    }
    es.onerror = () => {
      opts.onState?.(false)
      // CONNECTING＝瀏覽器自己會重試；CLOSED＝已放棄，要自己排重連
      if (es?.readyState === EventSource.CLOSED) schedule()
    }
    opts.listen(es)
  }

  function schedule() {
    if (closed || timer) return
    const wait = BACKOFF[Math.min(attempt, BACKOFF.length - 1)]
    attempt++
    timer = setTimeout(() => {
      timer = null
      open()
    }, wait)
  }

  // 分頁回到前景、網路恢復：連線不在就立刻重開（手機切 app 回來最常見）
  function wake() {
    if (closed || document.visibilityState !== 'visible') return
    if (!es || es.readyState === EventSource.CLOSED) {
      attempt = 0
      open()
    }
  }
  document.addEventListener('visibilitychange', wake)
  window.addEventListener('online', wake)
  window.addEventListener('pageshow', wake)

  open()

  return {
    close() {
      closed = true
      if (timer) clearTimeout(timer)
      es?.close()
      es = null
      document.removeEventListener('visibilitychange', wake)
      window.removeEventListener('online', wake)
      window.removeEventListener('pageshow', wake)
      opts.onState?.(false)
    },
  }
}
