import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore, type Perm } from '@/stores/auth'
import { navItems } from './nav'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('@/layouts/AppLayout.vue'),
      children: [
        { path: '', redirect: { name: 'dashboard' } },
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
  const perm = to.meta.perm as Perm | null | undefined
  if (perm && !auth.can(perm)) return to.name === 'dashboard' ? true : { name: 'dashboard' }
  return true
})

export default router
