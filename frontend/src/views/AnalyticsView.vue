<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { crmApi, type Summary } from '@/api/crm'
import { useAuthStore } from '@/stores/auth'
import { interactions, kindLabels, postsApi, type Post } from '@/api/posts'
import { intentLabels } from '@/api/automation'
import ColumnChart from '@/components/charts/ColumnChart.vue'
import BarList from '@/components/charts/BarList.vue'
import AccountSelect from '@/components/AccountSelect.vue'
import { useAccountsStore } from '@/stores/accounts'
import { delta, formatCount, formatDuration, formatPercent } from '@/utils/format'

// 已驗證的類別色（自動＝slot 1 藍、真人＝slot 2 橘）；單一系列數量圖用藍
// 色值定義在 styles/theme.scss（--lc-chart-*），淺／深色各有一組已驗證的階
const C_AUTO = 'var(--lc-chart-1)'
const C_HUMAN = 'var(--lc-chart-2)'
const C_SINGLE = 'var(--lc-chart-1)'

const days = ref(7)
const ranges = [
  { v: 7, label: '7 天' },
  { v: 30, label: '30 天' },
  { v: 90, label: '90 天' },
]
// 帳號篩選（null＝全部帳號）；統計與貼文成效都跟著它
const accounts = useAccountsStore()
const account = ref<number | null>(null)
onMounted(() => void accounts.ensure())
const data = ref<Summary | null>(null)
const loading = ref(false)
const tableView = ref(false)

