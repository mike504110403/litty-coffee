<script setup lang="ts">
import { useIsMobile } from '@/composables/useIsMobile'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox, type InputInstance } from 'element-plus'
import { ArrowLeft, Promotion, Check, RefreshLeft, WarningFilled, Clock } from '@element-plus/icons-vue'
import { contactLabel, inboxApi, riskSourceLabels, throttleMinutes, type Conversation, type Message, type Suggestion, type SuggestOption } from '@/api/inbox'
import { flagsApi, type RiskResult } from '@/api/flags'
import ContactAvatar from '@/components/ContactAvatar.vue'
import AccountTag from '@/components/AccountTag.vue'
import RiskTag from '@/components/RiskTag.vue'
import RiskMenu from '@/components/RiskMenu.vue'
import HelpTip from '@/components/HelpTip.vue'
import { useAccountsStore } from '@/stores/accounts'
import type { PendingMessage } from '@/stores/inbox'
import { clockTime, dayKey, dayLabel } from '@/utils/time'

const isMobile = useIsMobile()

const accountsStore = useAccountsStore()

const props = defineProps<{
  conversation: Conversation
  messages: Message[] | undefined
  hasMore: boolean
  pending?: PendingMessage[] // 樂觀送出中的暫時訊息（傳送中／等待中／失敗）
  sendFn: (text: string) => void
}>()
const emit = defineEmits<{
  back: []
  'load-older': []
  'set-handler': [h: 'bot' | 'human']
  'set-status': [s: 'open' | 'closed']
  'open-contact': []
  'risk-changed': [r: RiskResult]
  retry: [tmpId: number]
  discard: [tmpId: number]
}>()

const scroller = ref<HTMLElement>()
const inputRef = ref<InputInstance>()
const composerEl = ref<HTMLElement>()
const draft = ref('')

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
const isImageKind = (k: string) => k === 'image' || k === 'story_reply' || k === 'story_mention'

// 「已讀」只標在顧客讀過的最後一則我方訊息
const lastSeenOutId = computed(() => {
  const readAt = props.conversation.contactReadAt
  if (!readAt) return 0
  const t = new Date(readAt).getTime()
  let id = 0
  for (const m of props.messages ?? []) {
    if (m.direction === 'out' && new Date(m.createdAt).getTime() <= t) id = m.id
  }
  return id
})

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

// --- 捲動：訊息區是唯一的捲動區 ---
// stick＝使用者人在底部附近：內容長高（圖片載入、鍵盤彈出、建議卡出現）時維持貼底
const inner = ref<HTMLElement>()
const stick = ref(true)
const unseen = ref(0) // 人不在底部時收到的新訊息數 → 顯示「↓ 新訊息」浮鈕
const NEAR = 120
function nearBottom() {
  const el = scroller.value
  return !el || el.scrollHeight - el.scrollTop - el.clientHeight < NEAR
}
function toBottomNow() {
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
  stick.value = true
  unseen.value = 0
}
async function toBottom() {
  await nextTick()
  toBottomNow()
}
function jumpNew() {
  const el = scroller.value
  el?.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  unseen.value = 0
  stick.value = true
}
let ro: ResizeObserver | undefined
onMounted(() => {
  toBottomNow()
  if (typeof ResizeObserver === 'undefined') return
  ro = new ResizeObserver(() => {
    if (stick.value) toBottomNow()
  })
  if (inner.value) ro.observe(inner.value)
  if (scroller.value) ro.observe(scroller.value)
})
onBeforeUnmount(() => ro?.disconnect())

