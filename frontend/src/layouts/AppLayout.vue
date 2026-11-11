<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { SwitchButton } from '@element-plus/icons-vue'
import { ElNotification } from 'element-plus'
import { contactLabel, type Conversation } from '@/api/inbox'
import { navItems } from '@/router/nav'
import { useAuthStore } from '@/stores/auth'
import { useInboxStore } from '@/stores/inbox'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const inbox = useInboxStore()

// 自動回覆轉真人：所有登入中、看得到這個帳號的管理員都會收到
let offHandoff: (() => void) | null = null
function notifyHandoff({ conversation: c, reason }: { conversation: Conversation; reason: string }) {
  const who = contactLabel(c.contact)
  const open = () => router.push({ name: 'inbox', params: { id: c.id } })
  const n = ElNotification({
    title: `需要真人回覆：${who}`,
    message: reason || '自動回覆已轉給真人',
    type: 'warning',
    duration: 0, // 不自動消失，處理了才關
    position: 'bottom-right',
    onClick: () => {
      open()
      n.close()
    },
  })
  // 分頁在背景時再補一則系統通知（使用者有允許才會跳）
  if (document.visibilityState !== 'visible' && 'Notification' in window && Notification.permission === 'granted') {
    const sys = new Notification(`需要真人回覆：${who}`, { body: reason, tag: `handoff-${c.id}` })
    sys.onclick = () => {
      window.focus()
      open()
      sys.close()
    }
  }
}

onMounted(() => {
  if (!auth.can('inbox')) return // 沒收件匣權限就不連即時推送（後端會 403）
  inbox.connect()
  offHandoff = inbox.onHandoff(notifyHandoff)
  if (!inbox.conversations.length) void inbox.fetchConversations()
  // 第一次登入時詢問是否允許系統通知（拒絕也不影響頁內通知）
  if ('Notification' in window && Notification.permission === 'default') void Notification.requestPermission()
})
onBeforeUnmount(() => {
  offHandoff?.()
  inbox.disconnect()
})

const badge = (name: string) => (name === 'inbox' ? inbox.totalUnread : 0)

const visibleItems = computed(() => navItems.filter((n) => auth.can(n.perm)))
const mobileItems = computed(() => visibleItems.value.filter((n) => n.mobile))
const title = computed(() => (route.meta.title as string | undefined) ?? '')
const initial = computed(() => (auth.user?.displayName || auth.user?.username || '?').slice(0, 1).toUpperCase())

async function onLogout() {
  inbox.disconnect()
  await auth.logout()
  router.replace({ name: 'login' })
}
</script>

<template>
  <div class="shell">
    <!-- 桌機側欄 -->
    <aside class="sidebar">
      <div class="brand">
        <img src="/favicon.svg" alt="" class="brand-logo" />
        <div>
          <div class="brand-name lc-brand-font">Litty Coffee</div>
          <div class="brand-sub">IG 經營後台</div>
        </div>
      </div>
      <nav class="side-nav">
        <RouterLink
          v-for="n in visibleItems"
          :key="n.name"
          :to="{ name: n.name }"
          class="side-link"
          active-class="is-active"
        >
          <el-icon :size="18"><component :is="n.icon" /></el-icon>
          <span>{{ n.title }}</span>
          <span v-if="badge(n.name)" class="nav-badge">{{ badge(n.name) > 99 ? '99+' : badge(n.name) }}</span>
        </RouterLink>
      </nav>
      <div class="side-footer">
        <div class="avatar">{{ initial }}</div>
        <div class="who">{{ auth.user?.displayName || auth.user?.username }}</div>
        <el-tooltip content="登出" placement="top">
          <el-button text circle :icon="SwitchButton" @click="onLogout" />
        </el-tooltip>
      </div>
    </aside>

    <!-- 手機頂欄 -->
    <header class="topbar">
      <span class="topbar-title">{{ title }}</span>
      <el-dropdown trigger="click">
        <div class="avatar">{{ initial }}</div>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item v-if="auth.can('contacts')" @click="router.push({ name: 'contacts' })">顧客</el-dropdown-item>
            <el-dropdown-item @click="router.push({ name: 'settings' })">設定</el-dropdown-item>
            <el-dropdown-item divided @click="onLogout">登出</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </header>

    <main class="content">
      <RouterView />
    </main>

    <!-- 手機底部 tab bar -->
    <nav class="tabbar">
      <RouterLink
        v-for="n in mobileItems"
        :key="n.name"
        :to="{ name: n.name }"
        class="tab"
        active-class="is-active"
      >
        <el-badge :value="badge(n.name)" :hidden="!badge(n.name)" :max="99" class="tab-badge">
          <el-icon :size="22"><component :is="n.icon" /></el-icon>
        </el-badge>
        <span>{{ n.title }}</span>
      </RouterLink>
    </nav>
  </div>
