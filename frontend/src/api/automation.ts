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
  spamMaxMsgs: number // 大量訊息門檻：spamWindowSecs 秒內超過幾則就自動列入黑名單；0＝關閉
  spamWindowSecs: number
  aiModeration: boolean // AI 判斷騷擾／惡意，是就自動列入黑名單
}

export interface AIDecision {
  action: 'reply' | 'handoff'
  reply: string
  intent: string
  reason: string
  abusive?: boolean
  abuseReason?: string
}

export interface Outcome {
  kind: 'rule' | 'ai' | 'handoff' | 'block' | 'none'
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

// 設定、規則、知識庫都依 IG 帳號區分：除了 status，每個呼叫都帶 ?account=
const acc = (account: number) => ({ params: { account } })

export const automationApi = {
  status: () => http.get<{ aiConfigured: boolean; model: string }>('/automation/status').then((r) => r.data),
  settings: (account: number) => http.get<AutoSettings>('/automation/settings', acc(account)).then((r) => r.data),
  saveSettings: (account: number, s: AutoSettings) =>
    http.put<AutoSettings>('/automation/settings', s, acc(account)).then((r) => r.data),

  rules: (account: number) => http.get<Rule[]>('/automation/rules', acc(account)).then((r) => r.data),
  createRule: (account: number, r: Omit<Rule, 'id' | 'hitCount' | 'lastHitAt'>) =>
    http.post<Rule>('/automation/rules', r, acc(account)).then((res) => res.data),
  updateRule: (account: number, r: Rule) =>
    http.put<Rule>(`/automation/rules/${r.id}`, r, acc(account)).then((res) => res.data),
  deleteRule: (account: number, id: number) => http.delete(`/automation/rules/${id}`, acc(account)),

  knowledge: (account: number) => http.get<Knowledge[]>('/automation/knowledge', acc(account)).then((r) => r.data),
  saveKnowledge: (account: number, k: Partial<Knowledge> & { title: string }) =>
    (k.id
      ? http.put<Knowledge>(`/automation/knowledge/${k.id}`, k, acc(account))
      : http.post<Knowledge>('/automation/knowledge', k, acc(account))
    ).then((r) => r.data),
  deleteKnowledge: (account: number, id: number) => http.delete(`/automation/knowledge/${id}`, acc(account)),

  test: (account: number, text: string, history: { fromCustomer: boolean; text: string }[] = []) =>
    http
      .post<{ outcome: Outcome; elapsedMs: number }>(
        '/automation/test',
        { text, history },
        { timeout: 60000, params: { account } },
      )
      .then((r) => r.data),
}
