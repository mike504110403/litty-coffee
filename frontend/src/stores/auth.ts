import { defineStore } from 'pinia'
import { ref } from 'vue'
import { http } from '@/api/http'
import { useAccountsStore } from './accounts'

// 功能權限鍵；後端（auth.AllPerms）是唯一來源，這裡只是型別。
// ig_accounts、credentials 是超管專屬（不可授予），can() 只對超管回 true
export type Perm =
  | 'inbox'
  | 'contacts'
  | 'automation'
  | 'schedule'
  | 'analytics'
  | 'ig_accounts'
  | 'credentials'
  | 'admins'
  | 'audit'

export interface AdminUser {
  id: number
  username: string
  displayName: string
  isOwner: boolean
  isSuper: boolean // 超級管理員：位階在店長之上，IG 帳號連結與憑證只限超管
  permissions: Perm[] // 有效權限：店長、超管是完整清單（可授予的）
  disabled: boolean
  lastLoginAt: string | null
  lastSeenAt: string | null // 最後活動（每分鐘最多更新一次）
  online: boolean // 2 分鐘內有活動且登入中
  createdAt: string
  mustChangePassword: boolean
  totpEnabled: boolean // 已綁 Google Authenticator（登入要驗證碼）
  setupRequired: boolean // 首次登入鎖定：只能改密碼、綁 GA、登出（/setup）
}

const superOnly: Perm[] = ['ig_accounts', 'credentials']

// 登入第一步的結果：已綁 GA 時要再輸入驗證碼
export type LoginResult = { mfaRequired: true } | { mfaRequired: false; user: AdminUser }

export const useAuthStore = defineStore('auth', () => {
  const user = ref<AdminUser | null>(null)
  const checked = ref(false)

  async function fetchMe(): Promise<AdminUser | null> {
    try {
      const { data } = await http.get<AdminUser>('/auth/me')
      user.value = data
    } catch {
      user.value = null
    } finally {
      checked.value = true
    }
    return user.value
  }

  function setUser(u: AdminUser) {
    useAccountsStore().reset() // 可見帳號依登入者而異
    user.value = u
    checked.value = true
  }

  async function login(username: string, password: string): Promise<LoginResult> {
    const { data } = await http.post<AdminUser | { mfaRequired: true }>('/auth/login', { username, password })
    if ('mfaRequired' in data && data.mfaRequired) return { mfaRequired: true }
    setUser(data as AdminUser)
    return { mfaRequired: false, user: data as AdminUser }
  }

  // 第二步：6 位數驗證碼或備援碼（待驗證碼 token 在 httpOnly cookie）
  async function verifyCode(input: { code?: string; recoveryCode?: string }): Promise<AdminUser> {
    const { data } = await http.post<AdminUser>('/auth/totp', input)
    setUser(data)
    return data
  }

  async function logout() {
    await http.post('/auth/logout')
    user.value = null
    useAccountsStore().reset()
  }

  // 沒指定權限＝所有登入者都可用
  function can(perm?: Perm | null): boolean {
    const u = user.value
    if (!u) return false
    if (perm && superOnly.includes(perm)) return u.isSuper
    if (!perm || u.isOwner || u.isSuper) return true
    return u.permissions.includes(perm)
  }

  // 任一權限即可（陣列）；null／空陣列＝所有登入者
  function canAny(perms?: Perm | Perm[] | null): boolean {
    if (!perms || (Array.isArray(perms) && !perms.length)) return can()
    return (Array.isArray(perms) ? perms : [perms]).some((p) => can(p))
  }

  return { user, checked, fetchMe, login, verifyCode, logout, can, canAny }
})
