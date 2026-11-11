import { http } from './http'
import type { Risk, RiskSource } from './inbox'

export interface Tag {
  id: number
  name: string
  color: string
  count?: number
}

export interface CrmContact {
  id: number
  igAccountId: number // 傳訊給哪個我方 IG 帳號
  igsid: string
  username: string
  name: string
  avatarUrl: string
  note: string
  firstSeenAt: string
  lastSeenAt: string
  conversationId: number | null
  handler: string
  inboundCount: number
  lastIntent: string
  tags: Tag[]
  risk: Risk
  riskReason: string
  riskSource: RiskSource
  riskAt: string | null
  riskBy: string // 操作的管理員；自動標記為空
  throttleLevel: number // 降頻等級：0＝正常
}

export interface ContactDetail extends CrmContact {
  intents: { intent: string; count: number }[]
  outboundByAuto: number
  outboundByHuman: number
}

export interface Totals {
  inbound: number
  outboundAuto: number
  outboundHuman: number
  newContacts: number
  activeContacts: number
  humanConvs: number
}

export interface ResponseStats {
  turns: number
  answered: number
  avgSecs: number | null
  medianSecs: number | null
  autoAvgSecs: number | null
  humanAvgSecs: number | null
}

export interface Period {
  totals: Totals
  autoShare: number | null
  response: ResponseStats
}

export interface Summary {
  from: string
  to: string
  days: number
  current: Period
  previous: Period
  daily: { date: string; inbound: number; auto: number; human: number }[]
  intents: { key: string; count: number }[]
  hours: number[]
  topRules: { name: string; hitCount: number }[]
}

export const crmApi = {
  contacts: (params: { q?: string; tag?: number; sort?: string; account?: number; risk?: Risk } = {}) =>
    http.get<CrmContact[]>('/contacts', { params }).then((r) => r.data),
  contact: (id: number) => http.get<ContactDetail>(`/contacts/${id}`).then((r) => r.data),
  updateContact: (id: number, body: { note?: string; tagIds?: number[] }) =>
    http.patch<ContactDetail>(`/contacts/${id}`, body).then((r) => r.data),

  // 標籤全店共用；count 只算可見（或指定）帳號的顧客
  tags: (account?: number) => http.get<Tag[]>('/tags', { params: { account } }).then((r) => r.data),
  createTag: (name: string) => http.post<Tag>('/tags', { name }).then((r) => r.data),
  updateTag: (t: Tag) => http.put<Tag>(`/tags/${t.id}`, t).then((r) => r.data),
  deleteTag: (id: number) => http.delete(`/tags/${id}`),

  summary: (days: number, account?: number) =>
    http.get<Summary>('/analytics/summary', { params: { days, account } }).then((r) => r.data),
}
