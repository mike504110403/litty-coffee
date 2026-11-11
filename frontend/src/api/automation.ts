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
  autoReplyEnabled: boolean // 自動回覆總開關：關閉＝只收不回（不跑規則、AI、轉真人通知）
  digestTime: string // 每日摘要產生時間（店家時區 HH:MM）
  distillWeekly: boolean // 每週一 09:00 自動蒸餾近 7 天對話成建議（不會自動生效）
  postsKbEnabled: boolean // 自動回覆／建議／寫文案參考近期 IG 貼文文案
  humanIdleMinutes: number // 真人多久沒回就交回 AI；只接受 0（關閉）、5、10、15、30、60
}

export const HUMAN_IDLE_OPTIONS = [0, 5, 10, 15, 30, 60] as const

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
  // 只切單一欄位（其他設定不動）
  patchSettings: (account: number, patch: Partial<AutoSettings>) =>
    http.put<AutoSettings>('/automation/settings', patch, acc(account)).then((r) => r.data),
  // 只切總開關（其他設定不動）
  setAutoReply: (account: number, enabled: boolean) =>
    http.put<AutoSettings>('/automation/settings', { autoReplyEnabled: enabled }, acc(account)).then((r) => r.data),

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

// ---- 蒸餾：從對話整理「建議的」規則／知識庫，店家審核後才生效 ----

export type DistillDays = 7 | 30 | 90

export interface DistillRuleDraft {
  name: string
  keywords: string[]
  matchType: MatchType
  reply: string
  handoff: boolean
}

export interface DistillKnowledgeDraft {
  title: string
  content: string
}

export interface DistillSuggestion {
  type: 'rule' | 'knowledge'
  action: 'create' | 'update'
  targetId: number
  rule?: DistillRuleDraft
  knowledge?: DistillKnowledgeDraft
  needsInput: boolean
  reason: string
  evidence: string[]
  confidence: 'high' | 'medium' | 'low'
  beforeRule?: DistillRuleDraft
  beforeKnowledge?: DistillKnowledgeDraft
  edited?: boolean
}

export interface DistillItem {
  id: number
  runId: number
  igAccountId: number
  status: 'pending' | 'accepted' | 'dismissed'
  suggestion: DistillSuggestion
  resultId: number | null
  decidedBy: string
  decidedAt: string | null
  createdAt: string
}

export interface DistillRun {
  id: number
  igAccountId: number
  days: DistillDays
  trigger: 'manual' | 'weekly'
  status: 'running' | 'done' | 'failed' | 'empty'
  error: string
  pairCount: number
  reviewCount: number
  inputChars: number
  suggestionCount: number
  createdBy: string
  createdAt: string
  finishedAt: string | null
}

export interface DistillEdit {
  rule?: DistillRuleDraft
  knowledge?: DistillKnowledgeDraft
}

export const confidenceLabels: Record<DistillSuggestion['confidence'], string> = {
  high: '把握高',
  medium: '把握中',
  low: '把握低',
}

export const distillApi = {
  // 同步跑一次（最多約 2 分鐘）
  run: (account: number, days: DistillDays) =>
    http
      .post<{ run: DistillRun }>('/automation/distill', null, { params: { account, days }, timeout: 150000 })
      .then((r) => r.data.run),
  runs: (account: number) => http.get<DistillRun[]>('/automation/distill/runs', acc(account)).then((r) => r.data),
  suggestions: (account: number, status: DistillItem['status'] | '' = 'pending') =>
    http
      .get<DistillItem[]>('/automation/distill/suggestions', { params: { account, status } })
      .then((r) => r.data),
  accept: (id: number, edit?: DistillEdit) =>
    http.post<DistillItem>(`/automation/distill/suggestions/${id}/accept`, edit ?? {}).then((r) => r.data),
  dismiss: (id: number) => http.post<DistillItem>(`/automation/distill/suggestions/${id}/dismiss`).then((r) => r.data),
}
