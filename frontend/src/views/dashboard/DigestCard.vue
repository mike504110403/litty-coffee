<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh, ArrowRight } from '@element-plus/icons-vue'
import { digestApi, type Digest } from '@/api/system'
import { intentLabels } from '@/api/automation'
import { useAuthStore } from '@/stores/auth'
import { useAccountsStore } from '@/stores/accounts'
import { useSystemStore } from '@/stores/system'
import { delta, formatCount, formatPercent } from '@/utils/format'
import AccountSelect from '@/components/AccountSelect.vue'
import HelpTip from '@/components/HelpTip.vue'

// 每日摘要：前一天的量、熱門問題、依重要度排序的待處理對話（點了開對話），有 Claude key 時加 AI 總結。
// filter＝總覽的帳號篩選；「全部帳號」時卡片自己選一個帳號（摘要是每帳號一份）
const props = defineProps<{ filter: number | null }>()

const PAGE = 4
const auth = useAuthStore()
const router = useRouter()
const accounts = useAccountsStore()
const system = useSystemStore()

const acc = ref<number | null>(props.filter)
const digest = ref<Digest | null>(null)
const loading = ref(false)
const generating = ref(false)
const page = ref(1)

async function load() {
  if (acc.value == null) return
  loading.value = true
  try {
    digest.value = await digestApi.latest(acc.value)
    page.value = 1
  } catch {
    digest.value = null
  } finally {
    loading.value = false
  }
}

async function regenerate() {
  if (acc.value == null) return
  generating.value = true
  try {
    digest.value = await digestApi.generate(acc.value)
    page.value = 1
    ElMessage.success('摘要已更新')
  } finally {
    generating.value = false
  }
}

let offDigest: (() => void) | null = null
onMounted(async () => {
  await accounts.ensure()
  acc.value ??= accounts.lastUsed()
  void load()
  offDigest = system.onDigest((d) => {
    if (d.igAccountId === acc.value) void load()
  })
})
onBeforeUnmount(() => offDigest?.())
watch(
  () => props.filter,
  (v) => {
    if (v != null) acc.value = v
  },
)
watch(acc, load)

const c = computed(() => digest.value?.content ?? null)
const dateLabel = computed(() => {
  const d = digest.value?.date
  if (!d) return ''
  const [, m, day] = d.split('-').map(Number)
  const w = '日一二三四五六'[new Date(`${d}T00:00:00`).getDay()]
  return `${m}/${day}（${w}）`
})
const stats = computed(() => {
  const x = c.value
  if (!x) return []
  return [
    { label: '顧客訊息', value: formatCount(x.totals.inbound), d: delta(x.totals.inbound, x.previous.inbound) },
    { label: '互動顧客', value: formatCount(x.totals.activeContacts), d: delta(x.totals.activeContacts, x.previous.activeContacts) },
    { label: '自動回覆占比', value: formatPercent(x.autoShare), d: delta(x.autoShare, x.prevAutoShare) },
    { label: '需真人', value: formatCount(x.totals.humanConvs), d: null },
  ]
})
const intents = computed(() => c.value?.intents.slice(0, 4).map((i) => `${intentLabels[i.key] ?? i.key} ${i.count}`) ?? [])
const todo = computed(() => c.value?.todo ?? [])
const pageTodo = computed(() => todo.value.slice((page.value - 1) * PAGE, page.value * PAGE))
const canOpen = computed(() => auth.can('inbox'))
const open = (id: number) => canOpen.value && router.push({ name: 'inbox', params: { id } })
const urgent = (reasons: string[]) => reasons.some((r) => r.startsWith('未回覆') || r === '轉真人未回' || r === '客訴')
</script>

