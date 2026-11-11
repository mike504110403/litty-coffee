<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Calendar, EditPen, ArrowRight } from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'
import { crmApi, type Summary } from '@/api/crm'
import { interactions, kindLabels, postsApi, type Post } from '@/api/posts'
import { flagsApi } from '@/api/flags'
import { intentLabels } from '@/api/automation'
import { delta, formatCount, formatDuration, formatPercent } from '@/utils/format'
import ColumnChart from '@/components/charts/ColumnChart.vue'
import BarList from '@/components/charts/BarList.vue'
import AccountSelect from '@/components/AccountSelect.vue'
import HelpTip from '@/components/HelpTip.vue'
import StatusPanel from '@/views/dashboard/StatusPanel.vue'
import DigestCard from '@/views/dashboard/DigestCard.vue'
import { useAccountsStore } from '@/stores/accounts'

// 總覽＝監控頁：系統狀態（所有登入者）→ 指標 → 每日摘要與圖表（分析權限）→ 貼文成效。
// 各區塊依權限顯示；沒權限的 API 不打（後端會 403）

// 已驗證的類別色（自動＝slot 1 藍、真人＝slot 2 橘）；單一系列數量圖用藍。色值在 styles/theme.scss（--lc-chart-*）
const C_AUTO = 'var(--lc-chart-1)'
const C_HUMAN = 'var(--lc-chart-2)'
const C_SINGLE = 'var(--lc-chart-1)'
// 圖表卡片固定高度內的繪圖高度與表格／清單每頁列數
const CH = 140
const PS = 5

const auth = useAuthStore()
const canAnalytics = computed(() => auth.can('analytics'))
const canSchedule = computed(() => auth.can('schedule'))
const canAutomation = computed(() => auth.can('automation'))

const greeting = computed(() => {
  const h = new Date().getHours()
  if (h < 11) return '早安'
  if (h < 18) return '午安'
  return '晚安'
})

// 帳號篩選（null＝全部帳號）與期間；統計、貼文成效、待發排程、待改進都跟著它
const accounts = useAccountsStore()
const account = ref<number | null>(null)
const days = ref(7)
const ranges = [
  { v: 1, label: '今天' },
  { v: 7, label: '7 天' },
  { v: 30, label: '30 天' },
  { v: 90, label: '90 天' },
]
const tableView = ref(false)

const data = ref<Summary | null>(null)
const loading = ref(false)
const upcoming = ref<number | null>(null)
const openReviews = ref<number | null>(null)

async function loadSummary() {
  if (!canAnalytics.value) return
  loading.value = true
  try {
    data.value = await crmApi.summary(days.value, account.value ?? undefined)
  } finally {
    loading.value = false
  }
}
async function loadCounts() {
  const acc = account.value ?? undefined
  ;[upcoming.value, openReviews.value] = await Promise.all([
    canSchedule.value ? postsApi.upcomingCount(acc) : null,
    canAutomation.value ? flagsApi.openCount(acc) : null,
  ])
}

// ---- 貼文成效：區間內已發佈、且已取得數據的貼文，依互動數排序（需要分析＋排程權限） ----
const posts = ref<Post[]>([])
const postsLoading = ref(false)
const showPosts = computed(() => canAnalytics.value && canSchedule.value)
async function loadPosts() {
  if (!showPosts.value) return
  postsLoading.value = true
  try {
    const from = new Date(Date.now() - days.value * 86400_000)
    posts.value = await postsApi.list({ status: 'published', from: from.toISOString(), account: account.value ?? undefined })
  } catch {
    posts.value = []
  } finally {
    postsLoading.value = false
  }
}

onMounted(() => {
  void accounts.ensure()
  void loadSummary()
  void loadCounts()
  void loadPosts()
})
watch([days, account], () => {
  void loadSummary()
  void loadPosts()
})
watch(account, loadCounts)

const num = (n: number | undefined) => (n == null ? '—' : formatCount(n))
const shortDate = (iso: string | null) => {
  if (!iso) return ''
  const d = new Date(iso)
  return `${d.getMonth() + 1}/${d.getDate()}`
}
const rankedPosts = computed(() =>
  posts.value
    .filter((p) => p.insightsFetchedAt)
    .sort((a, b) => interactions(b) - interactions(a) || (b.insights.views ?? 0) - (a.insights.views ?? 0))
    .slice(0, 10)
    .map((p) => ({
      id: p.id,
      thumb: p.media[0]?.kind === 'image' ? p.media[0].url : '',
      kind: kindLabels[p.kind],
      caption: p.caption || (p.kind === 'story' ? '限動' : '（無文案）'),
      date: shortDate(p.publishedAt),
      views: num(p.insights.views),
      reach: num(p.insights.reach),
      likes: num(p.insights.likes),
      comments: num(p.insights.comments ?? p.insights.replies),
      saved: num(p.insights.saved),
      shares: num(p.insights.shares),
      total: formatCount(interactions(p)),
    })),
)
const pendingPosts = computed(() => posts.value.filter((p) => !p.insightsFetchedAt).length)

