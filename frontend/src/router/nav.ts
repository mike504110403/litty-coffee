import type { Component } from 'vue'
import { markRaw } from 'vue'
import { ChatDotRound, DataAnalysis, House, MagicStick, Calendar, User, Setting, Avatar } from '@element-plus/icons-vue'
import type { Perm } from '@/stores/auth'

export interface NavItem {
  path: string
  name: string
  title: string
  icon: Component
  component: () => Promise<unknown>
  mobile: boolean // 是否出現在手機底部 tab bar（最多 5 個）
  perm: Perm | Perm[] | null // 需要的功能權限（陣列＝任一即可）；null＝所有登入者
}

export const navItems: NavItem[] = [
  { path: 'dashboard', name: 'dashboard', title: '總覽', icon: markRaw(House), component: () => import('@/views/DashboardView.vue'), mobile: true, perm: null },
  { path: 'inbox/:id?', name: 'inbox', title: '收件匣', icon: markRaw(ChatDotRound), component: () => import('@/views/inbox/InboxView.vue'), mobile: true, perm: 'inbox' },
  { path: 'automation', name: 'automation', title: '自動回覆', icon: markRaw(MagicStick), component: () => import('@/views/automation/AutomationView.vue'), mobile: true, perm: 'automation' },
  { path: 'schedule', name: 'schedule', title: '排程', icon: markRaw(Calendar), component: () => import('@/views/schedule/ScheduleView.vue'), mobile: true, perm: 'schedule' },
  { path: 'contacts', name: 'contacts', title: '顧客', icon: markRaw(User), component: () => import('@/views/ContactsView.vue'), mobile: false, perm: 'contacts' },
  { path: 'analytics', name: 'analytics', title: '分析', icon: markRaw(DataAnalysis), component: () => import('@/views/AnalyticsView.vue'), mobile: true, perm: 'analytics' },
  { path: 'settings', name: 'settings', title: '設定', icon: markRaw(Setting), component: () => import('@/views/SettingsView.vue'), mobile: false, perm: ['ig_accounts', 'credentials'] },
  { path: 'admins', name: 'admins', title: '管理員', icon: markRaw(Avatar), component: () => import('@/views/admins/AdminsView.vue'), mobile: false, perm: 'admins' },
]