// 換對話：直接停在最底（不經過上一段對話的位置）
watch(
  () => props.conversation.id,
  () => {
    loadingOlder = false
    toBottom()
  },
  { flush: 'post' },
)
// 訊息變動：尾端新增＝新訊息（貼底才跟著捲，否則出浮鈕）；開頭新增＝載入更早（維持畫面位置）
watch(
  () => [props.messages?.[0]?.id, props.messages?.[props.messages.length - 1]?.id, props.messages?.length ?? 0] as const,
  ([first, last, len], [pFirst, pLast, pLen]) => {
    if (!pLen) {
      toBottom() // 第一次載入
      return
    }
    if (last !== pLast && len > pLen && first === pFirst) {
      // 尾端新增（自己送出的已在 submit 捲到底）
      if (stick.value) toBottom()
      else unseen.value += 1
    }
  },
  { flush: 'post' },
)

// 暫時訊息出現／狀態改變（失敗的那行比較高）：人在底部就維持貼底，由 ResizeObserver 負責
// 載入更早：先在 DOM 更新前記下高度，更新後補回差值，畫面不跳
let loadingOlder = false
let olderTimer: ReturnType<typeof setTimeout> | undefined
let anchorHeight = 0
let anchorTop = 0
watch(
  () => props.messages?.[0]?.id,
  (first, pFirst) => {
    if (pFirst === undefined || first === pFirst || !loadingOlder) return
    // flush:'pre'：此時 DOM 還是舊的
    anchorHeight = scroller.value?.scrollHeight ?? 0
    anchorTop = scroller.value?.scrollTop ?? 0
    nextTick(() => {
      const el = scroller.value
      if (el) el.scrollTop = anchorTop + (el.scrollHeight - anchorHeight)
      loadingOlder = false
      clearTimeout(olderTimer)
    })
  },
  { flush: 'pre' },
)
onBeforeUnmount(() => clearTimeout(olderTimer))

function onScroll() {
  const el = scroller.value
  if (!el) return
  stick.value = nearBottom()
  if (stick.value) unseen.value = 0
  if (el.scrollTop > 40 || !props.hasMore || loadingOlder || !props.messages) return
  loadingOlder = true
  clearTimeout(olderTimer)
  olderTimer = setTimeout(() => (loadingOlder = false), 5000) // 請求失敗時放行重試
  emit('load-older')
}

// --- 送出（Enter 送出、Shift+Enter 換行；中文輸入法選字中不送） ---
function onKeydown(ev: Event | KeyboardEvent) {
  const e = ev as KeyboardEvent
  if (e.key !== 'Enter' || e.shiftKey || e.isComposing || e.keyCode === 229) return
  if (window.matchMedia('(max-width: 768px)').matches) return // 手機 Enter 換行，用按鈕送
  e.preventDefault()
  submit()
}
// 樂觀送出：立刻清空輸入框、訊息進對話底部（狀態「傳送中」），背景由 store 依序送；輸入框不鎖，可接著打下一則
function submit() {
  const text = draft.value.trim()
  if (!text || !canSend.value) return
  props.sendFn(text)
  draft.value = ''
  dismissSuggestion() // 已回覆，候選作廢（含「AI 草稿」標籤與選中的候選卡）
  picked.value = null
  stick.value = true
  void toBottom()
}

// --- AI 建議回覆：一次給數個版本，點一個放進輸入框，送出一律由人按 ---
const AUTO_KEY = 'lc-ai-suggest-auto' // 每個瀏覽器各自記；預設開
function loadAuto() {
  try {
    return localStorage.getItem(AUTO_KEY) !== '0'
  } catch {
    return true
  }
}
const aiConfigured = ref(false)
const autoSuggest = ref(loadAuto())
watch(autoSuggest, (v) => {
  try {
    localStorage.setItem(AUTO_KEY, v ? '1' : '0')
  } catch {
    // 無痕模式等寫不進去：只在這次有效
  }
  if (!v) suggestion.value = null // 關掉就收起候選
})
const suggesting = ref(false)
const suggestion = ref<Suggestion | null>(null) // 目前的候選（還沒挑或挑完仍保留，可換一個）
const picked = ref<SuggestOption | null>(null) // 最後放進輸入框的那個版本
// 輸入框還是原封不動的 AI 版本時才顯示標籤；一改字或送出就消失
const showDraftTag = computed(() => !!picked.value && draft.value === picked.value.text)
let suggestSeq = 0 // 換對話或重新要求時，舊的回應作廢
const autoTried = new Set<number>()

