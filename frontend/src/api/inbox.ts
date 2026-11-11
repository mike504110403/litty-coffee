import { http } from './http'

export interface Contact {
  id: number
  igsid: string
  username: string
  name: string
  avatarUrl: string
}

export type Handler = 'bot' | 'human'
export type ConvStatus = 'open' | 'closed'

export interface Conversation {
  id: number
  status: ConvStatus
  handler: Handler
  unreadCount: number
  lastMessageAt: string | null
  lastInboundAt: string | null
  lastMessageText: string
  windowExpiresAt: string | null
  contactReadAt: string | null
  contact: Contact
}

export type SenderType = 'contact' | 'bot' | 'ai' | 'human'

export interface Message {
  id: number
  conversationId: number
  direction: 'in' | 'out'
  senderType: SenderType
  kind: string
  text: string
  mediaUrl: string // 已存檔時是 /api/inbox/media/…；否則是 IG 原始網址（可能已過期）
  reaction: string // 顧客按的表情
  deleted: boolean // 顧客已收回
  sentBy: { id: number; displayName: string } | null // 後台真人回覆的管理員；自動回覆、從 IG app 回的為 null
  createdAt: string
}

export const inboxApi = {
  list: (params: { status?: string; q?: string } = {}) =>
    http.get<Conversation[]>('/conversations', { params }).then((r) => r.data),
  get: (id: number) => http.get<Conversation>(`/conversations/${id}`).then((r) => r.data),
  patch: (id: number, body: Partial<Pick<Conversation, 'handler' | 'status'>>) =>
    http.patch<Conversation>(`/conversations/${id}`, body).then((r) => r.data),
  markRead: (id: number) => http.post(`/conversations/${id}/read`),
  messages: (id: number, before?: number) =>
    http.get<Message[]>(`/conversations/${id}/messages`, { params: { before, limit: 50 } }).then((r) => r.data),
  send: (id: number, text: string) => http.post<Message>(`/conversations/${id}/messages`, { text }).then((r) => r.data),
}

export const devApi = {
  status: () => http.get<{ simulator: boolean }>('/dev/status').then((r) => r.data),
  simulate: (body: {
    username: string
    name?: string
    text?: string
    image?: string
    action?: 'message' | 'read' | 'react' | 'unsend'
    emoji?: string
  }) =>
    http.post('/dev/simulate', body),
}

export const contactLabel = (c: Contact) => c.name || (c.username ? `@${c.username}` : '匿名顧客')