interface Tile {
  label: string
  value: string
  sub?: string
  d: number | null
  upIsGood: boolean
}
const vsLabel = computed(() => (days.value === 1 ? 'vs 昨天' : `vs 前 ${days.value} 天`))
const tiles = computed<Tile[]>(() => {
  if (!data.value) return []
  const c = data.value.current
  const p = data.value.previous
  return [
    { label: '顧客訊息', value: formatCount(c.totals.inbound), d: delta(c.totals.inbound, p.totals.inbound), upIsGood: true },
    {
      label: '自動回覆占比',
      value: formatPercent(c.autoShare),
      sub: `自動 ${c.totals.outboundAuto} · 真人 ${c.totals.outboundHuman}`,
      d: delta(c.autoShare, p.autoShare),
      upIsGood: true,
    },
    {
      label: '回應時間中位數',
      value: formatDuration(c.response.medianSecs),
      sub: `平均：自動 ${formatDuration(c.response.autoAvgSecs)} · 真人 ${formatDuration(c.response.humanAvgSecs)}`,
      d: delta(c.response.medianSecs, p.response.medianSecs),
      upIsGood: false,
    },
    {
      label: '互動顧客',
      value: formatCount(c.totals.activeContacts),
      sub: `新顧客 ${c.totals.newContacts} · 需真人 ${c.totals.humanConvs}`,
      d: delta(c.totals.activeContacts, p.totals.activeContacts),
      upIsGood: true,
    },
  ]
})
const unanswered = computed(() => {
  const r = data.value?.current.response
  return r ? r.turns - r.answered : 0
})

const dailyCats = computed(() => data.value?.daily.map((d) => d.date.replace(/^(\d+)-(\d+)-(\d+)$/, '$1/$2/$3')) ?? [])
const dailyShort = computed(() => data.value?.daily.map((d) => `${+d.date.slice(5, 7)}/${+d.date.slice(8)}`) ?? [])
const intentItems = computed(
  () => data.value?.intents.map((i) => ({ label: intentLabels[i.key] ?? i.key, value: i.count })) ?? [],
)
const busiest = computed(() => {
  const h = data.value?.hours ?? []
  const max = Math.max(...h, 0)
  return max ? h.indexOf(max) : null
})
const showCounts = computed(() => canSchedule.value || canAutomation.value)
</script>