</template>

<style scoped lang="scss">
.shell {
  min-height: 100vh;
}

.sidebar {
  position: fixed;
  inset: 0 auto 0 0;
  width: var(--lc-sidebar-w);
  display: flex;
  flex-direction: column;
  padding: 20px 14px;
  background: var(--lc-sidebar-bg);
  color: var(--lc-latte);
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 4px 8px 24px;
}
.brand-logo {
  width: 40px;
  height: 40px;
  border-radius: 12px;
  box-shadow: 0 0 0 1px var(--lc-side-line);
}
.brand-name {
  font-size: 18px;
  color: var(--lc-on-fill);
}
.brand-sub {
  font-size: 12px;
  opacity: 0.6;
}

.side-nav {
  display: flex;
  flex-direction: column;
  gap: 4px;
  flex: 1;
}
.side-link {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 11px 14px;
  border-radius: 12px;
  color: inherit;
  text-decoration: none;
  font-size: 15px;
  opacity: 0.75;
  transition: background 0.15s, opacity 0.15s;
  &:hover {
    opacity: 1;
    background: var(--lc-side-hover);
  }
  &.is-active {
    opacity: 1;
    color: var(--lc-ink);
    background: var(--lc-latte);
    font-weight: 500;
  }
}

.nav-badge {
  margin-left: auto;
  min-width: 20px;
  height: 20px;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--lc-danger);
  color: var(--lc-on-fill);
  font-size: 11px;
  font-weight: 700;
  display: grid;
  place-items: center;
}
.tab-badge :deep(.el-badge__content) {
  border: 0;
}

.side-footer {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 8px 0;
  border-top: 1px solid var(--lc-side-line);
  .who {
    flex: 1;
    font-size: 14px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  :deep(.el-button) {
    color: var(--lc-latte);
  }
}

.avatar {
  width: 34px;
  height: 34px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  background: var(--lc-caramel);
  color: var(--lc-on-fill);
  font-weight: 700;
  font-size: 14px;
  cursor: pointer;
  flex-shrink: 0;
}

.content {
  margin-left: var(--lc-sidebar-w);
  padding: 28px 32px;
  max-width: calc(1200px + var(--lc-sidebar-w));
}

.topbar,
.tabbar {
  display: none;
}

@media (max-width: 768px) {
  .sidebar {
    display: none;
  }
  .topbar {
    position: sticky;
    top: 0;
    z-index: 10;
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 56px;
    padding: 0 16px;
    padding-top: env(safe-area-inset-top);
    background: var(--lc-topbar-bg);
    backdrop-filter: blur(12px);
    border-bottom: 1px solid var(--lc-border);
  }
  .topbar-title {
    font-size: 17px;
    font-weight: 700;
  }
  .content {
    margin-left: 0;
    padding: 16px 16px calc(var(--lc-tabbar-h) + 24px + env(safe-area-inset-bottom));
  }
  .tabbar {
    position: fixed;
    inset: auto 0 0 0;
    z-index: 10;
    display: flex;
    height: calc(var(--lc-tabbar-h) + env(safe-area-inset-bottom));
    padding-bottom: env(safe-area-inset-bottom);
    background: var(--lc-tabbar-bg);
    backdrop-filter: blur(12px);
    border-top: 1px solid var(--lc-border);
  }
  .tab {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 3px;
    font-size: 11px;
    color: var(--lc-muted);
    text-decoration: none;
    &.is-active {
      color: var(--el-color-primary);
      font-weight: 500;
    }
  }
}
</style>
