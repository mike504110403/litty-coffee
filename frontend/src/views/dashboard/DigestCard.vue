<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh, ArrowRight } from '@element-plus/icons-vue'
import { digestApi, type Digest } from '@/api/system'
import { intentLabels } from '@/api/automation'
import { useAuthStore } from '@/stores/auth'
import { useAccountsStore } from '@/stores/accounts'
import { useMediaQuery } from '@/composables/useMediaQuery'
import { useSystemStore } from '@/stores/system'
import { delta, formatCount, formatPercent } from '@/utils/format'
import AccountSelect from '@/components/AccountSelect.vue'
import HelpTip from '@/components/HelpTip.vue'

// 每日摘要：前一天的量、熱門問題、依重要度排序的待處理對話（點了開對話），有 Claude key 時加 AI 總結。
// filter＝總覽的帳號篩選；「全部帳號」時卡片自己選一個帳號（摘要是每帳號一份）
const props = defineProps<{ filter: number | null }>()

// 待處理預設只列前幾則，其餘展開看；手機只列 3 則，避免卡片太高
const isNarrow = useMediaQuery('(max-width: 768px)')
const SHOW = computed(() => (isNarrow.value ? 3 : 5))
const auth = useAuthStore()
const router = useRouter()
const accounts = useAccountsStore()
const system = useSystemStore()

const acc = ref<number | null>(props.filter)
const digest = ref<Digest | null>(null)
const loading = ref(false)
const generating = ref(false)
const showAll = ref(false)
const sumOpen = ref(false)
const sumClamped = ref(false)
const sumEl = ref<HTMLElement | null>(null)

