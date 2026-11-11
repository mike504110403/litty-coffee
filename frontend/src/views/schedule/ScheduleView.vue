<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, Plus, Loading, CircleCheck, Warning, Clock, EditPen, Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { kindLabels, postThumb, postsApi, statusLabels, type Post, type PostStatus } from '@/api/posts'
import { crmApi } from '@/api/crm'
import { useAuthStore } from '@/stores/auth'
import { useIsMobile } from '@/composables/useIsMobile'
import PostComposer from './PostComposer.vue'
import PostInsights from './PostInsights.vue'
import PostViewer from './PostViewer.vue'
import AccountSelect from '@/components/AccountSelect.vue'
import AccountTag from '@/components/AccountTag.vue'
import { useAccountsStore } from '@/stores/accounts'

const isMobile = useIsMobile()
const view = ref<'calendar' | 'list'>(isMobile.value ? 'list' : 'calendar')

// ---- 月份 ----
const today = new Date()
const month = ref(new Date(today.getFullYear(), today.getMonth(), 1))
const monthLabel = computed(() => `${month.value.getFullYear()} 年 ${month.value.getMonth() + 1} 月`)
const shiftMonth = (d: number) => (month.value = new Date(month.value.getFullYear(), month.value.getMonth() + d, 1))

// 月曆格：從該月第一天所在週的週日開始，6 週
const gridStart = computed(() => {
  const d = new Date(month.value)
  d.setDate(1 - d.getDay())
  return d
})
const days = computed(() =>
  Array.from({ length: 42 }, (_, i) => {
    const d = new Date(gridStart.value)
    d.setDate(d.getDate() + i)
    return d
  }),
)
const dayKey = (d: Date) => `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
const isToday = (d: Date) => dayKey(d) === dayKey(today)

// ---- 資料 ----
const posts = ref<Post[]>([])
const loading = ref(false)
// 帳號篩選（null＝全部帳號）
const accounts = useAccountsStore()
const accountFilter = ref<number | null>(null)
onMounted(() => void accounts.ensure())

async function load() {
  loading.value = true
  try {
    if (view.value === 'calendar') {
      const end = new Date(gridStart.value)
      end.setDate(end.getDate() + 42)
      posts.value = await postsApi.list({
        from: gridStart.value.toISOString(),
        to: end.toISOString(),
        account: accountFilter.value ?? undefined,
      })
    } else {
      const from = new Date()
      from.setDate(from.getDate() - 14)
      posts.value = await postsApi.list({ from: from.toISOString(), account: accountFilter.value ?? undefined })
    }
  } finally {
    loading.value = false
  }
}
watch([month, view, accountFilter], load)
onMounted(load)

// 有發佈中或快到期的貼文時輪詢，狀態會自己更新
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  timer = setInterval(() => {
    const soon = Date.now() + 2 * 60_000
    if (posts.value.some((p) => p.status === 'publishing' || (p.status === 'scheduled' && new Date(p.scheduledAt!).getTime() < soon)))
      void load()
  }, 10_000)
})
onBeforeUnmount(() => clearInterval(timer))

const drafts = computed(() => posts.value.filter((p) => !p.scheduledAt))
const byDay = computed(() => {
  const m = new Map<string, Post[]>()
  for (const p of posts.value) {
    if (!p.scheduledAt) continue
    const k = dayKey(new Date(p.scheduledAt))
    m.set(k, [...(m.get(k) ?? []), p])
  }
  return m
})

// 清單：依日期分組
const agenda = computed(() => {
  const groups: { label: string; items: Post[] }[] = []
  // 倒序：最新（最晚）的日期在最上面
  const scheduled = posts.value.filter((p) => p.scheduledAt).sort((a, b) => b.scheduledAt!.localeCompare(a.scheduledAt!))
  for (const p of scheduled) {
    const d = new Date(p.scheduledAt!)
    const label = `${d.getMonth() + 1} 月 ${d.getDate()} 日（${'日一二三四五六'[d.getDay()]}）${isToday(d) ? '· 今天' : ''}`
    if (!groups.length || groups[groups.length - 1].label !== label) groups.push({ label, items: [] })
    groups[groups.length - 1].items.push(p)
  }
  return groups
})

const hhmm = (iso: string) => {
  const d = new Date(iso)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}
const statusIcon: Record<PostStatus, unknown> = {
  draft: EditPen,
  scheduled: Clock,
  publishing: Loading,
  published: CircleCheck,
  failed: Warning,
}

// ---- 最佳時段（依近 30 天顧客傳訊時段，取前三）----
const bestHours = ref<number[]>([])
const auth = useAuthStore()
onMounted(async () => {
  if (!auth.can('analytics')) return // 熱門時段來自分析資料
  try {
    const s = await crmApi.summary(30, accountFilter.value ?? undefined)
    bestHours.value = s.hours
      .map((n, h) => ({ n, h }))
      .filter((x) => x.n > 0)
      .sort((a, b) => b.n - a.n)
      .slice(0, 3)
      .map((x) => x.h)
      .sort((a, b) => a - b)
  } catch {
    bestHours.value = []
  }
})

// ---- 編輯器 ----
const composerOpen = ref(false)
const editing = ref<Post | null>(null)
const defaultDate = ref<Date | null>(null)

function openNew(day?: Date) {
  editing.value = null
  if (day) {
    const d = new Date(day)
    d.setHours(bestHours.value[0] ?? 12, 0, 0, 0)
    defaultDate.value = d.getTime() > Date.now() ? d : null
  } else defaultDate.value = null
  composerOpen.value = true
}
// 從 IG 同步進來的貼文（source='ig'）只能檢視；後台發的才進編輯器
const viewerOpen = ref(false)
const viewing = ref<Post | null>(null)
function openPost(p: Post) {
  if (p.source === 'ig') {
    viewing.value = p
    viewerOpen.value = true
    return
  }
  editing.value = p
  composerOpen.value = true
}
// 更新數據後就地換掉清單、編輯器與檢視裡那篇，不必整頁重載
function onInsightsUpdated(np: Post) {
  posts.value = posts.value.map((p) => (p.id === np.id ? np : p))
  if (editing.value?.id === np.id) editing.value = np
  if (viewing.value?.id === np.id) viewing.value = np
}

// ---- 同步 IG 貼文：選了帳號就同步該帳號，「全部帳號」逐一同步；每帳號 1 分鐘冷卻（後端 429） ----
const syncing = ref(false)
const cooldownUntil = ref<Record<number, number>>({}) // 帳號 id → 可再同步的時間（ms）
const nowTick = ref(Date.now())
let cdTimer: ReturnType<typeof setInterval> | undefined
const syncTargets = computed(() =>
  accountFilter.value != null ? [accountFilter.value] : accounts.list.filter((a) => a.active).map((a) => a.id),
)
// 目前要同步的帳號都在冷卻時，回傳最短剩餘秒數；否則 0
const cooldownLeft = computed(() => {
  const left = syncTargets.value.map((id) => Math.ceil(((cooldownUntil.value[id] ?? 0) - nowTick.value) / 1000))
  return left.length && left.every((n) => n > 0) ? Math.min(...left) : 0
})
function startCooldownTicker() {
  nowTick.value = Date.now()
  if (cdTimer) return
  cdTimer = setInterval(() => {
    nowTick.value = Date.now()
    if (Object.values(cooldownUntil.value).every((t) => t <= nowTick.value)) {
      clearInterval(cdTimer)
      cdTimer = undefined
    }
  }, 1000)
}
onBeforeUnmount(() => clearInterval(cdTimer))

async function syncIg() {
  if (syncing.value || cooldownLeft.value > 0) return
  const targets = syncTargets.value.filter((id) => (cooldownUntil.value[id] ?? 0) <= Date.now())
  if (!targets.length) {
    ElMessage.info('沒有可同步的帳號')
    return
  }
  syncing.value = true
  let nPosts = 0
  let nStories = 0
  const succeeded: number[] = []
  const problems: string[] = []
  try {
    for (const id of targets) {
      const r = await postsApi.syncMedia(id)
      const who = accounts.multi ? `${accounts.label(id)}：` : ''
      if (r.ok) {
        nPosts += r.posts
        nStories += r.stories
        succeeded.push(id)
        continue
      }
      if (r.status === 429) {
        cooldownUntil.value = { ...cooldownUntil.value, [id]: Date.now() + r.retryAfter * 1000 }
        startCooldownTicker()
        problems.push(`${who}剛同步過，請 ${r.retryAfter} 秒後再試`)
      } else if (r.status === 409) problems.push(`${who}${r.error || '尚未連結 IG（沒有可用的 token）'}`)
      else problems.push(`${who}${r.error || 'IG 暫時無法回應，請稍後再試'}`)
    }
  } finally {
    syncing.value = false
  }
  if (succeeded.length) {
    ElMessage.success(`同步完成：新增／更新 ${nPosts} 篇、限動 ${nStories} 則`)
    // 同步成功的帳號也進 1 分鐘冷卻（對齊後端），避免連點
    const next = { ...cooldownUntil.value }
    for (const id of succeeded) if ((next[id] ?? 0) <= Date.now()) next[id] = Date.now() + 60_000
    cooldownUntil.value = next
    startCooldownTicker()
    await load()
  }
  if (problems.length) ElMessage({ type: 'warning', message: problems.join('；'), duration: 6000 })
}
const canAddOn = (d: Date) => d.getTime() >= new Date().setHours(0, 0, 0, 0)
</script>

<template>
  <div>
    <div class="head">
      <div>
        <h1 class="lc-page-title lc-page-title--solo">排程</h1>
      </div>
      <div class="controls">
        <div class="ctl-filter">
          <AccountSelect v-if="accounts.multi" v-model="accountFilter" allow-all class="acc-filter" />
          <el-button
            class="sync-btn"
            :icon="Refresh"
            :loading="syncing"
            :disabled="cooldownLeft > 0 || !accounts.list.length"
            @click="syncIg"
          >
            <span class="sync-text">{{ cooldownLeft > 0 ? `${cooldownLeft} 秒後可再同步` : '同步 IG 貼文' }}</span>
          </el-button>
        </div>
        <el-segmented
          v-model="view"
          :options="[
            { label: '月曆', value: 'calendar' },
            { label: '清單', value: 'list' },
          ]"
        />
        <el-button type="primary" :icon="Plus" @click="openNew()">新增貼文</el-button>
      </div>
    </div>

    <div v-if="drafts.length" class="drafts lc-card">
      <span class="drafts-title">草稿 {{ drafts.length }}</span>
      <button v-for="p in drafts" :key="p.id" class="draft" @click="openPost(p)">
        <img v-if="p.media[0]?.kind === 'image'" :src="p.media[0].url" alt="" />
        <span v-else class="ph">{{ kindLabels[p.kind] }}</span>
        <span class="cap">{{ p.caption || '（無文案）' }}</span>
        <AccountTag v-if="accounts.multi" :id="p.igAccountId" compact />
      </button>
    </div>

    <!-- 月曆 -->
    <div v-if="view === 'calendar'" v-loading="loading" class="cal lc-card">
      <div class="cal-head">
        <el-button text circle :icon="ArrowLeft" aria-label="上個月" @click="shiftMonth(-1)" />
        <strong>{{ monthLabel }}</strong>
        <el-button text circle :icon="ArrowRight" aria-label="下個月" @click="shiftMonth(1)" />
        <el-button text size="small" @click="month = new Date(today.getFullYear(), today.getMonth(), 1)">今天</el-button>
      </div>
      <div class="weekdays">
        <span v-for="w in '日一二三四五六'" :key="w">{{ w }}</span>
      </div>
      <div class="grid">
        <div
          v-for="d in days"
          :key="dayKey(d)"
          class="day"
          :class="{ other: d.getMonth() !== month.getMonth(), today: isToday(d), past: !canAddOn(d) }"
          @click="canAddOn(d) && openNew(d)"
        >
          <span class="date">{{ d.getDate() }}</span>
          <button
            v-for="p in byDay.get(dayKey(d)) ?? []"
            :key="p.id"
            class="chip"
            :class="[p.status, { ig: p.source === 'ig' }]"
            :title="`${hhmm(p.scheduledAt!)} ${kindLabels[p.kind]} · ${p.source === 'ig' ? 'IG 發佈' : statusLabels[p.status]} · ${accounts.label(p.igAccountId)}`"
            @click.stop="openPost(p)"
          >
            <img v-if="postThumb(p)" :src="postThumb(p)" alt="" />
            <el-icon class="st" :class="{ spin: p.status === 'publishing' }"><component :is="statusIcon[p.status]" /></el-icon>
            <span class="t">{{ hhmm(p.scheduledAt!) }}</span>
            <span class="k">{{ kindLabels[p.kind] }}</span>
            <span v-if="p.source === 'ig'" class="ig-mark">IG</span>
            <AccountTag v-if="accounts.multi" :id="p.igAccountId" compact class="acc" />
          </button>
        </div>
      </div>
    </div>

    <!-- 清單 -->
    <div v-else v-loading="loading" class="agenda">
      <template v-for="g in agenda" :key="g.label">
        <div class="ag-day">{{ g.label }}</div>
        <div
          v-for="p in g.items"
          :key="p.id"
          class="ag-item lc-card"
          role="button"
          tabindex="0"
          @click="openPost(p)"
          @keydown.enter.self="openPost(p)"
        >
          <div class="ag-thumb">
            <img v-if="p.source === 'ig' && p.igThumb" :src="p.igThumb" alt="" />
            <video v-else-if="p.media[0]?.kind === 'video'" :src="p.media[0].url" muted />
            <img v-else-if="p.media[0]" :src="p.media[0].url" alt="" />
          </div>
          <div class="ag-main">
            <div class="ag-top">
              <span class="ag-time">{{ hhmm(p.scheduledAt!) }}</span>
              <span class="ag-kind">{{ kindLabels[p.kind] }}<template v-if="p.kind === 'carousel' && p.media.length"> · {{ p.media.length }} 張</template></span>
              <span v-if="p.source === 'ig'" class="ig-tag">IG 發佈</span>
              <AccountTag v-if="accounts.multi" :id="p.igAccountId" />
              <span class="badge" :class="p.status">
                <el-icon :class="{ spin: p.status === 'publishing' }"><component :is="statusIcon[p.status]" /></el-icon>
                {{ statusLabels[p.status] }}
              </span>
            </div>
            <p class="ag-cap">{{ p.caption || (p.kind === 'story' ? '限動' : '（無文案）') }}</p>
            <p v-if="p.error && p.status !== 'published'" class="ag-err">{{ p.error }}</p>
            <PostInsights v-if="p.status === 'published'" :post="p" compact @updated="onInsightsUpdated" />
          </div>
        </div>
      </template>
      <el-empty v-if="!loading && !agenda.length" :image-size="100" description="還沒有排程的貼文">
        <el-button type="primary" :icon="Plus" @click="openNew()">新增第一篇</el-button>
      </el-empty>
    </div>

    <PostViewer v-model="viewerOpen" :post="viewing" @insights-updated="onInsightsUpdated" />
    <PostComposer
      v-model="composerOpen"
      :post="editing"
      :default-date="defaultDate"
      :default-account="accountFilter"
      :best-hours="bestHours"
      @saved="load"
      @insights-updated="onInsightsUpdated"
    />
  </div>
</template>

<style scoped lang="scss">
.head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 12px;
}
.controls {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 10px;
  align-items: center;
}
.ctl-filter {
  display: flex;
  gap: 10px;
  align-items: center;
  min-width: 0;
}
.drafts {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  margin-bottom: 14px;
  overflow-x: auto;
  .drafts-title {
    flex-shrink: 0;
    font-size: 13px;
    font-weight: 500;
    color: var(--lc-muted);
  }
}
.draft {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  max-width: 220px;
  padding: 4px 10px 4px 4px;
  border: 1px dashed var(--lc-border);
  border-radius: 10px;
  background: var(--lc-surface);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  img,
  .ph {
    width: 32px;
    height: 32px;
    border-radius: 6px;
    object-fit: cover;
    flex-shrink: 0;
  }
  .ph {
    display: grid;
    place-items: center;
    background: var(--lc-cream);
    font-size: 10px;
  }
  .cap {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}

.cal {
  padding: 12px;
}
.cal-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 8px;
  strong {
    min-width: 110px;
    text-align: center;
  }
}
.weekdays,
.grid {
  display: grid;
  grid-template-columns: repeat(7, 1fr);
}
.weekdays span {
  text-align: center;
  font-size: 12px;
  color: var(--lc-muted);
  padding: 4px 0;
}
.grid {
  border-top: 1px solid var(--lc-border);
  border-left: 1px solid var(--lc-border);
}
.day {
  min-height: 108px;
  min-width: 0;
  padding: 4px;
  border-right: 1px solid var(--lc-border);
  border-bottom: 1px solid var(--lc-border);
  display: flex;
  flex-direction: column;
  gap: 3px;
  cursor: pointer;
  transition: background 0.12s;
  &:hover:not(.past) {
    background: var(--lc-bg-hover-3);
  }
  &.other .date {
    color: var(--lc-faint);
  }
  &.past {
    cursor: default;
    background: var(--lc-bg-past);
  }
  &.today .date {
    background: var(--lc-espresso);
    color: var(--lc-on-fill);
  }
}
.date {
  align-self: flex-start;
  min-width: 22px;
  height: 22px;
  padding: 0 4px;
  border-radius: 11px;
  font-size: 12px;
  display: grid;
  place-items: center;
}
.chip {
  display: flex;
  align-items: center;
  gap: 4px;
  width: 100%;
  min-width: 0;
  padding: 2px 6px 2px 2px;
  border: 0;
  border-radius: 6px;
  font: inherit;
  font-size: 11px;
  text-align: left;
  cursor: pointer;
  background: var(--lc-info-bg);
  color: var(--lc-text);
  img {
    width: 18px;
    height: 18px;
    border-radius: 4px;
    object-fit: cover;
    flex-shrink: 0;
  }
  .st {
    flex-shrink: 0;
  }
  .t {
    font-variant-numeric: tabular-nums;
    font-weight: 500;
  }
  .k {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--lc-muted);
  }
  &.published {
    background: var(--lc-ok-bg);
  }
  &.failed {
    background: var(--lc-fail-bg);
  }
  &.publishing {
    background: var(--lc-warn-bg);
  }
  &.ig {
    background: var(--lc-tone-caramel-bg);
  }
}
.ig-mark {
  flex-shrink: 0;
  padding: 0 4px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 700;
  line-height: 14px;
  background: var(--lc-tone-caramel-fg);
  color: var(--lc-on-fill);
}
.ig-tag {
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 12px;
  background: var(--lc-tone-caramel-bg);
  color: var(--lc-tone-caramel-fg);
}

.agenda {
  min-height: 120px;
}
@media (min-width: 1024px) {
  // 桌機清單：每天一個標題，底下的貼文兩欄並排
  .agenda {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px 12px;
    align-items: start;
    .ag-day,
    :deep(.el-empty) {
      grid-column: 1 / -1;
    }
    .ag-day {
      margin: 12px 0 0;
    }
    .ag-item {
      margin-bottom: 0;
    }
  }
}
.ag-day {
  font-size: 13px;
  font-weight: 700;
  margin: 16px 0 8px;
  &:first-child {
    margin-top: 0;
  }
}
.ag-item {
  width: 100%;
  display: flex;
  gap: 12px;
  padding: 10px;
  margin-bottom: 8px;
  text-align: left;
  font: inherit;
  color: inherit;
  cursor: pointer;
}
.ag-thumb {
  width: 64px;
  height: 64px;
  border-radius: 10px;
  overflow: hidden;
  flex-shrink: 0;
  background: var(--lc-cream);
  img,
  video {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
}
.ag-main {
  flex: 1;
  min-width: 0;
}
.ag-top {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.ag-time {
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.ag-kind {
  font-size: 12px;
  color: var(--lc-muted);
}
.ag-cap {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--lc-muted);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.ag-err {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--lc-fail-fg);
}
.badge {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--lc-cream);
  &.scheduled {
    background: var(--lc-info-bg);
    color: var(--lc-info-fg);
  }
  &.published {
    background: var(--lc-ok-bg);
    color: var(--lc-ok-fg);
  }
  &.failed {
    background: var(--lc-fail-bg);
    color: var(--lc-fail-fg);
  }
  &.publishing {
    background: var(--lc-warn-bg);
    color: var(--lc-warn-fg);
  }
}
.spin {
  animation: spin 1s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 768px) {
  .head {
    flex-direction: column;
    .lc-page-title {
      display: none;
    }
    .lc-page-sub {
      margin-bottom: 8px;
    }
  }
  .controls {
    width: 100%;
    justify-content: space-between;
    margin-bottom: 12px;
  }
  .ctl-filter {
    width: 100%;
    .acc-filter {
      flex: 1;
      min-width: 0;
      width: auto;
    }
    .sync-btn {
      flex: none;
      min-height: 40px;
    }
    // 只有一個帳號、沒有篩選器時，同步鈕撐滿一列
    .sync-btn:only-child {
      flex: 1;
    }
  }
  .cal {
    padding: 8px 4px;
  }
  .day {
    min-height: 64px;
    padding: 2px;
  }
  .chip {
    padding: 2px;
    justify-content: center;
    .t,
    .k,
    .ig-mark {
      display: none;
    }
  }
}
.acc-filter {
  width: 170px;
}
.chip .acc {
  margin-left: auto;
}
</style>