onMounted(async () => {
  try {
    aiConfigured.value = (await inboxApi.aiStatus()).configured
  } catch {
    // 查不到就當沒設定：不顯示按鈕
  }
})

async function runSuggest(auto: boolean) {
  const convId = props.conversation.id
  const seq = ++suggestSeq
  suggesting.value = true
  try {
    const r = await inboxApi.suggest(convId, auto)
    if (!r || seq !== suggestSeq || convId !== props.conversation.id) return
    if (!r.options.length) return
    suggestion.value = r
  } catch {
    // 錯誤由 http 攔截器提示
  } finally {
    if (seq === suggestSeq) suggesting.value = false
  }
}

function onSuggestClick() {
  if (suggesting.value || !canSend.value) return
  void runSuggest(false)
}

// 點一個版本：放進輸入框。輸入框已有自己打的字（不是原封不動的 AI 版本）→ 先問要不要取代
async function pickOption(o: SuggestOption) {
  if (draft.value.trim() && !showDraftTag.value && draft.value !== o.text) {
    try {
      await ElMessageBox.confirm('輸入框已經有內容，要用這個版本取代嗎？', '取代輸入框內容', {
        confirmButtonText: '取代',
        cancelButtonText: '保留我的',
        type: 'warning',
      })
    } catch {
      return
    }
  }
  draft.value = o.text
  picked.value = o
}

function dismissSuggestion() {
  suggestSeq++
  suggesting.value = false
  suggestion.value = null
}

// 最近一則真人回覆之後、仍有未刪除的顧客訊息 → 回傳最後一則顧客訊息的 id（否則 null）。
// 轉真人時 AI／規則會先回一句「請店員回覆」，所以不能只看最後一則是不是顧客的
const pendingInboundId = computed(() => {
  const list = props.messages
  if (!list) return null
  let inboundIdx = -1
  for (let i = list.length - 1; i >= 0; i--) {
    if (list[i].direction === 'in' && !list[i].deleted) {
      inboundIdx = i
      break
    }
  }
  if (inboundIdx < 0) return null
  for (let i = list.length - 1; i > inboundIdx; i--) {
    const m = list[i]
    if (m.direction === 'out' && !m.deleted && m.senderType !== 'ai' && m.senderType !== 'bot') return null
  }
  return list[inboundIdx].id
})

// 開啟自動建議時：「真人」處理、真人回覆之後還有顧客訊息的對話 → 自動要一組候選（只顯示，不放進輸入框、不送出）
function maybeAutoSuggest() {
  const c = props.conversation
  if (!autoSuggest.value || !aiConfigured.value || suggesting.value || suggestion.value) return
  if (c.handler !== 'human' || !canSend.value) return
  const key = pendingInboundId.value
  if (key == null || autoTried.has(key)) return
  autoTried.add(key)
  void runSuggest(true)
}
watch(
  () => props.conversation.id,
  () => {
    suggestSeq++ // 作廢上一段對話還在路上的建議
    suggesting.value = false
    if (showDraftTag.value) draft.value = '' // 沒動過的 AI 版本不帶到別的對話
    picked.value = null
    suggestion.value = null
  },
)
// 顧客又傳新訊息（最後一則顧客訊息 id 改變）→ 舊候選過時，收起來讓自動建議重新要
watch(pendingInboundId, (id, prev) => {
  if (prev != null && id !== prev) suggestion.value = null
})
watch(
  [() => props.conversation.id, pendingInboundId, () => props.conversation.handler, aiConfigured, autoSuggest, suggestion],
  maybeAutoSuggest,
  { immediate: true },
)

