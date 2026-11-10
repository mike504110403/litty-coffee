<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { SwitchButton } from '@element-plus/icons-vue'
import { navItems } from '@/router/nav'
import { useAuthStore } from '@/stores/auth'
import { useInboxStore } from '@/stores/inbox'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const inbox = useInboxStore()

onMounted(() => {
  inbox.connect()
  if (!inbox.conversations.length) void inbox.fetchConversations()
})
onBeforeUnmount(() => inbox.disconnect())

const badge = (name: string) => (name === 'inbox' ? inbox.totalUnread : 0)

const mobileItems = computed(() => navItems.filter((n) => n.mobile))
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
          v-for="n in navItems"
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
            <el-dropdown-item @click="router.push({ name: 'contacts' })">顧客</el-dropdown-item>
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
  background: var(--lc-espresso);
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
  box-shadow: 0 0 0 1px rgba(255, 255, 255, 0.08);
}
.brand-name {
  font-size: 18px;
  color: #fff;
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
    background: rgba(255, 255, 255, 0.06);
  }
  &.is-active {
    opacity: 1;
    color: var(--lc-espresso);
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
  background: #d9534f;
  color: #fff;
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
  border-top: 1px solid rgba(255, 255, 255, 0.08);
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
  color: #fff;
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
    background: rgba(250, 246, 241, 0.9);
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
    background: rgba(255, 255, 255, 0.95);
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
