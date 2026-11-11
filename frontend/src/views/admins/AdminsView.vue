<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import PillTabs from '@/components/PillTabs.vue'
import { useAuthStore } from '@/stores/auth'
import AdminUsersPanel from './AdminUsersPanel.vue'
import AuditLogPanel from './AuditLogPanel.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

// 「管理員」頁本身需要 admins；「操作紀錄」另需 audit
const tabs = computed(() => [
  { key: 'users', label: '管理員' } as const,
  ...(auth.can('audit') ? [{ key: 'audit', label: '操作紀錄' } as const] : []),
])
type TabKey = 'users' | 'audit'

const tab = computed<TabKey>({
  get: () => (route.query.tab === 'audit' && auth.can('audit') ? 'audit' : 'users'),
  set: (v) => router.replace({ query: { ...route.query, tab: v } }),
})
</script>

<template>
  <div class="admins-page lc-m">
    <h1 class="lc-page-title lc-page-title--solo">管理員</h1>

    <PillTabs v-if="tabs.length > 1" v-model="tab" :tabs="tabs" class="tabs" />

    <section v-if="tab === 'users'" class="lc-card block">
      <AdminUsersPanel />
    </section>
    <section v-else class="lc-card block">
      <AuditLogPanel />
    </section>
  </div>
</template>

<style scoped lang="scss">
.tabs {
  margin-bottom: 16px;
}
.block {
  padding: 20px;
  min-height: 120px;
}
.placeholder {
  margin: 0;
  padding: 32px 0;
  text-align: center;
  color: var(--lc-muted);
  font-size: 14px;
}
@media (max-width: 768px) {
  .admins-page .lc-page-title {
    display: none;
  }
  .tabs {
    width: 100%;
  }
  .block {
    padding: 16px;
  }
}
</style>
