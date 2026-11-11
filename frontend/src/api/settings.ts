import { http } from './http'
import type { IgAccount } from './accounts'

export type { IgAccount }

// 一次 Facebook 登入授權＝一個連線（業務組合的系統用戶 token），底下可有多個 IG 帳號
export interface IgConnection {
  id: number
  clientBusinessId: string
  systemUserId: string
  configId: string
  tokenExpires: string | null // null＝永不過期
  needsRelink: boolean // 已過期或快過期
  lastError: string
  connectedAt: string
}

export interface SettingsInfo {
  accounts: IgAccount[] // 登入者看得到的帳號
  connections: IgConnection[] // 只有可見全部帳號的人才拿得到
  manageConnections: boolean // 能不能連結／同步／斷開（限可見全部帳號）
  integration: {
    oauthReady: boolean
    webhookUrl: string
    redirectUri: string
    publicBaseUrl: string
    envTokenSet: boolean
    simulator: boolean
    permissions: string[] // 配置裡要勾的權限
    apiBase: string
  }
  timezone: string
}

export const settingsApi = {
  get: () => http.get<SettingsInfo>('/settings').then((r) => r.data),
  // OAuth 要整頁導向（Facebook 授權頁不能在 XHR 裡開）
  connectUrl: '/api/ig/oauth/start',
  refreshConnection: (id: number) =>
    http.post<IgConnection>(`/ig/connections/${id}/refresh`, null, { timeout: 60000 }).then((r) => r.data),
  disconnect: (id: number) => http.delete(`/ig/connections/${id}`),
  setActive: (id: number, active: boolean) => http.post<IgAccount>(`/ig/accounts/${id}/active`, { active }).then((r) => r.data),
  resubscribe: (id: number) => http.post<IgAccount>(`/ig/accounts/${id}/subscribe`).then((r) => r.data),
  changePassword: (current: string, next: string) => http.post('/auth/password', { current, next }),
}
