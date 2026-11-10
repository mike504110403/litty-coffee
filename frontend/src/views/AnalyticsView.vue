<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { crmApi, type Summary } from '@/api/crm'
import { intentLabels } from '@/api/automation'
import ColumnChart from '@/components/charts/ColumnChart.vue'
import BarList from '@/components/charts/BarList.vue'
import { delta, formatCount, formatDuration, formatPercent } from '@/utils/format'

// 已驗證的類別色（自動＝slot 1 藍、真人＝slot 2 橘）；單一系列數量圖用藍
const C_AUTO = '#2a78d6'
const C_HUMAN = '#eb6834'
const C_SINGLE = '#2a78d6'

const days = ref(7)
const ranges = [
  { v: 7, label: '7 天' },
  { v: 30, label: '30 天' },
  { v: 90, label: '90 天' },
]
const data = ref<Summary | null>(null)
const loading = ref(false)
const tableView = ref(false)

async function load() {
  loading.value = true
  try {
    data.value = await crmApi.summary(days.value)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(days, load)

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
            :categories="dailyCats"
            :short-labels="dailyShort"
            :series="[
              { key: 'auto', label: '自動回覆（規則＋AI）', color: C_AUTO, values: data.daily.map((d) => d.auto) },
              { key: 'human', label: '真人', color: C_HUMAN, values: data.daily.map((d) => d.human) },
            ]"
            :show-table="tableView"
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
          <BarList :items="intentItems" :color="C_SINGLE" empty-text="AI 回覆過的訊息才會有意圖分類" />
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
            :items="data.topRules.map((r) => ({ label: r.name, value: r.hitCount }))"
            :color="C_SINGLE"
            empty-text="還沒有規則被命中"
          />
        </section>
      </div>
    </template>
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
    color: #006300;
  }
  &.bad {
    color: #b42318;
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
  background: #fdf3e7;
  color: #8a5a1c;
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
