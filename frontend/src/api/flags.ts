import { http } from './http'
import type { Risk, RiskSource } from './inbox'

// 待改進問答：ai_wrong＝管理員標「回答有誤」；ai_unsure＝AI 轉真人或拒答時自動記錄
export type ReviewKind = 'ai_wrong' | 'ai_unsure'

export interface Review {
  id: number
  kind: ReviewKind
  igAccountId: number
  messageId: number
  conversationId: number
  contactId: number
  contactName: string
  question: string // 顧客的問題
  reply: string // AI／規則的回覆（ai_wrong 才有）
  replySender: '' | 'ai' | 'bot'
  ruleName: string
  intent: string
  note: string // ai_wrong＝管理員說明；ai_unsure＝AI 的理由
  createdBy: string
  createdAt: string
  resolvedAt: string | null
  resolvedBy: string
}

export const reviewKindLabels: Record<ReviewKind, string> = { ai_wrong: '回答有誤', ai_unsure: 'AI 沒把握' }

export interface RiskResult {
  id: number
  risk: Risk
  riskReason: string
  riskSource: RiskSource
  riskAt: string | null
  riskBy: string
}

export const flagsApi = {
  // 顧客標記：收件匣或顧客權限都能改（只有收件匣權限時回應只含標記欄位）
  setRisk: (contactId: number, risk: Risk, riskReason = '') =>
    http.patch<RiskResult>(`/contacts/${contactId}`, { risk, riskReason }).then((r) => r.data),
  // 收件匣：把自動回覆標「回答有誤」
  markWrong: (messageId: number, note: string) =>
    http.post<Review>(`/messages/${messageId}/reviews`, { note }).then((r) => r.data),
  // 自動回覆權限
  reviews: (params: { account?: number; kind?: ReviewKind; status?: 'open' | 'resolved' } = {}) =>
    http.get<Review[]>('/reviews', { params }).then((r) => r.data),
  openCount: (account?: number) =>
    http.get<{ count: number }>('/reviews/open-count', { params: { account } }).then((r) => r.data.count),
  resolve: (id: number) => http.post<Review>(`/reviews/${id}/resolve`).then((r) => r.data),
}
