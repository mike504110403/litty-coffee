import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { inboxApi, type Conversation, type Message } from '@/api/inbox'

const sortConvs = (a: Conversation, b: Conversation) =>
  (b.lastMessageAt ?? '').localeCompare(a.lastMessageAt ?? '')

export const useInboxStore = defineStore('inbox', () => {
  const conversations = ref<Conversation[]>([])
  const messages = ref<Record<number, Message[]>>({})
  const hasMore = ref<Record<number, boolean>>({})
  const activeId = ref<number | null>(null)
  const loading = ref(false)
  const connected = ref(false)
  let es: EventSource | null = null

  const totalUnread = computed(() => conversations.value.reduce((n, c) => n + c.unreadCount, 0))
  const active = computed(() => conversations.value.find((c) => c.id === activeId.value) ?? null)

  async function fetchConversations(q = '') {
    loading.value = true
    try {
      conversations.value = (await inboxApi.list({ q })).sort(sortConvs)
    } finally {
      loading.value = false
    }
  }

  function upsertConversation(c: Conversation) {
    const i = conversations.value.findIndex((x) => x.id === c.id)
    if (i >= 0) conversations.value[i] = c
    else conversations.value.push(c)
    conversations.value.sort(sortConvs)
  }

  function appendMessage(m: Message) {
    const list = messages.value[m.conversationId]
    if (!list || list.some((x) => x.id === m.id)) return
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

  async function send(convId: number, text: string) {
    appendMessage(await inboxApi.send(convId, text))
  }

  async function patch(convId: number, body: Parameters<typeof inboxApi.patch>[1]) {
    upsertConversation(await inboxApi.patch(convId, body))
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
    conversations, messages, hasMore, activeId, active, loading, connected, totalUnread,
    fetchConversations, open, loadOlder, markRead, send, patch, connect, disconnect,
  }
})
