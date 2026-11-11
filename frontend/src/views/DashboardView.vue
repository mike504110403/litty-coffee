<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ChatDotRound, MagicStick, Timer, Calendar } from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'
import { crmApi, type Summary } from '@/api/crm'
import { postsApi } from '@/api/posts'
import { formatCount, formatDuration, formatPercent } from '@/utils/format'

const auth = useAuthStore()

const greeting = computed(() => {
  const h = new Date().getHours()
  if (h < 11) return '早安'
  if (h < 18) return '午安'
  return '晚安'
})

const today = ref<Summary | null>(null)
const upcoming = ref<number | null>(null)
onMounted(async () => {
  ;[today.value, upcoming.value] = await Promise.all([crmApi.summary(1), postsApi.upcomingCount()])
})

const stats = computed(() => {
  const c = today.value?.current
  return [
    { label: '今日顧客訊息', value: c ? formatCount(c.totals.inbound) : '—', icon: ChatDotRound, tone: 'caramel' },
    { label: '今日自動回覆占比', value: formatPercent(c?.autoShare), icon: MagicStick, tone: 'roast' },
    { label: '今日回應時間中位數', value: formatDuration(c?.response.medianSecs), icon: Timer, tone: 'sage' },
    { label: '待發排程', value: upcoming.value == null ? '—' : String(upcoming.value), icon: Calendar, tone: 'rose' },
  ]
})

const steps = [
  { title: '連結 IG 商家帳號', desc: '在「設定」授權 Instagram，開始接收私訊' },
  { title: '建立店家知識庫', desc: '菜單、營業時間、地址，讓 AI 回答更準' },
  { title: '設定關鍵字回覆', desc: '例如「菜單」「訂位」自動回應' },
]
</script>

<template>
  <div>
    <h1 class="lc-page-title">{{ greeting }}，{{ auth.user?.displayName || auth.user?.username }}</h1>
    <p class="lc-page-sub">今天店裡的 IG 狀況一覽</p>

    <div class="stats">
      <div v-for="s in stats" :key="s.label" class="stat lc-card">
        <div class="stat-icon" :class="s.tone">
          <el-icon :size="20"><component :is="s.icon" /></el-icon>
        </div>
        <div class="stat-value">{{ s.value }}</div>
        <div class="stat-label">{{ s.label }}</div>
      </div>
    </div>

    <div class="onboard lc-card">
      <h3>開始設定</h3>
      <ol>
        <li v-for="(st, i) in steps" :key="st.title">
          <span class="num">{{ i + 1 }}</span>
          <div>
            <div class="st-title">{{ st.title }}</div>
            <div class="st-desc">{{ st.desc }}</div>
          </div>
        </li>
      </ol>
    </div>
  </div>
</template>

<style scoped lang="scss">
.stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
  margin-bottom: 20px;
}
.stat {
  padding: 18px;
}
.stat-icon {
  width: 38px;
  height: 38px;
  border-radius: 12px;
  display: grid;
  place-items: center;
  margin-bottom: 14px;
  &.caramel {
    background: var(--lc-tone-caramel-bg);
    color: var(--lc-tone-caramel-fg);
  }
  &.roast {
    background: var(--lc-tone-roast-bg);
    color: var(--lc-roast);
  }
  &.sage {
    background: var(--lc-tone-sage-bg);
    color: var(--lc-tone-sage-fg);
  }
  &.rose {
    background: var(--lc-tone-rose-bg);
    color: var(--lc-tone-rose-fg);
  }
}
.stat-value {
  font-size: 26px;
  font-weight: 700;
}
.stat-label {
  margin-top: 2px;
  font-size: 13px;
  color: var(--lc-muted);
}

.onboard {
  padding: 20px 22px;
  h3 {
    margin: 0 0 14px;
    font-size: 16px;
  }
  ol {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 14px;
  }
  li {
    display: flex;
    gap: 14px;
    align-items: flex-start;
  }
  .num {
    flex-shrink: 0;
    width: 28px;
    height: 28px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    font-size: 13px;
    font-weight: 700;
    background: var(--lc-num-bg);
    color: var(--lc-num-fg);
  }
  .st-title {
    font-weight: 500;
  }
  .st-desc {
    font-size: 13px;
    color: var(--lc-muted);
    margin-top: 2px;
  }
}

@media (max-width: 1024px) {
  .stats {
    grid-template-columns: repeat(2, 1fr);
  }
}
@media (max-width: 768px) {
  .stats {
    gap: 12px;
  }
  .stat {
    padding: 14px;
  }
  .stat-value {
    font-size: 22px;
  }
}
</style>
