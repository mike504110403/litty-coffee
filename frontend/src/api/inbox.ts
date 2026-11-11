import { http } from './http'

export interface Contact {
  id: number
  igsid: string
  username: string
  name: string
  avatarUrl: string
  risk: Risk // '' 一般｜watch 問題顧客｜blocked 黑名單
  riskReason: string
  riskSource: RiskSource
  riskAt: string | null
  throttleLevel: number // 降頻等級：0＝正常；N≥1＝自動回覆每 5×2^(N-1) 分鐘最多一次（上限 6 小時）
}

// 降頻等級對應的最短回覆間隔（分鐘）
export const throttleMinutes = (level: number) => (level <= 0 ? 0 : Math.min(5 * 2 ** (level - 1), 360))

// 顧客標記：blocked＝黑名單（訊息照收，但 AI 不回、不發轉真人通知）；watch＝問題顧客（AI 照常回）。
// 短時間大量訊息只會標 watch（來源 rate）並降低自動回覆頻率，不會封鎖。
export type Risk = '' | 'watch' | 'blocked'
export type RiskSource = '' | 'manual' | 'rate' | 'ai'
export const riskLabels: Record<Exclude<Risk, ''>, string> = { blocked: '黑名單', watch: '問題顧客' }
export const riskSourceLabels: Record<RiskSource, string> = {
  '': '',
  manual: '手動',
  rate: '自動（短時間大量訊息）',
  ai: '自動（AI 判斷騷擾）',
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
  igAccountId: number // 對話屬於哪個我方 IG 帳號（回覆也從這個帳號送出）
  contact: Contact
}

export type SenderType = 'contact' | 'bot' | 'ai' | 'human'

export interface Message {
  id: number
  conversationId: number
  igAccountId: number
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

// AI 建議回覆：草稿只放進輸入框，後端不會送出任何訊息
export interface Suggestion {
  text: string
  basis: string[] // 用到的知識庫條目標題
  warning?: string // 知識庫沒涵蓋或需店員判斷的地方
}

export const inboxApi = {
  list: (params: { status?: string; q?: string; account?: number } = {}) =>
    http.get<Conversation[]>('/conversations', { params }).then((r) => r.data),
  get: (id: number) => http.get<Conversation>(`/conversations/${id}`).then((r) => r.data),
  patch: (id: number, body: Partial<Pick<Conversation, 'handler' | 'status'>>) =>
    http.patch<Conversation>(`/conversations/${id}`, body).then((r) => r.data),
  markRead: (id: number) => http.post(`/conversations/${id}/read`),
  messages: (id: number, before?: number) =>
    http.get<Message[]>(`/conversations/${id}/messages`, { params: { before, limit: 50 } }).then((r) => r.data),
  send: (id: number, text: string) => http.post<Message>(`/conversations/${id}/messages`, { text }).then((r) => r.data),
  // 收件匣用的 AI 狀態（只需收件匣權限）：沒設 Claude key 就不顯示「AI 建議」
  aiStatus: () => http.get<{ configured: boolean }>('/inbox/ai').then((r) => r.data),
  // silent＝失敗（含 429、503）不跳錯誤提示、回 null；自動擬稿用，免得翻對話時一直跳提示
  suggest: (id: number, silent = false) =>
    http
      .post<Suggestion>(`/conversations/${id}/suggest`, undefined, {
        timeout: 70_000,
        ...(silent ? { validateStatus: () => true } : {}),
      })
      .then((r) => (r.status >= 200 && r.status < 300 ? r.data : null)),
}

export const devApi = {
  status: () => http.get<{ simulator: boolean }>('/dev/status').then((r) => r.data),
  simulate: (body: {
    account?: number // 收訊的我方帳號；省略＝第一個已連結的
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
