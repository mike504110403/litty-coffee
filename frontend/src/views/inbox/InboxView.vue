<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ChatLineRound, MagicStick } from '@element-plus/icons-vue'
import { storeToRefs } from 'pinia'
import { devApi } from '@/api/inbox'
import { useInboxStore } from '@/stores/inbox'
import { useAuthStore } from '@/stores/auth'
import { useAccountsStore } from '@/stores/accounts'
import ConversationList from './ConversationList.vue'
import ChatThread from './ChatThread.vue'
import SimulatorDialog from './SimulatorDialog.vue'
import ContactDrawer from '@/components/ContactDrawer.vue'

const route = useRoute()
const router = useRouter()
const store = useInboxStore()
const { conversations, active, loading, messages, hasMore, accountFilter } = storeToRefs(store)
const accounts = useAccountsStore()

const routeId = computed(() => {
  const n = Number(route.params.id)
  return Number.isFinite(n) && n > 0 ? n : null
})

watch(routeId, (id) => void store.open(id), { immediate: true })

const contactId = ref<number | null>(null)
const auth = useAuthStore() // 顧客資料需要 contacts 權限
const simulator = ref(false)
const showSim = ref(false)

onMounted(async () => {
  void accounts.ensure()
  void store.fetchConversations()
  try {
    simulator.value = (await devApi.status()).simulator
  } catch {
    simulator.value = false
  }
})

const select = (id: number) => router.push({ name: 'inbox', params: { id } })
const back = () => router.push({ name: 'inbox' })

async function send(text: string) {
  if (active.value) await store.send(active.value.id, text)
}
</script>

<template>
  <div class="inbox" :class="{ 'has-active': !!routeId }">
    <section class="pane-list lc-card">
      <div class="list-title">
        <h1>收件匣</h1>
        <el-button v-if="simulator" size="small" round :icon="MagicStick" @click="showSim = true">模擬訊息</el-button>
      </div>
      <ConversationList
        :conversations="conversations"
        :active-id="routeId"
        :loading="loading"
        :account="accountFilter"
        @update:account="store.setAccountFilter"
        @select="select"
        @search="(q) => store.fetchConversations(q)"
      />
    </section>

    <section class="pane-thread lc-card">
      <ChatThread
        v-if="active"
        :conversation="active"
        :messages="messages[active.id]"
        :has-more="!!hasMore[active.id]"
        @back="back"
        :send-fn="send"
        @load-older="store.loadOlder(active.id)"
        @set-handler="(h) => store.patch(active!.id, { handler: h })"
        @set-status="(s) => store.patch(active!.id, { status: s })"
        @risk-changed="store.applyRisk"
        @open-contact="auth.can('contacts') && (contactId = active!.contact.id)"
      />
      <div v-else class="placeholder">
        <el-icon :size="48"><ChatLineRound /></el-icon>
        <p>選一則對話開始回覆</p>
        <span>新的 IG 私訊會即時出現在左邊</span>
      </div>
    </section>

    <SimulatorDialog v-if="simulator" v-model="showSim" />
    <ContactDrawer :contact-id="contactId" @close="contactId = null" />
  </div>
</template>

<style scoped lang="scss">
.inbox {
  display: grid;
  grid-template-columns: 360px 1fr;
  gap: 16px;
  height: calc(100vh - 56px);
}
.pane-list,
.pane-thread {
  overflow: hidden;
  display: flex;
  flex-direction: column;
  min-height: 0;
}
.list-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 16px 0;
  h1 {
    margin: 0;
    font-size: 20px;
  }
}
.placeholder {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  color: var(--lc-muted);
  p {
    margin: 12px 0 4px;
    font-weight: 500;
    color: var(--lc-text);
  }
  span {
    font-size: 13px;
  }
}

@media (max-width: 1024px) {
  .inbox {
    grid-template-columns: 300px 1fr;
  }
}

@media (max-width: 768px) {
  .inbox {
    display: block;
    height: auto;
  }
  .pane-list {
    border: 0;
    box-shadow: none;
    background: transparent;
    margin: -16px -16px 0;
    border-radius: 0;
    .list-title {
      justify-content: flex-end;
      padding: 10px 16px 0;
      h1 {
        display: none;
      }
      &:has(> :only-child) {
        display: none;
      }
    }
  }
  .pane-thread {
    display: none;
  }
  // 手機進入對話：全螢幕覆蓋（蓋過頂欄與底部 tab bar）
  .has-active .pane-thread {
    display: flex;
    position: fixed;
    inset: 0;
    z-index: 20;
    border-radius: 0;
    border: 0;
    padding-top: env(safe-area-inset-top);
    background: var(--lc-surface);
  }
}
</style>
