import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore, type Perm } from '@/stores/auth'
import { navItems } from './nav'

// 每個頁面（路由名）各記自己的捲動位置：切 tab 回來時回到上次的位置，而不是最上面
const scrollPos = new Map<string, number>()

const router = createRouter({
  history: createWebHistory(),
  scrollBehavior(to, from, saved) {
    if (saved) return saved // 瀏覽器上一頁／下一頁
    if (to.name === from.name) return false // 同頁換參數（例如切分頁、選對話）不動
    const top = scrollPos.get(String(to.name)) ?? 0
    // 等頁面（lazy 載入、KeepAlive 還原）畫出來再捲
    return new Promise((resolve) => requestAnimationFrame(() => resolve({ top })))
  },
  routes: [
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
    { path: '/setup', name: 'setup', component: () => import('@/views/SetupView.vue'), meta: { setup: true } },
    {
      path: '/',
      component: () => import('@/layouts/AppLayout.vue'),
      children: [
        { path: '', redirect: { name: 'dashboard' } },
        { path: 'analytics', redirect: { name: 'dashboard' } }, // 分析已併入總覽
        ...navItems.map((n) => ({ path: n.path, name: n.name, component: n.component, meta: { title: n.title, perm: n.perm } })),
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to, from) => {
  if (from.name) scrollPos.set(String(from.name), window.scrollY)
  const auth = useAuthStore()
  if (!auth.checked) await auth.fetchMe()
  if (to.meta.public) return auth.user && to.name === 'login' ? { name: 'dashboard' } : true
  if (!auth.user) return { name: 'login', query: { redirect: to.fullPath } }
  // 首次登入鎖定：改密碼、綁 GA 前只能待在 /setup；完成後 /setup 不再可進
  if (auth.user.setupRequired) return to.name === 'setup' ? true : { name: 'setup' }
  if (to.meta.setup) return { name: 'dashboard' }
  const perm = to.meta.perm as Perm | Perm[] | null | undefined
  if (perm && !auth.canAny(perm)) return to.name === 'dashboard' ? true : { name: 'dashboard' }
  return true
})

export default router