// --- 標記「回答有誤」（只限 AI／規則的自動回覆） ---
const isAutoReply = (m: Message) => m.direction === 'out' && (m.senderType === 'ai' || m.senderType === 'bot') && !m.deleted
const flagged = ref(new Set<number>()) // 這次開著時標過的（後端未處理前不能重複標）
const wrongMsg = ref<Message | null>(null)
const wrongNote = ref('')
const wrongSaving = ref(false)
const wrongOpen = computed({
  get: () => wrongMsg.value !== null,
  set: (v) => {
    if (!v) wrongMsg.value = null
  },
})
function openWrong(m: Message) {
  wrongMsg.value = m
  wrongNote.value = ''
}
async function submitWrong() {
  const m = wrongMsg.value
  if (!m || !wrongNote.value.trim()) return
  wrongSaving.value = true
  try {
    await flagsApi.markWrong(m.id, wrongNote.value.trim())
    flagged.value.add(m.id)
    ElMessage.success('已記到「自動回覆 → 待改進」')
    wrongMsg.value = null
  } catch (e) {
    // 409＝已經標過、尚未處理
    if ((e as { response?: { status?: number } }).response?.status === 409) {
      flagged.value.add(m.id)
      wrongMsg.value = null
    }
  } finally {
    wrongSaving.value = false
  }
}

