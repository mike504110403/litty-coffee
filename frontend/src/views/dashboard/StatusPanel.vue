<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Cpu, Coin, Operation } from '@element-plus/icons-vue'
import { systemApi, type AccountStatus, type Light, type LightStatus, type SystemStatus } from '@/api/system'
import { automationApi } from '@/api/automation'
import { useSystemStore } from '@/stores/system'
import { useAccountsStore } from '@/stores/accounts'
import AccountAvatar from '@/components/AccountAvatar.vue'

// 系統狀態面板：每個 IG 帳號一顆燈（連線、私訊、最後收到、自動回覆開關），另有 Claude／背景工作／資料庫。
// 每 30 秒重抓；收到 system.alert 立即重抓。詳細資訊在 hover（觸控＝點一下）的浮層裡。
const POLL_MS = 30_000

const status = ref<SystemStatus | null>(null)
const failed = ref(false)
const toggling = ref<Record<number, boolean>>({})
const accounts = useAccountsStore()
const system = useSystemStore()

async function load() {
  try {
    status.value = await systemApi.status()
    failed.value = false
  } catch {
    failed.value = true
  }
}

let timer: ReturnType<typeof setInterval> | null = null
let offAlert: (() => void) | null = null
const now = ref(Date.now())
onMounted(() => {
  void accounts.ensure()
  void load()
  timer = setInterval(() => {
    now.value = Date.now()
    if (document.visibilityState === 'visible') void load()
  }, POLL_MS)
  offAlert = system.onAlert(() => void load())
})
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
  offAlert?.()
})

const canHover = typeof window !== 'undefined' && window.matchMedia('(hover: hover)').matches
const trigger = canHover ? 'hover' : 'click'

const statusLabel: Record<LightStatus, string> = { green: '正常', yellow: '需要注意', red: '異常' }

