<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { automationApi } from '@/api/automation'
import RulesPanel from './RulesPanel.vue'
import KnowledgePanel from './KnowledgePanel.vue'
import SettingsPanel from './SettingsPanel.vue'
import SandboxPanel from './SandboxPanel.vue'

const route = useRoute()
const router = useRouter()

const tabs = [
  { key: 'rules', label: '關鍵字規則' },
  { key: 'knowledge', label: '店家知識庫' },
  { key: 'ai', label: 'AI 設定' },
  { key: 'sandbox', label: '試答沙盒' },
] as const
type TabKey = (typeof tabs)[number]['key']

const tab = computed<TabKey>({
  get: () => (tabs.some((t) => t.key === route.query.tab) ? (route.query.tab as TabKey) : 'rules'),
  set: (v) => router.replace({ query: { ...route.query, tab: v } }),
})

const status = ref<{ aiConfigured: boolean; model: string } | null>(null)
onMounted(async () => {
  status.value = await automationApi.status()
})
</script>

<template>
  <div>
    <div class="head">
      <div>
        <h1 class="lc-page-title">自動回覆</h1>
        <p class="lc-page-sub">先比關鍵字規則，沒命中再交給 AI 依知識庫回答；沒把握就轉真人。</p>
      </div>
      <div v-if="status" class="ai-status" :class="{ on: status.aiConfigured }">
        <span class="dot" />
        {{ status.aiConfigured ? `AI 已連線 · ${status.model}` : 'AI 未設定（只跑關鍵字）' }}
      </div>
    </div>

    <div class="tabs">
      <button v-for="t in tabs" :key="t.key" class="tab" :class="{ on: tab === t.key }" @click="tab = t.key">
        {{ t.label }}
      </button>
    </div>

    <RulesPanel v-if="tab === 'rules'" />
    <KnowledgePanel v-else-if="tab === 'knowledge'" />
    <SettingsPanel v-else-if="tab === 'ai'" :ai-configured="!!status?.aiConfigured" />
    <SandboxPanel v-else :ai-configured="!!status?.aiConfigured" />
  </div>
</template>

<style scoped lang="scss">
.head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
}
.ai-status {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-radius: 999px;
  font-size: 13px;
  background: var(--lc-tone-rose-bg);
  color: var(--lc-tone-rose-fg);
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: currentColor;
  }
  &.on {
    background: var(--lc-tone-sage-bg);
    color: var(--lc-tone-sage-fg);
  }
}
.tabs {
  display: flex;
  gap: 4px;
  padding: 4px;
  margin-bottom: 18px;
  border-radius: 14px;
  background: var(--lc-bg-track);
  width: fit-content;
  max-width: 100%;
  overflow-x: auto;
  scrollbar-width: none;
}
.tab {
  flex-shrink: 0;
  border: 0;
  padding: 8px 16px;
  border-radius: 10px;
  background: transparent;
  font: inherit;
  font-size: 14px;
  color: var(--lc-muted);
  cursor: pointer;
  &.on {
    background: var(--lc-bg-tab-on);
    color: var(--lc-text);
    font-weight: 500;
    box-shadow: var(--lc-tab-shadow);
  }
}
@media (max-width: 768px) {
  .head {
    flex-direction: column;
    gap: 0;
    .lc-page-title {
      display: none;
    }
  }
  .ai-status {
    margin-bottom: 12px;
  }
  .tabs {
    width: 100%;
  }
  .tab {
    flex: 1;
    padding: 8px 10px;
  }
}
</style>
