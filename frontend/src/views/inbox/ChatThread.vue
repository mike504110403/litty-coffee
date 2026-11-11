<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowLeft, Promotion, Check, RefreshLeft, WarningFilled } from '@element-plus/icons-vue'
import { contactLabel, inboxApi, riskSourceLabels, throttleMinutes, type Conversation, type Message, type Suggestion } from '@/api/inbox'
import { flagsApi, type RiskResult } from '@/api/flags'
import ContactAvatar from '@/components/ContactAvatar.vue'
import AccountTag from '@/components/AccountTag.vue'
import RiskTag from '@/components/RiskTag.vue'
import RiskMenu from '@/components/RiskMenu.vue'
import HelpTip from '@/components/HelpTip.vue'
import { useAccountsStore } from '@/stores/accounts'
import { clockTime, dayKey, dayLabel } from '@/utils/time'

const accountsStore = useAccountsStore()

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
  'risk-changed': [r: RiskResult]
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
    aiDraft.value = null
    await toBottom()
  } catch {
    // 錯誤由 http 攔截器提示；保留草稿讓使用者重送
  } finally {
    sending.value = false
  }
}

// --- AI 建議回覆：只把草稿放進輸入框，送出一律由人按 ---
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
})
const suggesting = ref(false)
const aiDraft = ref<Suggestion | null>(null)
// 輸入框還是原封不動的 AI 草稿時才顯示標籤與警示；一改字或送出就消失
const showDraftTag = computed(() => !!aiDraft.value && draft.value === aiDraft.value.text)
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
  const snapshot = draft.value
  suggesting.value = true
  try {
    const r = await inboxApi.suggest(convId, auto)
    if (!r || seq !== suggestSeq || convId !== props.conversation.id) return
    if (draft.value !== snapshot) {
      // 等待期間輸入框被改過：不覆蓋使用者打的字
      if (!auto) ElMessage.info('輸入框內容已變動，已略過 AI 建議')
      return
    }
    draft.value = r.text
    aiDraft.value = r
  } catch {
    // 錯誤由 http 攔截器提示
  } finally {
    if (seq === suggestSeq) suggesting.value = false
  }
}

async function onSuggestClick() {
  if (suggesting.value || !canSend.value) return
  // 已有自己打的字（不是原封不動的 AI 草稿）→ 先問要不要取代
  if (draft.value.trim() && !showDraftTag.value) {
    try {
      await ElMessageBox.confirm('輸入框已經有內容，要用 AI 建議取代嗎？', '取代輸入框內容', {
        confirmButtonText: '取代',
        cancelButtonText: '保留我的',
        type: 'warning',
      })
    } catch {
      return
    }
  }
  await runSuggest(false)
}

// 開啟「真人」處理、最後一則是顧客的對話，輸入框又是空的 → 自動擬一份放進去（不送出）
function maybeAutoSuggest() {
  const c = props.conversation
  if (!autoSuggest.value || !aiConfigured.value || suggesting.value) return
  if (c.handler !== 'human' || !canSend.value || !props.messages) return
  const last = props.messages[props.messages.length - 1]
  if (!last || last.direction !== 'in' || last.deleted) return
  if (draft.value.trim() || autoTried.has(c.id)) return
  autoTried.add(c.id)
  void runSuggest(true)
}
watch(
  () => props.conversation.id,
  () => {
    suggestSeq++ // 作廢上一段對話還在路上的建議
    suggesting.value = false
    autoTried.clear()
    if (showDraftTag.value) draft.value = '' // 沒動過的 AI 草稿不帶到別的對話
    aiDraft.value = null
  },
)
watch(
  [() => props.conversation.id, () => props.messages !== undefined, () => props.conversation.handler, aiConfigured, autoSuggest],
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

    <div ref="scroller" class="t-body" @scroll.passive="onScroll">
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
          AI 建議
        </el-button>
        <HelpTip :width="230">
          <label class="auto-row"><el-switch v-model="autoSuggest" size="small" />開啟對話時自動擬稿</label>
          <div class="auto-sub">草稿只放進輸入框，不會自動送出。</div>
        </HelpTip>
        <template v-if="showDraftTag && aiDraft">
          <span class="ai-tag">AI 草稿，送出前請確認</span>
          <HelpTip v-if="aiDraft.basis.length" :width="240">
            參考的知識庫：
            <ul class="basis">
              <li v-for="b in aiDraft.basis" :key="b">{{ b }}</li>
            </ul>
          </HelpTip>
        </template>
      </div>
      <div v-if="showDraftTag && aiDraft?.warning" class="ai-warn" role="status">
        <el-icon><WarningFilled /></el-icon>
        <span>{{ aiDraft.warning }}</span>
      </div>
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
    </footer>

    <el-dialog v-model="wrongOpen" width="min(460px, 92vw)" append-to-body align-center>
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
.auto-sub {
  margin-top: 4px;
  font-size: 12px;
  color: var(--lc-muted);
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
.via {
  margin-left: 6px;
  white-space: nowrap;
}
</style>
