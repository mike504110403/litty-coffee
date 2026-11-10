import type { Component } from 'vue'
import { markRaw } from 'vue'
import { ChatDotRound, DataAnalysis, House, MagicStick, Calendar, User, Setting } from '@element-plus/icons-vue'

export interface NavItem {
  path: string
  name: string
  title: string
  icon: Component
  component: () => Promise<unknown>
  mobile: boolean // 是否出現在手機底部 tab bar（最多 5 個）
}

const placeholder = () => import('@/views/PlaceholderView.vue')

export const navItems: NavItem[] = [
  { path: 'dashboard', name: 'dashboard', title: '總覽', icon: markRaw(House), component: () => import('@/views/DashboardView.vue'), mobile: true },
  { path: 'inbox/:id?', name: 'inbox', title: '收件匣', icon: markRaw(ChatDotRound), component: () => import('@/views/inbox/InboxView.vue'), mobile: true },
  { path: 'automation', name: 'automation', title: '自動回覆', icon: markRaw(MagicStick), component: () => import('@/views/automation/AutomationView.vue'), mobile: true },
  { path: 'schedule', name: 'schedule', title: '排程', icon: markRaw(Calendar), component: () => import('@/views/schedule/ScheduleView.vue'), mobile: true },
  { path: 'contacts', name: 'contacts', title: '顧客', icon: markRaw(User), component: () => import('@/views/ContactsView.vue'), mobile: false },
  { path: 'analytics', name: 'analytics', title: '分析', icon: markRaw(DataAnalysis), component: () => import('@/views/AnalyticsView.vue'), mobile: true },
  { path: 'settings', name: 'settings', title: '設定', icon: markRaw(Setting), component: placeholder, mobile: false },
]
