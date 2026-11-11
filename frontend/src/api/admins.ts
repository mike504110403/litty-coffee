import { http } from './http'
import type { AdminUser, Perm } from '@/stores/auth'

export interface AdminCreate {
  username: string
  displayName: string
  password: string
  isOwner: boolean
  permissions: Perm[]
}

export interface AdminPatch {
  displayName?: string
  isOwner?: boolean
  permissions?: Perm[]
  disabled?: boolean
}

// 權限鍵的中文名稱與說明（鍵清單以後端回傳的 permissions 為準）
export const permMeta: Record<Perm, { label: string; desc: string }> = {
  inbox: { label: '收件匣', desc: '看私訊、即時更新、回覆顧客' },
  contacts: { label: '顧客', desc: '顧客資料與標籤' },
  automation: { label: '自動回覆', desc: '規則、知識庫、設定、沙盒' },
  schedule: { label: '排程', desc: '貼文排程、上傳媒體、AI 文案' },
  analytics: { label: '分析', desc: '數據分析' },
  ig_accounts: { label: 'IG 帳號', desc: '連結／斷開／續期 IG 帳號、串接資訊' },
  credentials: { label: '串接憑證', desc: 'IG App 與 Claude 憑證' },
  admins: { label: '管理員', desc: '新增、修改、停用管理員' },
}

export const adminsApi = {
  list: () => http.get<{ admins: AdminUser[]; permissions: Perm[] }>('/admins').then((r) => r.data),
  create: (in_: AdminCreate) => http.post<AdminUser>('/admins', in_).then((r) => r.data),
  update: (id: number, patch: AdminPatch) => http.patch<AdminUser>(`/admins/${id}`, patch).then((r) => r.data),
  resetPassword: (id: number, password: string) => http.post(`/admins/${id}/password`, { password }),
}
