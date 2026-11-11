import { http } from './http'

// 系統狀態（總覽頁上方）：後端每分鐘檢查一次；只回登入者看得到的帳號
export type LightStatus = 'green' | 'yellow' | 'red'

export interface Light {
  status: LightStatus
  message: string
  checkedAt: string
}

export interface AccountStatus extends Light {
  id: number
  igUserId: string
  username: string
  name: string
  profilePic: string
  active: boolean
  connection: Light
  latencyMs: number | null
  webhook: Light
  subscribed: boolean
  lastWebhookAt: string | null
  autoReplyEnabled: boolean
  posts: { failed: number; overdue: number; overdueSecs: number; nextAt: string | null }
}

export interface CallRecord {
  kind: 'reply' | 'digest'
  at: string
  ok: boolean
  error?: string
  durationMs: number
}

export interface SystemStatus {
  checkedAt: string
  meta: Light
  accounts: AccountStatus[]
  claude: Light & {
    configured: boolean
    keyValid: boolean | null
    keyCheckedAt: string | null
    keyError?: string
    lastCall: CallRecord | null
    consecutiveFails: number
  }
  jobs: Light & {
    pending: number
    oldestAt: string | null
    failedPosts: number
    overduePosts: number
    nextPostAt: string | null
  }
  db: Light & { latencyMs: number }
  canToggleAutoReply: boolean
}

// system.alert 推送內容
export interface SystemAlert {
  key: string
  kind: 'account' | 'posts' | 'claude' | 'queue' | 'db'
  igAccountId?: number
  title: string
  status: LightStatus
  message: string
  recovered: boolean
  at: string
}

// ---- 每日摘要 ----
export interface DigestTotals {
  inbound: number
  outboundAuto: number
  outboundHuman: number
  newContacts: number
  activeContacts: number
  humanConvs: number
}

export interface DigestTodo {
  conversationId: number
  contact: string
  score: number
  reasons: string[]
  lastText: string
  lastInboundAt: string | null
}

export interface Digest {
  id: number
  igAccountId: number
  date: string // YYYY-MM-DD（店家時區）
  generatedAt: string
  content: {
    date: string
    totals: DigestTotals
    previous: DigestTotals
    autoShare: number | null
    prevAutoShare: number | null
    medianSecs: number | null
    intents: { key: string; count: number }[]
    todo: DigestTodo[]
    todoTotal: number
    summary: string
    summaryStatus: 'ok' | 'no_key' | 'empty' | 'failed'
    summaryError?: string
  }
}

// distill.ready：每週自動蒸餾完成（有自動回覆權限、看得到該帳號者）
export interface DistillReady {
  igAccountId: number
  runId: number
  suggestionCount: number
}

export interface DigestReady {
  igAccountId: number
  date: string
  todoCount: number
}

export const systemApi = {
  status: () => http.get<SystemStatus>('/system/status').then((r) => r.data),
}

export const digestApi = {
  latest: (account: number) => http.get<Digest | null>('/digests/latest', { params: { account } }).then((r) => r.data),
  // 有 AI 總結時要等模型回覆，放寬逾時
  generate: (account: number, date?: string) =>
    http.post<Digest>('/digests/generate', null, { params: { account, date }, timeout: 90000 }).then((r) => r.data),
}