const riskSourceText = computed(() => riskSourceLabels[props.conversation.contact.riskSource] || '手動')

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
        <div class="name-row">
          <span class="name">{{ contactLabel(conversation.contact) }}</span>
          <RiskTag :risk="conversation.contact.risk" />
        </div>
        <div v-if="conversation.contact.username || accountsStore.multi" class="handle">
          <span v-if="conversation.contact.username">@{{ conversation.contact.username }}</span>
          <span v-if="accountsStore.multi" class="via">
            傳給 <AccountTag :id="conversation.igAccountId" />
          </span>
        </div>
      </div>
      <RiskMenu
        :contact-id="conversation.contact.id"
        :risk="conversation.contact.risk"
        size="small"
        @changed="(r) => emit('risk-changed', r)"
      />
      <el-segmented
        v-model="handler"
        class="handler-seg"
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

    <div v-if="conversation.contact.risk === 'blocked'" class="risk-banner blocked" role="status">
      <el-icon><WarningFilled /></el-icon>
      <span>
        此顧客在黑名單：AI 不會回覆、不會通知（<template v-if="conversation.contact.riskReason"
          >原因：{{ conversation.contact.riskReason }}，</template
        >來源：{{ riskSourceText }}）
      </span>
    </div>
    <div
      v-else-if="conversation.contact.risk === 'watch' || conversation.contact.throttleLevel > 0"
      class="risk-banner watch"
      role="status"
    >
      <el-icon><WarningFilled /></el-icon>
      <span>
        <template v-if="conversation.contact.risk === 'watch'">問題顧客</template>
        <template v-if="conversation.contact.throttleLevel > 0">
          {{ conversation.contact.risk === 'watch' ? '・' : '' }}已降低自動回覆頻率（每
          {{ throttleMinutes(conversation.contact.throttleLevel) }} 分鐘最多回一次）</template
        ><template v-else>：AI 照常回覆，回覆時請留意</template
        ><template v-if="conversation.contact.riskReason">（原因：{{ conversation.contact.riskReason }}）</template>
      </span>
    </div>

    <div class="t-main">
      <div ref="scroller" class="t-body" @scroll.passive="onScroll">
        <div ref="inner" class="t-inner">
          <div v-if="hasMore" class="more">往上捲載入更早的訊息</div>
          <div v-if="!messages" v-loading="true" class="t-loading" />
          <template v-for="g in groups" :key="g.key">
            <div class="day"><span>{{ g.label }}</span></div>
            <div v-for="m in g.items" :key="m.id" class="msg" :class="m.direction">
              <div v-if="m.deleted" class="bubble deleted">對方已收回這則訊息</div>
              <div v-else class="bubble" :class="[m.senderType, { media: !!m.mediaUrl, reacted: !!m.reaction }]">
                <template v-if="m.mediaUrl">
                  <video v-if="m.kind === 'video'" :src="m.mediaUrl" controls preload="metadata" class="img video" />
                  <audio v-else-if="m.kind === 'audio'" :src="m.mediaUrl" controls preload="metadata" class="audio" />
                  <el-image
                    v-else-if="isImageKind(m.kind)"
                    :src="m.mediaUrl"
                    :preview-src-list="[m.mediaUrl]"
                    fit="cover"
                    class="img"
                    preview-teleported
                  >
                    <template #error><div class="img-gone">圖片已無法顯示</div></template>
                  </el-image>
                </template>
                <div v-if="m.text" class="text">{{ m.text }}</div>
                <span v-if="m.reaction" class="reaction" :aria-label="`顧客回應 ${m.reaction}`">{{ m.reaction }}</span>
              </div>
              <div class="meta">
                <span v-if="m.direction === 'out'" class="by" :class="m.senderType">{{ m.sentBy?.displayName || senderLabel[m.senderType] }}</span>
                {{ clockTime(m.createdAt) }}
                <span v-if="m.id === lastSeenOutId" class="seen">已讀</span>
                <template v-if="isAutoReply(m)">
                  <span v-if="flagged.has(m.id)" class="flag-done">已標記有誤</span>
                  <button v-else type="button" class="flag-wrong" @click="openWrong(m)">回答有誤</button>
                </template>
              </div>
            </div>
          </template>
          <div v-for="p in pending" :key="p.tmpId" class="msg out pending" :class="p.status">
            <div class="bubble human"><div class="text">{{ p.text }}</div></div>
            <div class="meta">
              <template v-if="p.status === 'failed'">
                <span class="send-fail">傳送失敗</span>
                <button type="button" class="send-act" @click="emit('retry', p.tmpId)">重送</button>
                <button type="button" class="send-act" @click="emit('discard', p.tmpId)">刪除</button>
              </template>
              <template v-else>
                <el-icon class="clock"><Clock /></el-icon>{{ p.status === 'queued' ? '等待中' : '傳送中' }}
              </template>
            </div>
          </div>
        </div>
      </div>
      <button v-if="unseen > 0" type="button" class="new-pill" @click="jumpNew">
        ↓ {{ unseen > 1 ? `${unseen} 則新訊息` : '新訊息' }}
      </button>
    </div>

    <footer class="t-foot">
      <div class="window" :class="windowState.kind">
        {{ windowState.text }}
        <HelpTip v-if="conversation.handler === 'bot'" text="送出訊息後會自動切成「真人」，AI 暫停回覆這位顧客。" />
      </div>
      <div v-if="aiConfigured" class="ai-bar">
        <el-button size="small" round :loading="suggesting" :disabled="!canSend" @click="onSuggestClick">
          <template #icon>
            <svg viewBox="0 0 24 24" width="1em" height="1em" fill="currentColor" aria-hidden="true">
              <path d="M10 2l2 6 6 2-6 2-2 6-2-6-6-2 6-2 2-6z" />
              <path d="M19 14l.9 2.6 2.6.9-2.6.9L19 21l-.9-2.6-2.6-.9 2.6-.9L19 14z" />
            </svg>
          </template>
          {{ suggestion ? '換一組' : 'AI 建議' }}
        </el-button>
        <label class="auto-row"><el-switch v-model="autoSuggest" size="small" />自動建議</label>
        <HelpTip text="開啟時，顧客傳訊後自動列出幾個回覆版本；點一個放進輸入框，不會自動送出。" />
        <template v-if="showDraftTag && picked">
          <span class="ai-tag">AI 草稿，送出前請確認</span>
          <HelpTip v-if="picked.basis.length" :width="240">
            參考的知識庫：
            <ul class="basis">
              <li v-for="b in picked.basis" :key="b">{{ b }}</li>
            </ul>
          </HelpTip>
        </template>
      </div>
      <div v-if="aiConfigured && (suggestion || (suggesting && autoSuggest))" class="ai-picks">
        <template v-if="suggestion">
          <button
            v-for="(o, i) in suggestion.options"
            :key="i"
            type="button"
            class="pick"
            :class="{ active: showDraftTag && picked?.text === o.text }"
            :disabled="!canSend"
            @click="pickOption(o)"
          >
            <span class="pick-label">{{ o.label }}</span>
            <span class="pick-text">{{ o.text }}</span>
          </button>
          <button type="button" class="pick-close" aria-label="收起建議" @click="dismissSuggestion">✕</button>
        </template>
        <div v-else class="pick-loading">AI 正在擬回覆…</div>
      </div>
      <div v-if="suggestion?.warning" class="ai-warn" role="status">
        <el-icon><WarningFilled /></el-icon>
        <span>{{ suggestion.warning }}</span>
      </div>
      <div ref="composerEl" class="composer">
        <el-input
          ref="inputRef"
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
          :disabled="!draft.trim() || !canSend"
          @mousedown.prevent
          @click="submit"
        />
      </div>
    </footer>

    <el-dialog class="lc-fs-dialog" :fullscreen="isMobile" v-model="wrongOpen" width="min(460px, 92vw)" append-to-body align-center>
      <template #header>
        標記回答有誤
        <HelpTip text="會記到「自動回覆 → 待改進」，之後可補進知識庫或調整規則。" />
      </template>
      <div v-if="wrongMsg" class="wrong-quote">{{ wrongMsg.text }}</div>
      <el-input
        v-model="wrongNote"
        type="textarea"
        :autosize="{ minRows: 3, maxRows: 6 }"
        maxlength="500"
        show-word-limit
        placeholder="哪裡有誤、正確答案是什麼？例如：週一公休，不是營業到八點"
      />
      <template #footer>
        <el-button @click="wrongOpen = false">取消</el-button>
        <el-button type="primary" :loading="wrongSaving" :disabled="!wrongNote.trim()" @click="submitWrong">送出</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.thread {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--lc-bg-soft);
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
  .name-row {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
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
.risk-banner {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 8px 16px;
  font-size: 13px;
  line-height: 1.5;
  border-bottom: 1px solid var(--lc-border);
  .el-icon {
    flex-shrink: 0;
    margin-top: 2px;
  }
  &.blocked {
    background: var(--lc-fail-bg);
    color: var(--lc-fail-fg);
  }
  &.watch {
    background: var(--lc-warn-bg);
    color: var(--lc-warn-fg);
  }
}
.flag-wrong {
  margin-left: 6px;
  padding: 0;
  border: 0;
  background: none;
  font: inherit;
  color: var(--lc-muted);
  text-decoration: underline dotted;
  cursor: pointer;
  &:hover,
  &:focus-visible {
    color: var(--lc-tone-rose-fg);
  }
}
.flag-done {
  margin-left: 6px;
  color: var(--lc-tone-rose-fg);
}
.wrong-quote {
  margin: 0 0 12px;
  padding: 8px 12px;
  border-left: 3px solid var(--el-color-primary);
  background: var(--lc-bg-soft);
  border-radius: 6px;
  font-size: 13px;
  white-space: pre-wrap;
  max-height: 120px;
  overflow-y: auto;
}
.t-main {
  position: relative;
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.t-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain; // 捲到頭不把頁面一起帶著走
  overflow-anchor: none; // 載入更早訊息的位置補償由程式負責（Safari 沒有 scroll anchoring，兩邊一致）
  -webkit-overflow-scrolling: touch;
  padding: 12px 16px 20px;
}
.new-pill {
  display: none;
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
    background: var(--lc-bg-chip);
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
    background: var(--lc-surface);
    border: 1px solid var(--lc-border);
    border-bottom-left-radius: 6px;
  }
  &.human {
    background: var(--lc-espresso);
    color: var(--lc-on-fill);
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
.bubble {
  position: relative;
  &.reacted {
    margin-bottom: 12px;
  }
  &.deleted {
    background: transparent;
    border: 1px dashed var(--lc-muted);
    color: var(--lc-muted);
    font-size: 13px;
    font-style: italic;
  }
}
.reaction {
  position: absolute;
  bottom: -14px;
  right: 8px;
  font-size: 14px;
  line-height: 1;
  padding: 3px 5px;
  border-radius: 12px;
  background: var(--lc-surface);
  border: 1px solid var(--lc-border);
  box-shadow: var(--lc-reaction-shadow);
}
.msg.in .reaction {
  right: auto;
  left: 8px;
}
.img-gone {
  display: grid;
  place-items: center;
  width: 100%;
  height: 100%;
  background: var(--lc-cream);
  color: var(--lc-muted);
  font-size: 12px;
}
.video {
  height: auto;
  max-height: 320px;
  background: var(--lc-media-bg);
}
.audio {
  display: block;
  width: 240px;
  max-width: 100%;
}
.pending {
  &.sending,
  &.queued {
    opacity: 0.6;
  }
  .clock {
    margin-right: 3px;
    vertical-align: -2px;
  }
}
.send-fail {
  margin-right: 6px;
  color: var(--lc-fail-fg);
  font-weight: 500;
}
.send-act {
  margin-right: 8px;
  padding: 0;
  border: 0;
  background: none;
  font: inherit;
  color: var(--el-color-primary);
  text-decoration: underline;
  cursor: pointer;
}
.seen {
  margin-left: 4px;
  color: var(--lc-roast);
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
    color: var(--lc-tone-sage-fg);
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
  color: var(--lc-tone-sage-fg);
  &.warn {
    color: var(--lc-tone-amber-fg);
  }
  &.human {
    color: var(--lc-tone-caramel-fg);
  }
  &.closed {
    color: var(--lc-tone-rose-fg);
  }
}
.ai-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin: 0 4px 8px;
  :deep(.help-tip) {
    margin-left: 0;
  }
}
.ai-tag {
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
  background: var(--lc-tone-sage-bg);
  color: var(--lc-tone-sage-fg);
}
.ai-warn {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 4px 8px;
  padding: 6px 10px;
  border-radius: 8px;
  font-size: 12px;
  line-height: 1.5;
  background: var(--lc-warn-bg);
  color: var(--lc-warn-fg);
  .el-icon {
    flex-shrink: 0;
    margin-top: 2px;
  }
}
.auto-row {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.auto-row {
  font-size: 12px;
  color: var(--lc-muted);
}
.ai-picks {
  position: relative;
  display: flex;
  gap: 8px;
  margin: 0 4px 8px;
  padding-right: 26px;
  overflow-x: auto;
}
.pick {
  flex: 1 1 0;
  min-width: 180px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px 10px;
  border: 1px solid var(--lc-border);
  border-radius: 10px;
  background: var(--lc-surface);
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: border-color 0.15s, background-color 0.15s;
  &:hover:not(:disabled),
  &.active {
    border-color: var(--el-color-primary);
  }
  &.active {
    background: var(--lc-tone-sage-bg);
  }
  &:disabled {
    cursor: not-allowed;
    opacity: 0.6;
  }
}
.pick-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--el-color-primary);
}
.pick-text {
  font-size: 13px;
  line-height: 1.5;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.pick-close {
  position: absolute;
  top: 0;
  right: 0;
  width: 22px;
  height: 22px;
  border: 0;
  border-radius: 50%;
  background: transparent;
  color: var(--lc-muted);
  cursor: pointer;
  &:hover {
    color: inherit;
    background: var(--lc-border);
  }
}
.pick-loading {
  font-size: 12px;
  color: var(--lc-muted);
  padding: 6px 2px;
}
.basis {
  margin: 4px 0 0;
  padding-left: 18px;
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
@media (max-width: 768px) {
  .t-head {
    gap: 8px;
    padding: 6px 10px;
    min-height: 56px;
    .back {
      display: inline-flex;
      margin-left: -4px;
    }
    :deep(.el-button.is-circle),
    :deep(.el-button--small.is-circle) {
      width: 40px;
      height: 40px;
    }
    flex-wrap: wrap;
    // 第二列：接待方式切換全寬，兩個選項各半，好點
    .handler-seg {
      order: 9;
      flex: 1 0 100%;
      font-size: 14px;
      padding: 3px;
      :deep(.el-segmented__group) {
        display: flex;
        width: 100%;
      }
      :deep(.el-segmented__item) {
        flex: 1;
        justify-content: center;
        min-height: 40px;
      }
    }
  }
  .t-body {
    padding: 12px 12px 16px;
  }
  .bubble {
    max-width: 82%;
    font-size: 16px;
    overflow-wrap: anywhere;
  }
  .meta,
  .more,
  .who .handle {
    font-size: 13px;
  }
  .send-act {
    padding: 13px 8px;
    margin: -13px 4px -13px 0;
  }
  .flag-wrong,
  .flag-done {
    display: inline-block;
    padding: 13px 8px;
    margin: -13px 0 -13px 0; // 點擊範圍 ≥40px 但不撐高訊息列
  }
  .video {
    width: min(260px, 100%);
  }
  .new-pill {
    display: block;
    position: absolute;
    left: 50%;
    bottom: 12px;
    transform: translateX(-50%);
    height: 40px;
    padding: 0 18px;
    border: 0;
    border-radius: 999px;
    background: var(--lc-espresso);
    color: var(--lc-on-fill);
    font: inherit;
    font-size: 14px;
    box-shadow: var(--lc-tip-shadow);
    cursor: pointer;
  }
  .t-foot {
    padding: 6px 10px calc(8px + env(safe-area-inset-bottom));
    &:focus-within {
      padding-bottom: 8px; // 鍵盤彈出時底部已不是 home indicator
    }
  }
  .window {
    font-size: 13px;
    margin-bottom: 4px;
  }
  .ai-bar {
    flex-wrap: nowrap;
    margin-bottom: 4px;
    :deep(.el-button) {
      height: 40px;
      font-size: 14px;
      flex-shrink: 0;
    }
    :deep(.help-tip) {
      flex-shrink: 0;
    }
    .ai-tag {
      font-size: 13px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      min-width: 0;
    }
  }
  .auto-row {
    min-height: 40px;
    font-size: 13px;
    white-space: nowrap;
  }
  // 建議候選：左右滑的一列，一次露出約一張半
  .ai-picks {
    gap: 8px;
    margin: 0 0 6px;
    padding-right: 0;
    overflow-x: auto;
    overflow-y: hidden;
    overscroll-behavior-x: contain;
    scroll-snap-type: x mandatory;
    scrollbar-width: none;
    &::-webkit-scrollbar {
      display: none;
    }
  }
  .pick {
    flex: 0 0 78%;
    min-width: 0;
    scroll-snap-align: start;
    min-height: 64px;
  }
  .pick-label {
    font-size: 13px;
  }
  .pick-text {
    font-size: 14px;
    -webkit-line-clamp: 2;
  }
  .pick-close {
    position: static;
    flex: 0 0 40px;
    width: 40px;
    height: auto;
    min-height: 64px;
    scroll-snap-align: end;
  }
  .pick-loading,
  .ai-warn {
    font-size: 13px;
  }
  .composer {
    gap: 8px;
    :deep(.el-textarea__inner) {
      font-size: 16px; // iOS 聚焦時 <16px 會自動放大頁面
      min-height: 40px !important;
      padding: 9px 14px;
    }
  }
}
.via {
  margin-left: 6px;
  white-space: nowrap;
}
</style>
