import { http } from './http'

// 操作紀錄：登入／登出／被踢／後台所有異動。需「操作紀錄」權限；依可見 IG 帳號過濾（不屬於帳號的紀錄都看得到）
export interface AuditLog {
  id: number
  adminId: number | null // 登入失敗且帳號不存在時為 null
  adminName: string // 當下的顯示名稱快照（登入失敗＝嘗試的帳號）
  action: string // 動作鍵，例：inbox.reply
  actionName: string // 中文名稱，例：回覆私訊
  summary: string
  targetType: string
  targetId: string
  targetLabel: string // 例：對話 #12；沒有目標為空
  igAccountId: number | null
  detail: Record<string, unknown> // method／path／route／params／query／body（機密已遮蔽、長字串已截斷）
  ip: string
  userAgent: string
  status: number // HTTP 狀態碼
  success: boolean
  createdAt: string
}

export interface AuditQuery {
  admin?: number
  action?: string
  from?: string // RFC3339
  to?: string // RFC3339（不含）
  account?: number
  page?: number
  pageSize?: number
}

export interface AuditAction {
  key: string
  name: string
}

export interface AuditAdmin {
  id: number
  username: string
  displayName: string
  disabled: boolean
}

export const auditApi = {
  list: (q: AuditQuery) =>
    http.get<{ items: AuditLog[]; total: number }>('/audit-logs', { params: q }).then((r) => r.data),
  actions: () => http.get<AuditAction[]>('/audit-logs/actions').then((r) => r.data),
  admins: () => http.get<AuditAdmin[]>('/audit-logs/admins').then((r) => r.data),
}
