import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore, type Perm } from '@/stores/auth'
import { navItems } from './nav'

const router = createRouter({
  history: createWebHistory(),
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

router.beforeEach(async (to) => {
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
