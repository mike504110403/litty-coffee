import { http } from './http'

export type MatchType = 'exact' | 'contains' | 'regex'

export interface Rule {
  id: number
  name: string
  matchType: MatchType
  keywords: string[]
  reply: string
  handoff: boolean
  enabled: boolean
  priority: number
  hitCount: number
  lastHitAt: string | null
}

export interface Knowledge {
  id: number
  title: string
  content: string
  enabled: boolean
  sort: number
  updatedAt: string
}

export interface AutoSettings {
  aiEnabled: boolean
  persona: string
  handoffMessage: string
  debounceSecs: number
}

export interface AIDecision {
  action: 'reply' | 'handoff'
  reply: string
  intent: string
  reason: string
}

export interface Outcome {
  kind: 'rule' | 'ai' | 'handoff' | 'none'
  rule?: Rule
  ai?: AIDecision
  reply: string
  detail?: string
}

export const intentLabels: Record<string, string> = {
  hours: '營業資訊',
  menu: '菜單價格',
  reservation: '訂位',
  location: '交通位置',
  promotion: '活動優惠',
  complaint: '客訴',
  business: '合作洽談',
  chitchat: '閒聊',
  other: '其他',
}

export const matchTypeLabels: Record<MatchType, string> = {
  contains: '包含',
  exact: '完全相同',
  regex: '正則',
}

export const automationApi = {
  status: () => http.get<{ aiConfigured: boolean; model: string }>('/automation/status').then((r) => r.data),
  settings: () => http.get<AutoSettings>('/automation/settings').then((r) => r.data),
  saveSettings: (s: AutoSettings) => http.put<AutoSettings>('/automation/settings', s).then((r) => r.data),

  rules: () => http.get<Rule[]>('/automation/rules').then((r) => r.data),
  createRule: (r: Omit<Rule, 'id' | 'hitCount' | 'lastHitAt'>) =>
    http.post<Rule>('/automation/rules', r).then((res) => res.data),
  updateRule: (r: Rule) => http.put<Rule>(`/automation/rules/${r.id}`, r).then((res) => res.data),
  deleteRule: (id: number) => http.delete(`/automation/rules/${id}`),

  knowledge: () => http.get<Knowledge[]>('/automation/knowledge').then((r) => r.data),
  saveKnowledge: (k: Partial<Knowledge> & { title: string }) =>
    (k.id ? http.put<Knowledge>(`/automation/knowledge/${k.id}`, k) : http.post<Knowledge>('/automation/knowledge', k)).then(
      (r) => r.data,
    ),
  deleteKnowledge: (id: number) => http.delete(`/automation/knowledge/${id}`),

  test: (text: string, history: { fromCustomer: boolean; text: string }[] = []) =>
    http
      .post<{ outcome: Outcome; elapsedMs: number }>('/automation/test', { text, history }, { timeout: 60000 })
      .then((r) => r.data),
}