async function load() {
  loading.value = true
  try {
    data.value = await crmApi.summary(days.value, account.value ?? undefined)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch([days, account], load)

// ---- 貼文成效：區間內已發佈、且已取得數據的貼文，依互動數排序 ----
const posts = ref<Post[]>([])
const postsLoading = ref(false)
const auth = useAuthStore()
async function loadPosts() {
  if (!auth.can('schedule')) return // 貼文資料需要排程權限
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
onMounted(loadPosts)
watch([days, account], loadPosts)

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
      sub: `自動 ${formatDuration(c.response.autoAvgSecs)} · 真人 ${formatDuration(c.response.humanAvgSecs)}（平均）`,
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
</script>

<template>
  <div v-loading="loading && !data">
    <div class="head">
      <div>
        <h1 class="lc-page-title">分析</h1>
        <p class="lc-page-sub">IG 私訊的量、自動化程度與回應速度</p>
      </div>
      <div class="controls">
        <AccountSelect v-if="accounts.multi" v-model="account" allow-all class="acc-filter" />
        <el-segmented v-model="days" :options="ranges.map((r) => ({ label: r.label, value: r.v }))" />
        <el-segmented
          v-model="tableView"
          :options="[
            { label: '圖表', value: false },
            { label: '表格', value: true },
          ]"
        />
      </div>
    </div>

    <template v-if="data">
      <div class="tiles">
        <div v-for="t in tiles" :key="t.label" class="tile lc-card">
          <div class="t-label">{{ t.label }}</div>
          <div class="t-value">{{ t.value }}</div>
          <div class="t-foot">
            <span
              v-if="t.d != null"
              class="t-delta"
              :class="(t.d >= 0) === t.upIsGood ? 'good' : 'bad'"
            >
              {{ t.d >= 0 ? '▲' : '▼' }} {{ Math.abs(Math.round(t.d * 100)) }}%
            </span>
            <span v-if="t.d != null" class="vs">vs 前 {{ days }} 天</span>
          </div>
          <div v-if="t.sub" class="t-sub">{{ t.sub }}</div>
        </div>
      </div>

      <div v-if="unanswered > 0" class="notice">
        有 {{ unanswered }} 個顧客回合還沒有任何回覆，記得到收件匣看看。
      </div>

      <div class="grid">
        <section class="card lc-card wide">
          <header>
            <div>
              <h3>每日訊息</h3>
              <p>顧客傳來的訊息數</p>
            </div>
          </header>
          <ColumnChart
            :key="`c${days}`"
            :categories="dailyCats"
            :short-labels="dailyShort"
            :series="[{ key: 'in', label: '顧客訊息', color: C_SINGLE, values: data.daily.map((d) => d.inbound) }]"
            :show-table="tableView"
            description="每日顧客訊息數長條圖"
          />
        </section>

        <section class="card lc-card wide">
          <header>
            <div>
              <h3>每日回覆：自動 vs 真人</h3>
              <p>店家送出的訊息由誰負責</p>
            </div>
          </header>
          <ColumnChart
            :key="`c${days}`"
            :categories="dailyCats"
            :short-labels="dailyShort"
            :series="[
              { key: 'auto', label: '自動回覆（規則＋AI）', color: C_AUTO, values: data.daily.map((d) => d.auto) },
              { key: 'human', label: '真人', color: C_HUMAN, values: data.daily.map((d) => d.human) },
            ]"
            :show-table="tableView"
            :page-size="6"
            description="每日店家回覆數堆疊長條圖，分自動與真人"
          />
        </section>

        <section class="card lc-card">
          <header>
            <div>
              <h3>顧客在問什麼</h3>
              <p>AI 判斷的訊息意圖</p>
            </div>
          </header>
          <BarList :key="`b${days}`" :items="intentItems" :color="C_SINGLE" empty-text="AI 回覆過的訊息才會有意圖分類" />
        </section>

        <section class="card lc-card">
          <header>
            <div>
              <h3>訊息時段</h3>
              <p>
                顧客最常在
                <b v-if="busiest != null">{{ busiest }}:00–{{ busiest + 1 }}:00</b>
                <template v-else>—</template>
                傳訊
              </p>
            </div>
          </header>
          <ColumnChart
            :key="`c${days}`"
            :categories="data.hours.map((_, h) => `${h}:00–${h + 1}:00`)"
            :short-labels="data.hours.map((_, h) => `${h}`)"
            :series="[{ key: 'h', label: '顧客訊息', color: C_SINGLE, values: data.hours }]"
            :height="150"
            :show-table="tableView"
            description="一天 24 小時的顧客訊息分布長條圖"
          />
        </section>

        <section class="card lc-card">
          <header>
            <div>
              <h3>最常命中的關鍵字規則</h3>
              <p>累計命中次數</p>
            </div>
          </header>
          <BarList
            :key="`r${days}`"
            :items="data.topRules.map((r) => ({ label: r.name, value: r.hitCount }))"
            :color="C_SINGLE"
            empty-text="還沒有規則被命中"
          />
        </section>
      </div>
    </template>

    <section v-loading="postsLoading" class="card lc-card posts-card">
      <header>
        <div>
          <h3>貼文成效</h3>
          <p>近 {{ days }} 天已發佈的貼文，依互動數（按讚＋留言＋收藏＋分享；限動為回覆＋分享）排序，最多 10 篇</p>
        </div>
      </header>
      <el-table v-if="rankedPosts.length" :data="rankedPosts" size="small" class="post-table">
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
      <div v-else-if="!postsLoading" class="posts-empty">這段期間還沒有已取得成效的貼文</div>
      <p v-if="pendingPosts" class="posts-note">另有 {{ pendingPosts }} 篇尚未取得數據（發佈後會自動更新，也可到「排程」按「更新數據」）。</p>
    </section>
  </div>
</template>

<style scoped lang="scss">
.head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 12px;
}
.acc-filter {
  width: 170px;
}
.controls {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.tiles {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
  margin-bottom: 14px;
}
.tile {
  padding: 16px 18px;
}
.t-label {
  font-size: 13px;
  color: var(--lc-muted);
}
.t-value {
  font-size: 28px;
  font-weight: 700;
  margin: 4px 0 2px;
  font-variant-numeric: tabular-nums;
}
.t-foot {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  min-height: 18px;
}
.t-delta {
  font-weight: 700;
  &.good {
    color: var(--lc-delta-good);
  }
  &.bad {
    color: var(--lc-delta-bad);
  }
}
.vs {
  color: var(--lc-muted);
}
.t-sub {
  font-size: 12px;
  color: var(--lc-muted);
  margin-top: 4px;
}
.notice {
  padding: 10px 14px;
  margin-bottom: 14px;
  border-radius: 12px;
  background: var(--lc-warn-bg);
  color: var(--lc-warn-fg);
  font-size: 13px;
}
.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
}
.card {
  padding: 16px 18px 18px;
  min-width: 0;
  // 圖表卡片固定高度：表格／清單太多列時在卡片內分頁，不撐高卡片（貼文成效表格另計）
  &:not(.posts-card) {
    height: 360px;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    > :last-child {
      flex: 1;
      min-height: 0;
    }
  }
  &.wide {
    grid-column: span 1;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 8px;
    margin-bottom: 12px;
  }
  h3 {
    margin: 0;
    font-size: 15px;
  }
  p {
    margin: 2px 0 0;
    font-size: 12px;
    color: var(--lc-muted);
    b {
      color: var(--lc-text);
    }
  }
}
.posts-card {
  margin-top: 14px;
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
    width: 36px;
    height: 36px;
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
  padding: 18px 0;
  text-align: center;
  font-size: 13px;
  color: var(--lc-muted);
}
.posts-note {
  margin: 10px 0 0;
  font-size: 12px;
  color: var(--lc-muted);
}
@media (max-width: 1100px) {
  .tiles {
    grid-template-columns: repeat(2, 1fr);
  }
  .grid {
    grid-template-columns: 1fr;
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
    margin-bottom: 14px;
  }
  .tiles {
    gap: 10px;
  }
  .tile {
    padding: 12px 14px;
  }
  .t-value {
    font-size: 22px;
  }
}
</style>
