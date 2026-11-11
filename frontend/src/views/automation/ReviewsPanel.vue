<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { ChatDotRound, Collection, Check } from '@element-plus/icons-vue'
import { flagsApi, reviewKindLabels, type Review, type ReviewKind } from '@/api/flags'
import { intentLabels } from '@/api/automation'
import { useAuthStore } from '@/stores/auth'
import { shortTime } from '@/utils/time'

// 待改進問答：管理員標「回答有誤」與 AI 沒把握（轉真人／拒答）的記錄，依選的帳號列出
const props = defineProps<{ accountId: number }>()
const emit = defineEmits<{ 'go-knowledge': [] }>()

const router = useRouter()
const auth = useAuthStore()

const kind = ref<'' | ReviewKind>('')
const status = ref<'open' | 'resolved' | ''>('open')
const list = ref<Review[]>([])
const loading = ref(false)
const resolving = ref<number | null>(null)

async function load() {
  loading.value = true
  try {
    list.value = await flagsApi.reviews({
      account: props.accountId,
      kind: kind.value || undefined,
      status: status.value || undefined,
    })
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch([kind, status], load)

async function resolve(r: Review) {
  resolving.value = r.id
  try {
    const done = await flagsApi.resolve(r.id)
    if (status.value === 'open') list.value = list.value.filter((x) => x.id !== r.id)
    else list.value = list.value.map((x) => (x.id === r.id ? done : x))
    ElMessage.success('已標為已處理')
  } finally {
    resolving.value = null
  }
}

const openChat = (r: Review) => router.push({ name: 'inbox', params: { id: r.conversationId } })
const replyLabel = (r: Review) => (r.replySender === 'bot' ? (r.ruleName ? `規則「${r.ruleName}」回` : '自動回覆') : 'AI 回')
</script>

<template>
  <div class="reviews">
    <div class="filters">
      <el-segmented
        v-model="kind"
        :options="[
          { label: '全部', value: '' },
          { label: '回答有誤', value: 'ai_wrong' },
          { label: 'AI 沒把握', value: 'ai_unsure' },
        ]"
      />
      <el-segmented
        v-model="status"
        :options="[
          { label: '待處理', value: 'open' },
          { label: '已處理', value: 'resolved' },
          { label: '全部', value: '' },
        ]"
      />
    </div>
    <p class="intro">
      「回答有誤」是同事在收件匣標記的自動回覆；「AI 沒把握」是 AI 轉真人或拒答時自動記下的問題。補好知識庫或規則後標為已處理。
    </p>

    <div v-loading="loading" class="items">
      <article v-for="r in list" :key="r.id" class="item lc-card" :class="{ resolved: !!r.resolvedAt }">
        <header>
          <span class="kind" :class="r.kind">{{ reviewKindLabels[r.kind] }}</span>
          <span class="who">{{ r.contactName ? `@${r.contactName}` : '匿名顧客' }}</span>
          <span v-if="r.intent" class="intent">{{ intentLabels[r.intent] ?? r.intent }}</span>
          <span class="time">{{ shortTime(r.createdAt) }}</span>
        </header>
        <div class="qa">
          <div class="row">
            <span class="lbl">顧客問</span>
            <p class="q">{{ r.question || '（找不到顧客訊息）' }}</p>
          </div>
          <div v-if="r.reply" class="row">
            <span class="lbl">{{ replyLabel(r) }}</span>
            <p class="a">{{ r.reply }}</p>
          </div>
          <div v-if="r.note" class="row">
            <span class="lbl">{{ r.kind === 'ai_wrong' ? '哪裡有誤' : 'AI 理由' }}</span>
            <p class="note">
              {{ r.note }}<span v-if="r.createdBy" class="by"> — {{ r.createdBy }}</span>
            </p>
          </div>
        </div>
        <footer>
          <span v-if="r.resolvedAt" class="done">
            已處理 · {{ shortTime(r.resolvedAt) }}<template v-if="r.resolvedBy"> · {{ r.resolvedBy }}</template>
          </span>
          <div class="actions">
            <el-button v-if="auth.can('inbox')" size="small" round :icon="ChatDotRound" @click="openChat(r)">開啟對話</el-button>
            <el-button size="small" round :icon="Collection" @click="emit('go-knowledge')">到知識庫補充</el-button>
            <el-button
              v-if="!r.resolvedAt"
              size="small"
              round
              type="primary"
              :icon="Check"
              :loading="resolving === r.id"
              @click="resolve(r)"
            >
              標為已處理
            </el-button>
          </div>
        </footer>
      </article>
      <el-empty
        v-if="!loading && !list.length"
        :image-size="90"
        :description="status === 'open' ? '目前沒有待改進的問答' : '沒有符合的記錄'"
      />
    </div>
  </div>
</template>

<style scoped lang="scss">
.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
.intro {
  margin: 10px 0 14px;
  font-size: 13px;
  color: var(--lc-muted);
  line-height: 1.6;
}
.items {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 120px;
  max-width: 860px;
}
.item {
  padding: 14px 16px;
  &.resolved {
    opacity: 0.75;
  }
  header {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    font-size: 13px;
  }
}
.kind {
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 600;
  &.ai_wrong {
    background: var(--lc-tone-rose-bg);
    color: var(--lc-tone-rose-fg);
  }
  &.ai_unsure {
    background: var(--lc-tone-caramel-bg);
    color: var(--lc-tone-caramel-fg);
  }
}
.who {
  font-weight: 500;
}
.intent {
  font-size: 12px;
  color: var(--lc-muted);
  background: var(--lc-bg-chip);
  padding: 1px 8px;
  border-radius: 999px;
}
.time {
  margin-left: auto;
  color: var(--lc-muted);
  font-size: 12px;
}
.qa {
  margin: 10px 0 8px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.row {
  display: grid;
  grid-template-columns: 104px 1fr;
  gap: 8px;
  align-items: baseline;
}
.lbl {
  font-size: 12px;
  color: var(--lc-muted);
}
.row p {
  margin: 0;
  font-size: 14px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
}
.q {
  font-weight: 500;
}
.a {
  padding: 6px 10px;
  border-radius: 10px;
  background: var(--el-color-primary-light-8);
}
.note {
  color: var(--lc-text);
}
.by {
  color: var(--lc-muted);
  font-size: 12px;
}
footer {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.done {
  font-size: 12px;
  color: var(--lc-tone-sage-fg);
}
.actions {
  margin-left: auto;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  .el-button + .el-button {
    margin-left: 0;
  }
}
@media (max-width: 768px) {
  .row {
    grid-template-columns: 1fr;
    gap: 2px;
  }
  .actions {
    margin-left: 0;
  }
}
</style>
