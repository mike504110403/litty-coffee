<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, Plus, Loading, CircleCheck, Warning, Clock, EditPen } from '@element-plus/icons-vue'
import { kindLabels, postsApi, statusLabels, type Post, type PostStatus } from '@/api/posts'
import { crmApi } from '@/api/crm'
import { useIsMobile } from '@/composables/useIsMobile'
import PostComposer from './PostComposer.vue'
import PostInsights from './PostInsights.vue'

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

async function load() {
  loading.value = true
  try {
    if (view.value === 'calendar') {
      const end = new Date(gridStart.value)
      end.setDate(end.getDate() + 42)
      posts.value = await postsApi.list({ from: gridStart.value.toISOString(), to: end.toISOString() })
    } else {
      const from = new Date()
      from.setDate(from.getDate() - 14)
      posts.value = await postsApi.list({ from: from.toISOString() })
    }
  } finally {
    loading.value = false
  }
}
watch([month, view], load)
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
  const scheduled = posts.value.filter((p) => p.scheduledAt).sort((a, b) => a.scheduledAt!.localeCompare(b.scheduledAt!))
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
onMounted(async () => {
  try {
    const s = await crmApi.summary(30)
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
function openPost(p: Post) {
  editing.value = p
  composerOpen.value = true
}
// 更新數據後就地換掉清單與編輯器裡那篇，不必整頁重載
function onInsightsUpdated(np: Post) {
  posts.value = posts.value.map((p) => (p.id === np.id ? np : p))
  if (editing.value?.id === np.id) editing.value = np
}
const canAddOn = (d: Date) => d.getTime() >= new Date().setHours(0, 0, 0, 0)
</script>

<template>
  <div>
    <div class="head">
      <div>
        <h1 class="lc-page-title">排程</h1>
        <p class="lc-page-sub">規劃 IG 貼文、輪播、Reels 與限動，時間到自動發佈</p>
      </div>
      <div class="controls">
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
            :class="p.status"
            :title="`${hhmm(p.scheduledAt!)} ${kindLabels[p.kind]} · ${statusLabels[p.status]}`"
            @click.stop="openPost(p)"
          >
            <img v-if="p.media[0]?.kind === 'image'" :src="p.media[0].url" alt="" />
            <el-icon class="st" :class="{ spin: p.status === 'publishing' }"><component :is="statusIcon[p.status]" /></el-icon>
            <span class="t">{{ hhmm(p.scheduledAt!) }}</span>
            <span class="k">{{ kindLabels[p.kind] }}</span>
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
            <video v-if="p.media[0]?.kind === 'video'" :src="p.media[0].url" muted />
            <img v-else-if="p.media[0]" :src="p.media[0].url" alt="" />
          </div>
          <div class="ag-main">
            <div class="ag-top">
              <span class="ag-time">{{ hhmm(p.scheduledAt!) }}</span>
              <span class="ag-kind">{{ kindLabels[p.kind] }}<template v-if="p.kind === 'carousel'"> · {{ p.media.length }} 張</template></span>
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

    <PostComposer v-model="composerOpen" :post="editing" :default-date="defaultDate" :best-hours="bestHours" @saved="load" @insights-updated="onInsightsUpdated" />
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
  gap: 10px;
  align-items: center;
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
}

.agenda {
  min-height: 120px;
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
    .k {
      display: none;
    }
  }
}
</style>
