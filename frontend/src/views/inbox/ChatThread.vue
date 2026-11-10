<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowLeft, Promotion, Check, RefreshLeft } from '@element-plus/icons-vue'
import { contactLabel, type Conversation, type Message } from '@/api/inbox'
import ContactAvatar from '@/components/ContactAvatar.vue'
import { clockTime, dayKey, dayLabel } from '@/utils/time'

const props = defineProps<{
  conversation: Conversation
  messages: Message[] | undefined
  hasMore: boolean
  sendFn: (text: string) => Promise<void>
}>()
const emit = defineEmits<{
  back: []
  'load-older': []
  'set-handler': [h: 'bot' | 'human']
  'set-status': [s: 'open' | 'closed']
  'open-contact': []
}>()

const scroller = ref<HTMLElement>()
const draft = ref('')
const sending = ref(false)

// --- 24h / 7d 回覆窗口 ---
const now = ref(Date.now())
const tick = setInterval(() => (now.value = Date.now()), 60_000)
onBeforeUnmount(() => clearInterval(tick))

const windowState = computed(() => {
  const last = props.conversation.lastInboundAt
  if (!last) return { kind: 'closed' as const, text: '顧客尚未傳訊，IG 不允許主動私訊' }
  const age = now.value - new Date(last).getTime()
  const H = 3600_000
  if (age < 24 * H) {
    const left = Math.max(1, Math.ceil((24 * H - age) / H))
    return { kind: left <= 3 ? ('warn' as const) : ('ok' as const), text: `回覆窗口剩 ${left} 小時` }
  }
  if (age < 7 * 24 * H) {
    const left = Math.ceil((7 * 24 * H - age) / (24 * H))
    return { kind: 'human' as const, text: `已過 24 小時：自動回覆停止，真人仍可回覆 ${left} 天` }
  }
  return { kind: 'closed' as const, text: '已超過 7 天，需等顧客再傳訊息才能回覆' }
})
const canSend = computed(() => windowState.value.kind !== 'closed')

// --- 日期分組 ---
const groups = computed(() => {
  const out: { key: string; label: string; items: Message[] }[] = []
  for (const m of props.messages ?? []) {
    const k = dayKey(m.createdAt)
    if (!out.length || out[out.length - 1].key !== k) out.push({ key: k, label: dayLabel(m.createdAt), items: [] })
    out[out.length - 1].items.push(m)
  }
  return out
})

const senderLabel: Record<string, string> = { ai: 'AI', bot: '自動回覆', human: '真人' }

// --- 捲動：新訊息時若在底部附近就自動捲到底 ---
function nearBottom() {
  const el = scroller.value
  return !el || el.scrollHeight - el.scrollTop - el.clientHeight < 120
}
async function toBottom() {
  await nextTick()
  scroller.value?.scrollTo({ top: scroller.value.scrollHeight })
}
watch(
  () => props.conversation.id,
  () => toBottom(),
)
watch(
  () => props.messages?.length ?? 0,
  async (_n, o) => {
    if (!o || nearBottom()) await toBottom()
  },
)
onMounted(toBottom)

async function onScroll() {
  const el = scroller.value
  if (!el || el.scrollTop > 40 || !props.hasMore) return
  const prev = el.scrollHeight
  emit('load-older')
  await nextTick()
  // 等父層載完再補位；簡化：下一幀維持視覺位置
  requestAnimationFrame(() => {
    if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight - prev
  })
}

// --- 送出（Enter 送出、Shift+Enter 換行；中文輸入法選字中不送） ---
function onKeydown(ev: Event | KeyboardEvent) {
  const e = ev as KeyboardEvent
  if (e.key !== 'Enter' || e.shiftKey || e.isComposing || e.keyCode === 229) return
  if (window.matchMedia('(max-width: 768px)').matches) return // 手機 Enter 換行，用按鈕送
  e.preventDefault()
  submit()
}
async function submit() {
  const text = draft.value.trim()
  if (!text || sending.value || !canSend.value) return
  sending.value = true
  try {
    await props.sendFn(text)
    draft.value = ''
    await toBottom()
  } catch {
    // 錯誤由 http 攔截器提示；保留草稿讓使用者重送
  } finally {
    sending.value = false
  }
}

const handler = computed({
  get: () => props.conversation.handler,
  set: (v) => emit('set-handler', v),
})
</script>

