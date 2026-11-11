import { http } from './http'

export interface IgAccount {
  id: number
  igUserId: string
  username: string
  name: string
  profilePic: string
  accountType: string
  tokenExpires: string | null
  tokenRefreshedAt: string | null
  connectedAt: string | null
  webhookSubscribed: boolean
  lastError: string
}

export interface SettingsInfo {
  account: IgAccount | null
  integration: {
    oauthReady: boolean
    appIdSet: boolean
    appSecretSet: boolean
    verifyToken: string
    webhookUrl: string
    redirectUri: string
    publicBaseUrl: string
    envTokenSet: boolean
    simulator: boolean
  }
  ai: { enabled: boolean; model: string }
  timezone: string
}

export const settingsApi = {
  get: () => http.get<SettingsInfo>('/settings').then((r) => r.data),
  // OAuth 要整頁導向（IG 授權頁不能在 XHR 裡開）
  connectUrl: '/api/ig/oauth/start',
  refresh: () => http.post<IgAccount>('/ig/account/refresh').then((r) => r.data),
  resubscribe: () => http.post<IgAccount>('/ig/account/subscribe').then((r) => r.data),
  disconnect: () => http.delete('/ig/account'),
  changePassword: (current: string, next: string) => http.post('/auth/password', { current, next }),
}
