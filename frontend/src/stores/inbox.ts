import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { inboxApi, type Conversation, type Message } from '@/api/inbox'
import type { RiskResult } from '@/api/flags'

const sortConvs = (a: Conversation, b: Conversation) =>
  (b.lastMessageAt ?? '').localeCompare(a.lastMessageAt ?? '')

export const useInboxStore = defineStore('inbox', () => {
  const conversations = ref<Conversation[]>([])
  const messages = ref<Record<number, Message[]>>({})
  const hasMore = ref<Record<number, boolean>>({})
  const activeId = ref<number | null>(null)
  const loading = ref(false)
  const connected = ref(false)
  const accountFilter = ref<number | null>(null) // null＝全部帳號
  const query = ref('')
  let es: EventSource | null = null

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
    if (i >= 0) list[i] = m
    else list.push(m)
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

  async function send(convId: number, text: string) {
    appendMessage(await inboxApi.send(convId, text))
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
    if (es) return
    es = new EventSource('/api/inbox/stream', { withCredentials: true })
    es.onopen = () => {
      // 重連後補抓斷線期間漏掉的
      if (!connected.value) {
        void fetchConversations()
        if (activeId.value != null) void loadMessages(activeId.value)
      }
      connected.value = true
    }
    es.onerror = () => {
      connected.value = false
    }
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
  }

  function disconnect() {
    es?.close()
    es = null
    connected.value = false
  }

  return {
    conversations, messages, hasMore, activeId, active, loading, connected, totalUnread, accountFilter,
    fetchConversations, setAccountFilter, open, loadOlder, markRead, send, patch, applyRisk, connect, disconnect, onHandoff,
  }
})
