<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { automationApi, distillApi } from '@/api/automation'
import RulesPanel from './RulesPanel.vue'
import KnowledgePanel from './KnowledgePanel.vue'
import SettingsPanel from './SettingsPanel.vue'
import SandboxPanel from './SandboxPanel.vue'
import ReviewsPanel from './ReviewsPanel.vue'
import DistillPanel from './DistillPanel.vue'
import AccountSelect from '@/components/AccountSelect.vue'
import { useAccountsStore } from '@/stores/accounts'
import PillTabs from '@/components/PillTabs.vue'
import HelpTip from '@/components/HelpTip.vue'
import { useMediaQuery } from '@/composables/useMediaQuery'

const route = useRoute()
const router = useRouter()

const allTabs = [
  { key: 'rules', label: '關鍵字規則' },
  { key: 'knowledge', label: '店家知識庫' },
  { key: 'ai', label: 'AI 設定' },
  { key: 'sandbox', label: '試答沙盒' },
  { key: 'reviews', label: '待改進' },
  { key: 'distill', label: '蒸餾建議' },
] as const
type TabKey = (typeof allTabs)[number]['key']

// 寬螢幕：AI 設定（左）與試答沙盒（右）併成同一分頁，邊改邊試；窄螢幕仍是兩個分頁
const wide = useMediaQuery('(min-width: 1280px)')
// 手機：六個分頁用短名稱才排得下
const narrow = useMediaQuery('(max-width: 768px)')
const shortLabels: Partial<Record<TabKey, string>> = { rules: '規則', knowledge: '知識庫', ai: 'AI', sandbox: '試答', distill: '蒸餾' }
// 有待審核的蒸餾建議時，分頁名稱帶數字
const distillPending = ref(0)
const tabs = computed(() =>
  (wide.value
    ? allTabs.filter((t) => t.key !== 'sandbox').map((t) => (t.key === 'ai' ? { ...t, label: 'AI 設定與試答' } : t))
    : allTabs.map((t) => (narrow.value && shortLabels[t.key] ? { ...t, label: shortLabels[t.key]! } : t))
  ).map((t) => (t.key === 'distill' && distillPending.value ? { ...t, label: `${t.label} ${distillPending.value}` } : t)),
)

const tab = computed<TabKey>({
  get: () => {
    const q = route.query.tab
    const t = allTabs.find((x) => x.key === q)?.key ?? 'rules'
    return wide.value && t === 'sandbox' ? 'ai' : t
  },
  set: (v) => router.replace({ query: { ...route.query, tab: v } }),
})

// 各分頁的一句說明（取代頁內說明文字）
const tabHelp: Record<TabKey, string> = {
  rules: '先比關鍵字規則（優先順序數字小的先），沒命中再交給 AI 依知識庫回答；沒把握就轉真人。',
  knowledge: 'AI 只會根據這裡的內容回答。寫得越具體（價格、時間、規則），AI 越不需要轉真人；沒填的段落不會給 AI 看。',
  ai: '調整 AI 的角色語氣、轉真人與降頻規則；右側（寬螢幕）可立刻試答。',
  sandbox: '用目前的規則與知識庫試回覆，不會送給任何人。',
  reviews: '「回答有誤」是同事在收件匣標記的自動回覆；「AI 沒把握」是 AI 轉真人或拒答時自動記下的問題。補好知識庫或規則後標為已處理。',
  distill: 'AI 從近期對話整理出的規則與知識庫建議。只有按「採用」的才會生效；修改既有項目時會標出前後差異。',
}

const status = ref<{ aiConfigured: boolean; model: string } | null>(null)

// 每個 IG 帳號有自己的規則、知識庫、AI 設定；沙盒也用選的帳號
const accounts = useAccountsStore()
const accountId = ref<number | null>(null)
watch(accountId, (id) => {
  accounts.remember(id)
  void loadPending()
})

async function loadPending() {
  const id = accountId.value
  if (id == null) return
  try {
    const list = await distillApi.suggestions(id, 'pending')
    if (id === accountId.value) distillPending.value = list.length
  } catch {
    distillPending.value = 0
  }
}
const goDistill = () => (tab.value = 'distill')

onMounted(async () => {
  await accounts.ensure()
  accountId.value = accounts.lastUsed()
  status.value = await automationApi.status()
})
</script>

<template>
  <div class="lc-m">
    <div class="head">
      <div>
        <h1 class="lc-page-title lc-page-title--solo">自動回覆</h1>
      </div>
      <div v-if="status" class="ai-status" :class="{ on: status.aiConfigured }">
        <span class="dot" />
        {{ status.aiConfigured ? `AI 已連線 · ${status.model}` : 'AI 未設定（只跑關鍵字）' }}
      </div>
    </div>

    <div class="toolbar">
      <PillTabs v-model="tab" :tabs="tabs" class="tabs" />
      <div class="tool-right">
        <AccountSelect v-if="accounts.list.length" v-model="accountId" class="acc" />
        <HelpTip :text="tabHelp[tab]" placement="bottom-end" />
      </div>
    </div>

    <el-empty v-if="accounts.loaded && !accounts.list.length" description="還沒有 IG 帳號：請先到「設定」連結" />
    <template v-else-if="accountId != null">
      <RulesPanel
        v-if="tab === 'rules'"
        :key="`r${accountId}`"
        :account-id="accountId"
        :distill-pending="distillPending"
        @go-distill="goDistill"
      />
      <KnowledgePanel
        v-else-if="tab === 'knowledge'"
        :key="`k${accountId}`"
        :account-id="accountId"
        :distill-pending="distillPending"
        @go-distill="goDistill"
      />
      <div v-else-if="tab === 'ai'" :class="{ 'ai-split': wide }">
        <SettingsPanel :key="`s${accountId}`" :account-id="accountId" :ai-configured="!!status?.aiConfigured" />
        <SandboxPanel
          v-if="wide"
          :key="`t${accountId}`"
          :account-id="accountId"
          :ai-configured="!!status?.aiConfigured"
        />
      </div>
      <ReviewsPanel
        v-else-if="tab === 'reviews'"
        :key="`v${accountId}`"
        :account-id="accountId"
        @go-knowledge="tab = 'knowledge'"
      />
      <DistillPanel
        v-else-if="tab === 'distill'"
        :key="`d${accountId}`"
        :account-id="accountId"
        :ai-configured="!!status?.aiConfigured"
        @changed="(n) => (distillPending = n)"
      />
      <SandboxPanel v-else :key="`t${accountId}`" :account-id="accountId" :ai-configured="!!status?.aiConfigured" />
    </template>
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
.ai-split {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 20px;
  align-items: start;
  // 兩個面板各自帶卡片寬度上限，在格內填滿
  :deep(.lc-card) {
    max-width: none;
  }
}
.tool-right {
  display: flex;
  align-items: center;
  gap: 4px;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 18px;
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
    :deep(.pill) {
      flex: 1 0 auto;
      padding: 0 10px;
      font-size: 14px;
    }
  }
  .tool-right {
    width: 100%;
  }
  .acc {
    flex: 1;
  }
}
</style>