<template>
  <div class="thread">
    <header class="t-head">
      <el-button class="back" text circle :icon="ArrowLeft" @click="emit('back')" />
      <ContactAvatar :contact="conversation.contact" :size="38" class="clickable" @click="emit('open-contact')" />
      <div class="who clickable" title="顧客資料" @click="emit('open-contact')">
        <div class="name">{{ contactLabel(conversation.contact) }}</div>
        <div v-if="conversation.contact.username" class="handle">@{{ conversation.contact.username }}</div>
      </div>
      <el-segmented
        v-model="handler"
        size="small"
        :options="[
          { label: 'AI 接待', value: 'bot' },
          { label: '真人', value: 'human' },
        ]"
      />
      <el-tooltip :content="conversation.status === 'open' ? '標為已結束' : '重新開啟'" placement="bottom">
        <el-button
          circle
          :icon="conversation.status === 'open' ? Check : RefreshLeft"
          @click="emit('set-status', conversation.status === 'open' ? 'closed' : 'open')"
        />
      </el-tooltip>
    </header>

    <div ref="scroller" class="t-body" @scroll.passive="onScroll">
      <div v-if="hasMore" class="more">往上捲載入更早的訊息</div>
      <div v-if="!messages" v-loading="true" class="t-loading" />
      <template v-for="g in groups" :key="g.key">
        <div class="day"><span>{{ g.label }}</span></div>
        <div v-for="m in g.items" :key="m.id" class="msg" :class="m.direction">
          <div class="bubble" :class="[m.senderType, { media: !!m.mediaUrl }]">
            <el-image
              v-if="m.mediaUrl && (m.kind === 'image' || m.kind === 'story_reply')"
              :src="m.mediaUrl"
              :preview-src-list="[m.mediaUrl]"
              fit="cover"
              class="img"
              preview-teleported
            />
            <div v-if="m.text" class="text">{{ m.text }}</div>
          </div>
          <div class="meta">
            <span v-if="m.direction === 'out'" class="by" :class="m.senderType">{{ senderLabel[m.senderType] }}</span>
            {{ clockTime(m.createdAt) }}
          </div>
        </div>
      </template>
    </div>

    <footer class="t-foot">
      <div class="window" :class="windowState.kind">{{ windowState.text }}</div>
      <div class="composer">
        <el-input
          v-model="draft"
          type="textarea"
          :autosize="{ minRows: 1, maxRows: 5 }"
          resize="none"
          :disabled="!canSend"
          :placeholder="canSend ? '輸入訊息…' : '目前無法回覆'"
          @keydown="onKeydown"
        />
        <el-button
          type="primary"
          circle
          size="large"
          :icon="Promotion"
          :loading="sending"
          :disabled="!draft.trim() || !canSend"
          @click="submit"
        />
      </div>
      <div v-if="conversation.handler === 'bot'" class="hint">送出訊息後會自動切成「真人」，AI 暫停回覆這位顧客</div>
    </footer>
  </div>
</template>

<style scoped lang="scss">
.thread {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: #fbf8f4;
}
.t-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  background: var(--lc-surface);
  border-bottom: 1px solid var(--lc-border);
  .back {
    display: none;
  }
}
.clickable {
  cursor: pointer;
}
.who {
  flex: 1;
  min-width: 0;
  .name {
    font-weight: 700;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .handle {
    font-size: 12px;
    color: var(--lc-muted);
  }
}
.t-body {
  flex: 1;
  overflow-y: auto;
  padding: 12px 16px 20px;
}
.t-loading {
  height: 120px;
}
.more {
  text-align: center;
  font-size: 12px;
  color: var(--lc-muted);
  padding: 4px 0 8px;
}
.day {
  text-align: center;
  margin: 14px 0 10px;
  span {
    font-size: 12px;
    color: var(--lc-muted);
    background: #f1e9df;
    padding: 3px 10px;
    border-radius: 999px;
  }
}
.msg {
  display: flex;
  flex-direction: column;
  margin: 4px 0;
  &.in {
    align-items: flex-start;
  }
  &.out {
    align-items: flex-end;
  }
}
.bubble {
  max-width: min(72%, 520px);
  padding: 9px 13px;
  border-radius: 18px;
  line-height: 1.5;
  font-size: 15px;
  word-break: break-word;
  white-space: pre-wrap;
  &.contact {
    background: #fff;
    border: 1px solid var(--lc-border);
    border-bottom-left-radius: 6px;
  }
  &.human {
    background: var(--lc-espresso);
    color: #fff;
    border-bottom-right-radius: 6px;
  }
  &.ai,
  &.bot {
    background: var(--el-color-primary-light-8);
    color: var(--lc-text);
    border-bottom-right-radius: 6px;
  }
  &.media {
    padding: 4px;
    .text {
      padding: 6px 9px 4px;
    }
  }
}
.img {
  display: block;
  width: 220px;
  max-width: 100%;
  height: 220px;
  border-radius: 14px;
}
.meta {
  margin-top: 3px;
  font-size: 11px;
  color: var(--lc-muted);
  padding: 0 4px;
}
.by {
  margin-right: 4px;
  font-weight: 500;
  &.ai,
  &.bot {
    color: #4f7a4a;
  }
  &.human {
    color: var(--lc-roast);
  }
}
.t-foot {
  padding: 8px 12px 12px;
  background: var(--lc-surface);
  border-top: 1px solid var(--lc-border);
}
.window {
  font-size: 12px;
  margin: 0 4px 8px;
  color: #4f7a4a;
  &.warn {
    color: #b7791f;
  }
  &.human {
    color: #a86a2c;
  }
  &.closed {
    color: #a2504a;
  }
}
.composer {
  display: flex;
  align-items: flex-end;
  gap: 8px;
  :deep(.el-textarea__inner) {
    border-radius: 20px;
    padding: 9px 14px;
    font-size: 15px;
    box-shadow: 0 0 0 1px var(--lc-border) inset;
  }
}
.hint {
  margin: 6px 4px 0;
  font-size: 11px;
  color: var(--lc-muted);
}

@media (max-width: 768px) {
  .t-head .back {
    display: inline-flex;
    margin-left: -6px;
  }
  .t-head :deep(.el-segmented) {
    font-size: 12px;
  }
  .bubble {
    max-width: 82%;
  }
  .t-foot {
    padding-bottom: calc(12px + env(safe-area-inset-bottom));
  }
}
</style>
