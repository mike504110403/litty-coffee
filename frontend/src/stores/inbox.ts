import { defineStore } from 'pinia'
import { resilientSSE, type ResilientSSE } from '@/utils/sse'
import { computed, ref } from 'vue'
import { inboxApi, type Conversation, type Message } from '@/api/inbox'
import type { RiskResult } from '@/api/flags'

const sortConvs = (a: Conversation, b: Conversation) =>
  (b.lastMessageAt ?? '').localeCompare(a.lastMessageAt ?? '')

// 樂觀送出：按下送出就先在對話底部出現的「暫時訊息」（id 為負數，與伺服器 id 不會相撞）
export interface PendingMessage {
  tmpId: number
  conversationId: number
  text: string
  status: 'queued' | 'sending' | 'failed' // 等待中（前一則還沒送完）／傳送中／傳送失敗
  createdAt: string
}

export const useInboxStore = defineStore('inbox', () => {
  const conversations = ref<Conversation[]>([])
  const messages = ref<Record<number, Message[]>>({})
  const hasMore = ref<Record<number, boolean>>({})
  const activeId = ref<number | null>(null)
  const loading = ref(false)
  const connected = ref(false)
  const accountFilter = ref<number | null>(null) // null＝全部帳號
  const query = ref('')
  const pending = ref<Record<number, PendingMessage[]>>({}) // 每個對話各自一條送出佇列
  const pumping = new Set<number>()
  let tmpSeq = 0
  let sse: ResilientSSE | null = null

  const totalUnread = computed(() => conversations.value.reduce((n, c) => n + c.unreadCount, 0))
  const active = computed(() => conversations.value.find((c) => c.id === activeId.value) ?? null)

  async function fetchConversations(q?: string) {
    if (q !== undefined) query.value = q
    loading.value = true
    try {
      conversations.value = (
        await inboxApi.list({ q: query.value, account: accountFilter.value ?? undefined })
      ).sort(sortConvs)
    } finally {
      loading.value = false
    }
  }

  function setAccountFilter(id: number | null) {
    accountFilter.value = id
    void fetchConversations()
  }

  function upsertConversation(c: Conversation) {
    const i = conversations.value.findIndex((x) => x.id === c.id)
    if (i >= 0) conversations.value[i] = c
    else if (accountFilter.value == null || c.igAccountId === accountFilter.value || c.id === activeId.value)
      conversations.value.push(c)
    conversations.value.sort(sortConvs)
  }

  function appendMessage(m: Message) {
    const list = messages.value[m.conversationId]
    if (!list) return
    // 同一筆再次推送（例：echo 先到、後台送出後認領成自己的）→ 就地取代，送出者名字才會更新
    const i = list.findIndex((x) => x.id === m.id)
    if (i >= 0) {
      list[i] = m
      return
    }
    // 自己正在送的那則，SSE 的 message.created 可能比 HTTP 回應先到：以文字認領，避免畫面短暫出現兩則
    if (m.direction === 'out' && m.senderType === 'human') {
      const q = pending.value[m.conversationId]
      const j = q?.findIndex((p) => p.status === 'sending' && p.text === m.text) ?? -1
      if (q && j >= 0) q.splice(j, 1)
    }
    list.push(m)
  }

  function replaceMessage(m: Message) {
    const list = messages.value[m.conversationId]
    const i = list?.findIndex((x) => x.id === m.id) ?? -1
    if (list && i >= 0) list[i] = m
  }

  async function loadMessages(convId: number) {
    const list = await inboxApi.messages(convId)
    messages.value[convId] = list
    hasMore.value[convId] = list.length === 50
  }

  async function loadOlder(convId: number) {
    const list = messages.value[convId]
    if (!list?.length || !hasMore.value[convId]) return
    const older = await inboxApi.messages(convId, list[0].id)
    messages.value[convId] = [...older, ...list]
    hasMore.value[convId] = older.length === 50
  }

  async function open(convId: number | null) {
    activeId.value = convId
    if (convId == null) return
    if (!conversations.value.some((c) => c.id === convId)) {
      upsertConversation(await inboxApi.get(convId))
    }
    await loadMessages(convId)
    await markRead(convId)
  }

  async function markRead(convId: number) {
    const c = conversations.value.find((x) => x.id === convId)
    if (!c || c.unreadCount === 0) return
    c.unreadCount = 0
    await inboxApi.markRead(convId)
  }

  // 送出：訊息立刻進佇列（畫面馬上看到「傳送中」），背景依序呼叫 API；前一則失敗就停在那裡，後面的維持「等待中」
  function send(convId: number, text: string) {
    const q = (pending.value[convId] ??= [])
    q.push({ tmpId: --tmpSeq, conversationId: convId, text, status: 'queued', createdAt: new Date().toISOString() })
    void pump(convId)
  }

  async function pump(convId: number) {
    if (pumping.has(convId)) return
    pumping.add(convId)
    try {
      for (;;) {
        const q = pending.value[convId]
        const next = q?.find((p) => p.status === 'queued')
        if (!q || !next || q.some((p) => p.status === 'failed')) break // 前面有失敗的就停著，等使用者處理
        next.status = 'sending'
        try {
          const m = await inboxApi.send(convId, next.text)
          const k = q.findIndex((p) => p.tmpId === next.tmpId)
          if (k >= 0) q.splice(k, 1) // 已被 SSE 認領走就沒有這則了
          appendMessage(m) // 以伺服器 id 去重
        } catch {
          next.status = 'failed' // 錯誤提示由 http 攔截器負責；內容保留，等使用者重送或刪除
          break
        }
      }
    } finally {
      pumping.delete(convId)
    }
  }

  function retrySend(convId: number, tmpId: number) {
    const p = pending.value[convId]?.find((x) => x.tmpId === tmpId)
    if (!p || p.status !== 'failed') return
    p.status = 'queued'
    void pump(convId)
  }

  function discardPending(convId: number, tmpId: number) {
    const q = pending.value[convId]
    const k = q?.findIndex((x) => x.tmpId === tmpId) ?? -1
    if (q && k >= 0 && q[k].status !== 'sending') q.splice(k, 1)
    void pump(convId) // 刪掉失敗的那則後，後面等待中的繼續送
  }

  async function patch(convId: number, body: Parameters<typeof inboxApi.patch>[1]) {
    upsertConversation(await inboxApi.patch(convId, body))
  }

  // 顧客標記改了：先就地更新（SSE 的 conversation.updated 也會帶來同樣的值）
  function applyRisk(r: RiskResult) {
    for (const c of conversations.value) {
      if (c.contact.id !== r.id) continue
      c.contact = { ...c.contact, risk: r.risk, riskReason: r.riskReason, riskSource: r.riskSource, riskAt: r.riskAt }
    }
  }

  // 轉真人通知：AppLayout 註冊處理函式（跳通知、點了打開對話）
  type HandoffHandler = (e: { conversation: Conversation; reason: string }) => void
  const handoffHandlers = new Set<HandoffHandler>()
  function onHandoff(fn: HandoffHandler) {
    handoffHandlers.add(fn)
    return () => handoffHandlers.delete(fn)
  }

  function connect() {
    if (sse) return
    sse = resilientSSE('/api/inbox/stream', {
      onState: (v) => (connected.value = v),
      // 重連後補抓斷線期間漏掉的
      onReopen: () => {
        void fetchConversations()
        if (activeId.value != null) void loadMessages(activeId.value)
      },
      listen: (es) => {
        es.addEventListener('message.created', (e) => {
          const m = JSON.parse((e as MessageEvent).data) as Message
          appendMessage(m)
          if (m.direction === 'in' && m.conversationId === activeId.value && document.visibilityState === 'visible') {
            void inboxApi.markRead(m.conversationId)
          }
        })
        es.addEventListener('message.updated', (e) => {
          replaceMessage(JSON.parse((e as MessageEvent).data) as Message)
        })
        es.addEventListener('handoff.requested', (e) => {
          const ev = JSON.parse((e as MessageEvent).data) as { conversation: Conversation; reason: string }
          upsertConversation(ev.conversation)
          handoffHandlers.forEach((fn) => fn(ev))
        })
        es.addEventListener('conversation.updated', (e) => {
          const c = JSON.parse((e as MessageEvent).data) as Conversation
          if (c.id === activeId.value && document.visibilityState === 'visible') c.unreadCount = 0
          upsertConversation(c)
        })
      },
    })
  }

  function disconnect() {
    sse?.close()
    sse = null
    connected.value = false
  }

  return {
    conversations, messages, hasMore, pending, activeId, active, loading, connected, totalUnread, accountFilter,
    fetchConversations, setAccountFilter, open, loadOlder, markRead, send, retrySend, discardPending, patch, applyRisk, connect, disconnect, onHandoff,
  }
})
