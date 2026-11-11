import axios, { AxiosError } from 'axios'
import { ElMessage, ElMessageBox } from 'element-plus'
import router from '@/router'
import { useAuthStore } from '@/stores/auth'

// 被別處登入踢掉時的提示（顯示中不重複跳）
let replacedNotice: Promise<unknown> | null = null

// 全專案唯一的 axios 實例；API 一律經此
export const http = axios.create({
  baseURL: '/api',
  withCredentials: true,
  timeout: 15000,
})

http.interceptors.response.use(
  (res) => res,
  (err: AxiosError<{ error?: string; code?: string }>) => {
    const status = err.response?.status
    if (status === 401 && router.currentRoute.value.name !== 'login') {
      useAuthStore().user = null // 帳號被停用等：清掉登入狀態，否則路由守衛會把 /login 導回總覽
      const redirect = router.currentRoute.value.fullPath
      // 單一登入：同帳號在別處登入，這裡被踢掉。同時多個請求一起 401 時只跳一次提示
      if (err.response?.data?.code === 'session_replaced' && !replacedNotice) {
        replacedNotice = ElMessageBox.alert(err.response.data.error ?? '此帳號已在其他裝置登入，你已被登出', '已被登出', {
          type: 'warning',
          confirmButtonText: '重新登入',
        })
          .catch(() => {})
          .finally(() => {
            replacedNotice = null
            router.replace({ name: 'login', query: { redirect } })
          })
      } else if (!replacedNotice) {
        router.replace({ name: 'login', query: { redirect } })
      }
    } else if (status === 403 && err.response?.data?.code === 'setup_required') {
      // 首次登入鎖定（必須改密碼／超管未綁 GA）：導去設定頁，不跳錯誤
      if (router.currentRoute.value.name !== 'setup') router.replace({ name: 'setup' })
    } else if (status !== 401) {
      ElMessage.error(err.response?.data?.error ?? '連線失敗，請稍後再試')
    }
    return Promise.reject(err)
  },
)