async function load() {
  if (acc.value == null) return
  loading.value = true
  try {
    digest.value = await digestApi.latest(acc.value)
    showAll.value = false
    sumOpen.value = false
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
    showAll.value = false
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
const shownTodo = computed(() => (showAll.value ? todo.value : todo.value.slice(0, SHOW.value)))
const canOpen = computed(() => auth.can('inbox'))
const open = (id: number) => canOpen.value && router.push({ name: 'inbox', params: { id } })
// AI 總結預設 3 行；實際被截斷才出現「展開」
function measure() {
  const el = sumEl.value
  sumClamped.value = !!el && !sumOpen.value && el.scrollHeight > el.clientHeight + 1
}
watch(
  () => c.value?.summary,
  () => {
    sumOpen.value = false
    void nextTick(measure)
  },
  { flush: 'post' },
)
const urgent = (reasons: string[]) => reasons.some((r) => r.startsWith('未回覆') || r === '轉真人未回' || r === '客訴')
</script>

<template>
  <section v-loading="loading && !c" class="digest lc-card" :class="{ slim: !c }">
    <header>
      <div class="title">
        <h3>每日摘要</h3>
        <span v-if="dateLabel" class="date">{{ dateLabel }}</span>
        <span v-else-if="!loading" class="date">尚未產生</span>
        <HelpTip text="每天在「自動回覆 → AI 設定」的時間整理前一天的私訊；待處理依未回覆、轉真人、客訴／訂位、問題顧客、待改進排序。" />
      </div>
      <div class="actions">
        <AccountSelect v-if="filter == null && accounts.multi" v-model="acc" size="small" class="acc" />
        <el-button
          :icon="c ? Refresh : undefined"
          size="small"
          :type="c ? 'default' : 'primary'"
          :loading="generating"
          :disabled="acc == null"
          @click="regenerate"
        >
          {{ c ? '重新產生' : '產生' }}
        </el-button>
      </div>
    </header>

    <div v-if="c" class="body">
      <div class="left">
        <div v-if="c.summaryStatus === 'ok' && c.summary" class="sum-wrap">
          <p ref="sumEl" class="summary" :class="{ open: sumOpen }">{{ c.summary }}</p>
          <button v-if="sumClamped || sumOpen" type="button" class="more" @click="((sumOpen = !sumOpen), nextTick(measure))">
            {{ sumOpen ? '收合' : '展開' }}
          </button>
        </div>
        <el-tooltip v-else-if="c.summaryStatus === 'failed'" :content="c.summaryError || 'AI 呼叫失敗'" placement="top">
          <el-tag type="warning" size="small" class="sum-fail">AI 總結失敗</el-tag>
        </el-tooltip>

        <div class="stats">
          <div v-for="s in stats" :key="s.label" class="stat">
            <span class="s-label">{{ s.label }}</span>
            <span class="s-line">
              <span class="s-value">{{ s.value }}</span>
              <span v-if="s.d != null" class="s-delta" :class="s.d >= 0 ? 'up' : 'down'">
                {{ s.d >= 0 ? '▲' : '▼' }}{{ Math.abs(Math.round(s.d * 100)) }}%
              </span>
            </span>
          </div>
        </div>
        <div v-if="intents.length" class="intents">
          <el-tag v-for="i in intents" :key="i" size="small" type="info" effect="plain" round>{{ i }}</el-tag>
        </div>
      </div>

      <div class="right">
        <div class="todo-head">
          待處理
          <b>{{ c.todoTotal }}</b>
        </div>
        <ul v-if="todo.length" class="todo" :class="{ scroll: showAll }">
          <li
            v-for="t in shownTodo"
            :key="t.conversationId"
            :class="{ link: canOpen }"
            :tabindex="canOpen ? 0 : -1"
            @click="open(t.conversationId)"
            @keydown.enter="open(t.conversationId)"
          >
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
            <el-icon v-if="canOpen" class="t-go"><ArrowRight /></el-icon>
          </li>
        </ul>
        <div v-else class="empty">沒有待處理的對話</div>
        <button v-if="todo.length > SHOW" type="button" class="more" @click="showAll = !showAll">
          {{ showAll ? '收合' : `展開其餘 ${todo.length - SHOW} 則` }}
        </button>
      </div>
    </div>
  </section>
</template>

<style scoped lang="scss">
.digest {
  padding: 10px 12px;
  margin-bottom: 12px;
  min-width: 0;
  // 沒有摘要時縮成一行
  &.slim {
    padding: 6px 12px;
    header {
      margin: 0;
    }
  }
}
header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.title {
  display: flex;
  align-items: center;
  gap: 8px;
  h3 {
    margin: 0;
    font-size: 14px;
  }
}
.date {
  font-size: 12px;
  color: var(--lc-muted);
}
.actions {
  display: flex;
  gap: 8px;
  align-items: center;
  .acc {
    width: 150px;
  }
}
.body {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 12px;
  align-items: start;
}
.left,
.right {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.sum-wrap {
  padding: 6px 10px;
  border-radius: 10px;
  background: var(--lc-bg-soft);
  display: flex;
  flex-direction: column;
  align-items: flex-start;
}
.summary {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
  line-clamp: 3;
  overflow: hidden;
  &.open {
    display: block;
    -webkit-line-clamp: unset;
    line-clamp: unset;
    max-height: 200px;
    overflow-y: auto;
  }
}
.more {
  margin-top: 0;
  padding: 0;
  border: 0;
  background: none;
  color: var(--el-color-primary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  align-self: flex-start;
  &:hover {
    text-decoration: underline;
  }
}
.sum-fail {
  align-self: flex-start;
}
.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
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
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.s-line {
  display: flex;
  align-items: baseline;
  gap: 4px;
  flex-wrap: wrap;
}
.s-value {
  font-size: 18px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.s-delta {
  font-size: 11px;
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
  gap: 4px;
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
  gap: 4px;
  &.scroll {
    max-height: 240px;
    overflow-y: auto;
  }
  li {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    padding: 4px 8px;
    border-radius: 8px;
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
.t-who {
  font-weight: 600;
  font-size: 13px;
  white-space: nowrap;
}
.t-tags {
  display: inline-flex;
  gap: 4px;
  flex: none;
}
.t-text {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  color: var(--lc-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.t-go {
  color: var(--lc-muted);
  flex: none;
  margin-left: auto;
}
.empty {
  font-size: 13px;
  color: var(--lc-muted);
  padding: 2px 0;
}
@media (max-width: 1100px) {
  .body {
    grid-template-columns: minmax(0, 1fr);
  }
}
// 手機：字級與間距放大一階；展開時整張卡片往下長，不在卡片內出現小捲動區
@media (max-width: 768px) {
  .digest {
    padding: 14px;
    &.slim {
      padding: 10px 14px;
    }
  }
  header {
    margin-bottom: 12px;
  }
  .title h3 {
    font-size: 16px;
  }
  .date {
    font-size: 13px;
  }
  .body {
    gap: 16px;
  }
  .left,
  .right {
    gap: 12px;
  }
  .sum-wrap {
    padding: 10px 12px;
  }
  .summary {
    font-size: 14px;
    line-height: 1.7;
    &.open {
      max-height: none;
      overflow: visible;
    }
  }
  .more {
    padding: 6px 0;
    font-size: 13px;
  }
  .stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px 8px;
  }
  .s-label {
    font-size: 13px;
  }
  .s-value {
    font-size: 20px;
  }
  .s-delta {
    font-size: 12px;
  }
  .todo {
    gap: 8px;
    &.scroll {
      max-height: none;
      overflow: visible;
    }
    li {
      flex-wrap: wrap;
      gap: 4px 8px;
      min-height: 44px;
      padding: 10px 12px;
    }
  }
  .t-who {
    font-size: 14px;
  }
  .t-tags {
    flex-wrap: wrap;
  }
  .t-text {
    order: 3;
    flex: 0 0 100%;
    font-size: 13px;
  }
}
</style>
