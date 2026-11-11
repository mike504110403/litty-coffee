import { defineStore } from 'pinia'
import { ref } from 'vue'
import { http } from '@/api/http'
import { useAccountsStore } from './accounts'

// 功能權限鍵；後端（auth.AllPerms）是唯一來源，這裡只是型別
export type Perm =
  | 'inbox'
  | 'contacts'
  | 'automation'
  | 'schedule'
  | 'analytics'
  | 'ig_accounts'
  | 'credentials'
  | 'admins'

export interface AdminUser {
  id: number
  username: string
  displayName: string
  isOwner: boolean
  permissions: Perm[] // 有效權限：店長是完整清單
  disabled: boolean
  lastLoginAt: string | null
  createdAt: string
}

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

  async function login(username: string, password: string) {
    const { data } = await http.post<AdminUser>('/auth/login', { username, password })
    useAccountsStore().reset() // 可見帳號依登入者而異
    user.value = data
    checked.value = true
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
    if (!perm || u.isOwner) return true
    return u.permissions.includes(perm)
  }

  return { user, checked, fetchMe, login, logout, can }
})