<template>
  <div v-loading="loading && !data">
    <div class="head">
      <h1 class="lc-page-title">{{ greeting }}，{{ auth.user?.displayName || auth.user?.username }}</h1>
      <div class="controls">
        <AccountSelect v-if="accounts.multi" v-model="account" allow-all class="acc-filter" />
        <template v-if="canAnalytics">
          <el-segmented v-model="days" size="small" :options="ranges.map((r) => ({ label: r.label, value: r.v }))" />
          <el-segmented
            v-model="tableView"
            size="small"
            :options="[
              { label: '圖表', value: false },
              { label: '表格', value: true },
            ]"
          />
        </template>
      </div>
    </div>

    <StatusPanel />

    <div v-if="tiles.length || showCounts" class="tiles">
      <div v-for="t in tiles" :key="t.label" class="tile lc-card">
        <div class="t-label">{{ t.label }}</div>
        <div class="t-main">
          <span class="t-value">{{ t.value }}</span>
          <span v-if="t.d != null" class="t-delta" :class="(t.d >= 0) === t.upIsGood ? 'good' : 'bad'" :title="vsLabel">
            {{ t.d >= 0 ? '▲' : '▼' }}{{ Math.abs(Math.round(t.d * 100)) }}%
          </span>
        </div>
        <div class="t-sub" :title="t.sub">{{ t.sub || vsLabel }}</div>
      </div>
      <router-link v-if="canSchedule" :to="{ name: 'schedule' }" class="tile lc-card link-tile">
        <div class="t-label"><el-icon><Calendar /></el-icon>待發排程</div>
        <div class="t-main">
          <span class="t-value">{{ upcoming == null ? '—' : upcoming }}</span>
          <el-icon class="t-arrow"><ArrowRight /></el-icon>
        </div>
      </router-link>
      <router-link
        v-if="canAutomation"
        :to="{ name: 'automation', query: { tab: 'reviews' } }"
        class="tile lc-card link-tile"
        :class="{ hot: !!openReviews }"
      >
        <div class="t-label"><el-icon><EditPen /></el-icon>待改進問答</div>
        <div class="t-main">
          <span class="t-value">{{ openReviews == null ? '—' : openReviews }}</span>
          <el-icon class="t-arrow"><ArrowRight /></el-icon>
        </div>
      </router-link>
    </div>

    <router-link v-if="canAnalytics && unanswered > 0 && auth.can('inbox')" :to="{ name: 'inbox' }" class="notice">
      {{ unanswered }} 個顧客回合還沒回覆
      <el-icon><ArrowRight /></el-icon>
    </router-link>

    <template v-if="canAnalytics">
      <DigestCard :filter="account" />

      <div class="grid">
        <template v-if="data">
          <section class="card lc-card">
            <header><h3>每日訊息</h3></header>
            <ColumnChart
              :key="`c${days}`"
              :categories="dailyCats"
              :short-labels="dailyShort"
              :series="[{ key: 'in', label: '顧客訊息', color: C_SINGLE, values: data.daily.map((d) => d.inbound) }]"
              :height="CH"
              :page-size="PS"
              :show-table="tableView"
              description="每日顧客訊息數長條圖"
            />
          </section>

          <section class="card lc-card">
            <header>
              <h3>每日回覆：自動 vs 真人<HelpTip text="自動＝關鍵字規則＋AI；真人＝後台手動回覆。" /></h3>
            </header>
            <ColumnChart
              :key="`c${days}`"
              :categories="dailyCats"
              :short-labels="dailyShort"
              :series="[
                { key: 'auto', label: '自動回覆（規則＋AI）', color: C_AUTO, values: data.daily.map((d) => d.auto) },
                { key: 'human', label: '真人', color: C_HUMAN, values: data.daily.map((d) => d.human) },
              ]"
              :height="CH"
              :show-table="tableView"
              :page-size="PS - 1"
              description="每日店家回覆數堆疊長條圖，分自動與真人"
            />
          </section>

          <section class="card lc-card">
            <header>
              <h3>顧客在問什麼<HelpTip text="AI 回覆時判斷的訊息意圖；只有 AI 處理過的訊息才有分類。" /></h3>
            </header>
            <BarList :key="`b${days}`" :items="intentItems" :color="C_SINGLE" :page-size="PS" empty-text="還沒有意圖資料" />
          </section>

          <section class="card lc-card">
            <header>
              <h3>訊息時段</h3>
              <span v-if="busiest != null" class="peak">高峰 {{ busiest }}:00</span>
            </header>
            <ColumnChart
              :key="`c${days}`"
              :categories="data.hours.map((_, h) => `${h}:00–${h + 1}:00`)"
              :short-labels="data.hours.map((_, h) => `${h}`)"
              :series="[{ key: 'h', label: '顧客訊息', color: C_SINGLE, values: data.hours }]"
              :height="CH"
              :page-size="PS"
              :show-table="tableView"
              description="一天 24 小時的顧客訊息分布長條圖"
            />
          </section>

          <section class="card lc-card">
            <header>
              <h3>最常命中的關鍵字規則<HelpTip text="累計命中次數（不受期間篩選影響）。" /></h3>
            </header>
            <BarList
              :key="`r${days}`"
              :items="data.topRules.map((r) => ({ label: r.name, value: r.hitCount }))"
              :color="C_SINGLE"
              :page-size="PS"
              empty-text="還沒有規則被命中"
            />
          </section>
        </template>
      </div>

      <section v-if="showPosts" v-loading="postsLoading" class="card lc-card posts-card">
        <header>
          <h3>
            貼文成效
            <HelpTip text="期間內已發佈的貼文，依互動數（按讚＋留言＋收藏＋分享；限動為回覆＋分享）排序，最多 10 篇。發佈後會自動更新數據，也可到「排程」按「更新數據」。" />
          </h3>
          <span v-if="pendingPosts" class="peak">{{ pendingPosts }} 篇尚無數據</span>
        </header>
        <el-table v-if="rankedPosts.length" :data="rankedPosts" size="small" class="post-table" :max-height="240">
          <el-table-column label="貼文" min-width="220">
            <template #default="{ row }">
              <div class="pc">
                <img v-if="row.thumb" :src="row.thumb" alt="" />
                <span v-else class="ph">{{ row.kind }}</span>
                <div class="pc-text">
                  <span class="pc-cap">{{ row.caption }}</span>
                  <span class="pc-sub">{{ row.date }} · {{ row.kind }}</span>
                </div>
              </div>
            </template>
          </el-table-column>
          <el-table-column prop="views" label="觀看" width="80" align="right" />
          <el-table-column prop="reach" label="觸及" width="80" align="right" />
          <el-table-column prop="likes" label="按讚" width="76" align="right" />
          <el-table-column prop="comments" label="留言" width="76" align="right" />
          <el-table-column prop="saved" label="收藏" width="76" align="right" />
          <el-table-column prop="shares" label="分享" width="76" align="right" />
          <el-table-column prop="total" label="互動" width="84" align="right" />
        </el-table>
        <div v-else-if="!postsLoading" class="posts-empty">這段期間沒有已取得成效的貼文</div>
      </section>
    </template>
  </div>