<template>
  <section v-loading="loading" class="digest lc-card">
    <header>
      <div class="title">
        <h3>每日摘要</h3>
        <span v-if="dateLabel" class="date">{{ dateLabel }}</span>
        <HelpTip text="每天在「自動回覆 → AI 設定」的時間整理前一天的私訊；待處理依未回覆、轉真人、客訴／訂位、問題顧客、待改進排序。" />
      </div>
      <div class="actions">
        <AccountSelect v-if="filter == null && accounts.multi" v-model="acc" size="small" class="acc" />
        <el-button :icon="Refresh" size="small" :loading="generating" :disabled="acc == null" @click="regenerate">
          重新產生
        </el-button>
      </div>
    </header>

    <div v-if="c" class="body">
      <p v-if="c.summaryStatus === 'ok' && c.summary" class="summary">{{ c.summary }}</p>
      <el-tooltip v-else-if="c.summaryStatus === 'failed'" :content="c.summaryError || 'AI 呼叫失敗'" placement="top">
        <el-tag type="warning" size="small" class="sum-fail">AI 總結失敗</el-tag>
      </el-tooltip>

      <div class="stats">
        <div v-for="s in stats" :key="s.label" class="stat">
          <span class="s-label">{{ s.label }}</span>
          <span class="s-value">{{ s.value }}</span>
          <span v-if="s.d != null" class="s-delta" :class="s.d >= 0 ? 'up' : 'down'">
            {{ s.d >= 0 ? '▲' : '▼' }}{{ Math.abs(Math.round(s.d * 100)) }}%
          </span>
        </div>
      </div>
      <div v-if="intents.length" class="intents">
        <el-tag v-for="i in intents" :key="i" size="small" type="info" effect="plain" round>{{ i }}</el-tag>
      </div>

      <div class="todo-head">
        待處理
        <b>{{ c.todoTotal }}</b>
      </div>
      <ul v-if="todo.length" class="todo">
        <li
          v-for="t in pageTodo"
          :key="t.conversationId"
          :class="{ link: canOpen }"
          :tabindex="canOpen ? 0 : -1"
          @click="open(t.conversationId)"
          @keydown.enter="open(t.conversationId)"
        >
          <div class="t-main">
            <span class="t-who">{{ t.contact }}</span>
            <span class="t-tags">
              <el-tag
                v-for="r in t.reasons"
                :key="r"
                size="small"
                :type="urgent([r]) ? 'danger' : 'warning'"
                effect="light"
                disable-transitions
              >
                {{ r }}
              </el-tag>
            </span>
            <span v-if="t.lastText" class="t-text">{{ t.lastText }}</span>
          </div>
          <el-icon v-if="canOpen" class="t-go"><ArrowRight /></el-icon>
        </li>
      </ul>
      <div v-else class="empty">沒有待處理的對話</div>
      <el-pagination
        v-if="todo.length > PAGE"
        v-model:current-page="page"
        :page-size="PAGE"
        :total="todo.length"
        layout="prev, pager, next"
        size="small"
        class="pager"
      />
    </div>
    <div v-else-if="!loading" class="empty-state">
      <span>還沒有摘要</span>
      <el-button type="primary" size="small" :loading="generating" :disabled="acc == null" @click="regenerate">產生昨天的摘要</el-button>
    </div>
  </section>
</template>

<style scoped lang="scss">
.digest {
  padding: 16px 18px 14px;
  display: flex;
  flex-direction: column;
  min-width: 0;
}
header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.title {
  display: flex;
  align-items: center;
  gap: 8px;
  h3 {
    margin: 0;
    font-size: 15px;
  }
}
.date {
  font-size: 13px;
  color: var(--lc-muted);
}
.actions {
  display: flex;
  gap: 8px;
  align-items: center;
  .acc {
    width: 160px;
  }
}
.body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  flex: 1;
  min-height: 0;
}
.summary {
  margin: 0;
  font-size: 14px;
  line-height: 1.7;
  padding: 10px 12px;
  border-radius: 10px;
  background: var(--lc-bg-soft);
}
.sum-fail {
  align-self: flex-start;
}
.stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
}
.stat {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.s-label {
  font-size: 12px;
  color: var(--lc-muted);
}
.s-value {
  font-size: 20px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.s-delta {
  font-size: 12px;
  font-weight: 600;
  &.up {
    color: var(--lc-delta-good);
  }
  &.down {
    color: var(--lc-delta-bad);
  }
}
.intents {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.todo-head {
  font-size: 13px;
  font-weight: 600;
  b {
    margin-left: 4px;
    color: var(--lc-fail-fg);
  }
}
.todo {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 6px;
  li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-radius: 10px;
    border: 1px solid var(--lc-border);
    &.link {
      cursor: pointer;
      &:hover,
      &:focus-visible {
        background: var(--lc-bg-hover);
        outline: none;
      }
    }
  }
}
.t-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
}
.t-who {
  font-weight: 600;
  font-size: 14px;
}
.t-tags {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
}
.t-text {
  flex-basis: 100%;
  font-size: 12px;
  color: var(--lc-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.t-go {
  color: var(--lc-muted);
  flex: none;
}
.pager {
  justify-content: center;
}
.empty {
  font-size: 13px;
  color: var(--lc-muted);
  padding: 8px 0;
}
.empty-state {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 24px 0;
  color: var(--lc-muted);
  font-size: 13px;
}
@media (max-width: 768px) {
  .stats {
    grid-template-columns: repeat(2, 1fr);
  }
}
</style>