function ago(iso: string | null | undefined): string {
  if (!iso) return '—'
  const s = Math.max(0, (now.value - new Date(iso).getTime()) / 1000)
  if (s < 60) return '剛剛'
  if (s < 3600) return `${Math.floor(s / 60)} 分前`
  if (s < 86400 * 2) return `${Math.floor(s / 3600)} 小時前`
  return `${Math.floor(s / 86400)} 天前`
}
function clock(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

// 帳號頭像用帳號清單的資料（有 profilePic）；沒載到時用狀態 API 的
const accountFor = (a: AccountStatus) => accounts.byId.get(a.id) ?? { ...a, connectionId: null, connected: true, webhookSubscribed: a.subscribed, lastError: '', connectedAt: null }
const accountName = (a: AccountStatus) => (a.username ? `@${a.username}` : a.name || a.igUserId)

async function toggle(a: AccountStatus, v: boolean | string | number) {
  const on = Boolean(v)
  toggling.value[a.id] = true
  try {
    await automationApi.setAutoReply(a.id, on)
    a.autoReplyEnabled = on
    ElMessage.success(on ? `${accountName(a)} 已開啟自動回覆` : `${accountName(a)} 已關閉自動回覆`)
  } finally {
    toggling.value[a.id] = false
  }
}

interface SysItem {
  key: string
  label: string
  icon: typeof Cpu
  light: Light
  rows: [string, string][]
}
const sysItems = computed<SysItem[]>(() => {
  const s = status.value
  if (!s) return []
  const c = s.claude
  const j = s.jobs
  const callRow: [string, string] = c.lastCall
    ? ['最近一次呼叫', `${clock(c.lastCall.at)} · ${c.lastCall.ok ? '成功' : '失敗'} · ${(c.lastCall.durationMs / 1000).toFixed(1)} 秒`]
    : ['最近一次呼叫', '—']
  return [
    {
      key: 'claude',
      label: 'Claude',
      icon: Cpu,
      light: c,
      rows: [
        ['API Key', !c.configured ? '未設定' : c.keyValid === true ? '有效' : c.keyValid === false ? '無效' : '無法確認'],
        ['Key 檢查時間', clock(c.keyCheckedAt)],
        callRow,
        ...(c.lastCall && !c.lastCall.ok && c.lastCall.error ? ([['錯誤', c.lastCall.error]] as [string, string][]) : []),
        ...(c.consecutiveFails ? ([['連續失敗', `${c.consecutiveFails} 次`]] as [string, string][]) : []),
      ],
    },
    {
      key: 'jobs',
      label: '背景工作',
      icon: Operation,
      light: j,
      rows: [
        ['待處理私訊事件', `${j.pending} 筆${j.oldestAt ? `（最舊 ${ago(j.oldestAt)}）` : ''}`],
        ['發佈失敗貼文', `${j.failedPosts} 篇`],
        ['遲發貼文', `${j.overduePosts} 篇`],
        ['下一篇排程', clock(j.nextPostAt)],
      ],
    },
    {
      key: 'db',
      label: '資料庫',
      icon: Coin,
      light: s.db,
      rows: [['回應時間', `${s.db.latencyMs} ms`]],
    },
  ]
})
</script>

<template>
  <section class="status lc-card" :class="{ loading: !status && !failed }" aria-label="系統狀態">
    <header class="st-head">
      <h3>
        系統狀態
        <span v-if="status" class="dot" :class="status.meta.status" :aria-label="statusLabel[status.meta.status]" />
      </h3>
      <span v-if="status" class="checked" :title="clock(status.checkedAt)">{{ ago(status.checkedAt) }}檢查</span>
      <span v-else-if="failed" class="checked bad">無法取得狀態</span>
    </header>

    <div v-if="status" class="st-body">
      <div v-if="status.accounts.length" class="acc-grid">
        <div v-for="a in status.accounts" :key="a.id" class="acc" :class="[a.status, { off: !a.active }]">
          <el-popover :trigger="trigger" placement="bottom-start" :width="290" :show-arrow="false" popper-class="status-pop">
            <template #reference>
              <button type="button" class="acc-top">
                <span class="av">
                  <AccountAvatar :account="accountFor(a)" :size="34" />
                  <span class="dot ring" :class="a.status" :aria-label="statusLabel[a.status]" />
                </span>
                <span class="acc-text">
                  <span class="acc-name">{{ accountName(a) }}</span>
                  <span class="acc-sub">
                    <span class="mini"><i class="dot sm" :class="a.connection.status" />連線</span>
                    <span class="mini"><i class="dot sm" :class="a.webhook.status" />私訊</span>
                    <span class="mini muted">{{ ago(a.lastWebhookAt) }}</span>
                  </span>
                </span>
              </button>
            </template>
            <div class="pop">
              <div class="pop-title">{{ accountName(a) }}</div>
              <div class="pop-row"><span>狀態</span><b :class="a.status">{{ a.message }}</b></div>
              <div class="pop-row">
                <span>Meta 連線</span>
                <b :class="a.connection.status">{{ a.connection.message }}</b>
              </div>
              <div class="pop-row"><span>私訊事件</span><b :class="a.webhook.status">{{ a.webhook.message }}</b></div>
              <div class="pop-row"><span>最後收到</span><b>{{ clock(a.lastWebhookAt) }}</b></div>
              <div v-if="a.posts.failed || a.posts.overdue" class="pop-row">
                <span>貼文</span><b class="yellow">失敗 {{ a.posts.failed }} · 遲發 {{ a.posts.overdue }}</b>
              </div>
              <div class="pop-row"><span>檢查時間</span><b>{{ clock(a.checkedAt) }}</b></div>
            </div>
          </el-popover>
          <div class="acc-switch">
            <span>自動回覆</span>
            <el-switch
              :model-value="a.autoReplyEnabled"
              size="small"
              :loading="toggling[a.id]"
              :disabled="!status.canToggleAutoReply || !a.active"
              :aria-label="`${accountName(a)} 自動回覆`"
              @change="(v: string | number | boolean) => toggle(a, v)"
            />
          </div>
        </div>
      </div>

      <div class="sys-grid">
        <el-popover
          v-for="it in sysItems"
          :key="it.key"
          :trigger="trigger"
          placement="bottom-start"
          :width="280"
          :show-arrow="false"
          popper-class="status-pop"
        >
          <template #reference>
            <button type="button" class="sys" :class="it.light.status">
              <el-icon class="sys-icon" :size="18"><component :is="it.icon" /></el-icon>
              <span class="sys-text">
                <span class="sys-name">{{ it.label }}<i class="dot sm" :class="it.light.status" /></span>
                <span class="sys-msg">{{ it.light.message }}</span>
              </span>
            </button>
          </template>
          <div class="pop">
            <div class="pop-title">{{ it.label }}</div>
            <div class="pop-row"><span>狀態</span><b :class="it.light.status">{{ it.light.message }}</b></div>
            <div v-for="[k, v] in it.rows" :key="k" class="pop-row">
              <span>{{ k }}</span><b>{{ v }}</b>
            </div>
          </div>
        </el-popover>
      </div>
    </div>
    <el-skeleton v-else-if="!failed" :rows="2" animated />
  </section>
</template>

<style scoped lang="scss">
.status {
  padding: 14px 16px 16px;
  margin-bottom: 16px;
}
.st-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 12px;
  h3 {
    margin: 0;
    font-size: 15px;
    display: flex;
    align-items: center;
    gap: 8px;
  }
}
.checked {
  font-size: 12px;
  color: var(--lc-muted);
  &.bad {
    color: var(--lc-fail-fg);
  }
}

