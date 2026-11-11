import { http } from './http'

// 我方的 IG 專業帳號（ig_accounts）；後端只回登入者「可見」的帳號
export interface IgAccount {
  id: number
  igUserId: string
  username: string
  name: string
  profilePic: string
  active: boolean // false＝停用：不收不送，資料保留
  connectionId: number | null
  connected: boolean // 有掛在連線（token）上
  webhookSubscribed: boolean
  lastError: string
  connectedAt: string | null
}

export const accountsApi = {
  list: () => http.get<IgAccount[]>('/ig/accounts').then((r) => r.data),
}

export const accountLabel = (a: Pick<IgAccount, 'username' | 'name' | 'igUserId'>) =>
  a.username ? `@${a.username}` : a.name || a.igUserId
