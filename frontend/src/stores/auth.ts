import { defineStore } from 'pinia'
import { ref } from 'vue'
import { http } from '@/api/http'

export interface AdminUser {
  id: number
  username: string
  displayName: string
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
    user.value = data
    checked.value = true
  }

  async function logout() {
    await http.post('/auth/logout')
    user.value = null
  }

  return { user, checked, fetchMe, login, logout }
})