// 燈號色：綠／黃／紅用既有狀態 token（不是圖表色）
.dot {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--lc-faint);
  flex: none;
  &.green {
    background: var(--lc-ok-fg);
  }
  &.yellow {
    background: var(--lc-tone-amber-fg);
  }
  &.red {
    background: var(--lc-fail-fg);
  }
  &.sm {
    width: 7px;
    height: 7px;
    margin: 0 4px 0 0;
  }
  &.ring {
    position: absolute;
    right: -2px;
    bottom: -2px;
    width: 12px;
    height: 12px;
    box-shadow: 0 0 0 2px var(--lc-surface);
  }
}

.acc-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
  gap: 10px;
  margin-bottom: 10px;
}
.acc {
  border: 1px solid var(--lc-border);
  border-radius: 12px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  &.red {
    border-color: var(--lc-fail-fg);
    background: var(--lc-fail-bg);
  }
  &.off {
    opacity: 0.7;
  }
}
.acc-top {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  min-width: 0;
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.av {
  position: relative;
  display: inline-flex;
  flex: none;
}
.acc-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
  gap: 2px;
}
.acc-name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.acc-sub {
  display: flex;
  gap: 10px;
  font-size: 12px;
  color: var(--lc-text);
}
.mini {
  display: inline-flex;
  align-items: center;
  white-space: nowrap;
  &.muted {
    color: var(--lc-muted);
  }
}
.acc-switch {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 13px;
  color: var(--lc-muted);
  padding-top: 6px;
  border-top: 1px dashed var(--lc-border);
}

.sys-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 10px;
}
.sys {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--lc-border);
  border-radius: 12px;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
  &.red {
    border-color: var(--lc-fail-fg);
    background: var(--lc-fail-bg);
  }
  &:hover {
    background: var(--lc-bg-hover);
  }
}
.sys-icon {
  color: var(--lc-muted);
  flex: none;
}
.sys-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.sys-name {
  font-weight: 600;
  font-size: 14px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  .dot.sm {
    margin: 0;
  }
}
.sys-msg {
  font-size: 12px;
  color: var(--lc-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pop {
  font-size: 13px;
  display: grid;
  gap: 6px;
}
.pop-title {
  font-weight: 700;
  margin-bottom: 2px;
}
.pop-row {
  display: grid;
  grid-template-columns: 76px 1fr;
  gap: 8px;
  span {
    color: var(--lc-muted);
  }
  b {
    font-weight: 500;
    color: var(--lc-text);
    word-break: break-word;
    &.red {
      color: var(--lc-fail-fg);
    }
    &.yellow {
      color: var(--lc-tone-amber-fg);
    }
    &.green {
      color: var(--lc-ok-fg);
    }
  }
}

@media (max-width: 768px) {
  .sys-grid {
    grid-template-columns: 1fr;
  }
  .acc-grid {
    grid-template-columns: 1fr;
  }
}
</style>