</template>

<style scoped lang="scss">
.head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
  .lc-page-title {
    margin: 0;
    font-size: 18px;
  }
}
.acc-filter {
  width: 160px;
}
.controls {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

// KPI：一排六格，數字＋標籤＋一行小字，緊湊不留白
.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 8px;
  margin-bottom: 8px;
}
.tile {
  padding: 6px 10px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 1px;
}
.t-label {
  font-size: 12px;
  color: var(--lc-muted);
  display: flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
}
.t-main {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}
.t-value {
  font-size: 19px;
  font-weight: 700;
  line-height: 1.3;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.t-delta {
  font-size: 12px;
  font-weight: 700;
  white-space: nowrap;
  &.good {
    color: var(--lc-delta-good);
  }
  &.bad {
    color: var(--lc-delta-bad);
  }
}
.t-sub {
  font-size: 11px;
  color: var(--lc-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.link-tile {
  color: inherit;
  text-decoration: none;
  transition: background 0.12s;
  &:hover {
    background: var(--lc-bg-hover);
  }
  &.hot .t-value {
    color: var(--lc-tone-rose-fg);
  }
  .t-main {
    justify-content: space-between;
    align-items: center;
  }
}
.t-arrow {
  color: var(--lc-muted);
}

.notice {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 12px;
  margin-bottom: 12px;
  border-radius: 12px;
  background: var(--lc-warn-bg);
  color: var(--lc-warn-fg);
  font-size: 13px;
  text-decoration: none;
}

// 圖表：寬螢幕第一排三張（各占 2/6）、第二排兩張（各占 3/6）
.grid {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 8px;
  > .card {
    grid-column: span 2;
    &:nth-child(n + 4) {
      grid-column: span 3;
    }
  }
}
.card {
  padding: 8px 10px 8px;
  min-width: 0;
  // 圖表卡片固定高度：表格／清單太多列時在卡片內分頁，不撐高卡片（貼文成效表格另計）
  &:not(.posts-card) {
    height: 206px;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    > :last-child {
      flex: 1;
      min-height: 0;
    }
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 8px;
    margin-bottom: 2px;
  }
  h3 {
    margin: 0;
    font-size: 13px;
    display: inline-flex;
    align-items: center;
  }
}
.peak {
  font-size: 12px;
  color: var(--lc-muted);
  white-space: nowrap;
}
.posts-card {
  margin-top: 12px;
}
.post-table {
  width: 100%;
}
.pc {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  img,
  .ph {
    width: 28px;
    height: 28px;
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
}
.pc-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
  line-height: 1.3;
}
.pc-cap {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pc-sub {
  font-size: 12px;
  color: var(--lc-muted);
}
.posts-empty {
  padding: 10px 0;
  text-align: center;
  font-size: 13px;
  color: var(--lc-muted);
}
@media (max-width: 1279px) {
  .grid > .card,
  .grid > .card:nth-child(n + 4) {
    grid-column: span 3;
  }
  .grid > .card:last-child:nth-child(odd) {
    grid-column: span 6;
  }
}
@media (max-width: 1100px) {
  .tiles {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
@media (max-width: 768px) {
  .head {
    flex-direction: column;
    align-items: stretch;
    .lc-page-title {
      display: none;
    }
  }
  .acc-filter {
    width: 100%;
  }
  .tiles {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }
  .grid {
    grid-template-columns: minmax(0, 1fr);
    gap: 10px;
    > .card,
    > .card:nth-child(n + 4),
    > .card:last-child:nth-child(odd) {
      grid-column: auto;
    }
  }
}
</style>
