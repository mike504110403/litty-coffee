import axios, { AxiosError } from 'axios'
import { ElMessage } from 'element-plus'
import router from '@/router'

// 全專案唯一的 axios 實例；API 一律經此
export const http = axios.create({
  baseURL: '/api',
  withCredentials: true,
  timeout: 15000,
})

http.interceptors.response.use(
  (res) => res,
  (err: AxiosError<{ error?: string }>) => {
    const status = err.response?.status
    if (status === 401 && router.currentRoute.value.name !== 'login') {
      router.replace({ name: 'login', query: { redirect: router.currentRoute.value.fullPath } })
    } else if (status !== 401) {
      ElMessage.error(err.response?.data?.error ?? '連線失敗，請稍後再試')
    }
    return Promise.reject(err)
  },
)
