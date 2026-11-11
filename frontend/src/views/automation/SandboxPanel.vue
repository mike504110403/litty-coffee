<script setup lang="ts">
import { nextTick, ref } from 'vue'
import { Promotion, RefreshLeft } from '@element-plus/icons-vue'
import { automationApi, intentLabels, type Outcome } from '@/api/automation'

const props = defineProps<{ aiConfigured: boolean; accountId: number }>()

interface Line {
  fromCustomer: boolean
  text: string
  outcome?: Outcome
  elapsedMs?: number
}

const lines = ref<Line[]>([])
const draft = ref('')
const busy = ref(false)
const scroller = ref<HTMLElement>()

const samples = ['請問今天有營業嗎？', '你們在哪裡？捷運怎麼去', '拿鐵多少錢', '週六下午 6 個人可以訂位嗎', '上次的蛋糕有頭髮，很失望']

const kindLabel: Record<Outcome['kind'], string> = {
  rule: '關鍵字規則',
  ai: 'AI 回覆',
  handoff: '轉真人',
  block: 'AI 判斷騷擾',
  none: '不回覆',
}

async function send(text = draft.value) {
  text = text.trim()
  if (!text || busy.value) return
  const history = lines.value.map((l) => ({ fromCustomer: l.fromCustomer, text: l.text })).filter((l) => l.text)
  lines.value.push({ fromCustomer: true, text })
  draft.value = ''
  busy.value = true
  await scrollDown()
  try {
    const { outcome, elapsedMs } = await automationApi.test(props.accountId, text, history)
    lines.value.push({ fromCustomer: false, text: outcome.reply, outcome, elapsedMs })
  } catch {
    lines.value.pop()
    draft.value = text
  } finally {
    busy.value = false
    await scrollDown()
  }
}

async function scrollDown() {
  await nextTick()
  scroller.value?.scrollTo({ top: scroller.value.scrollHeight, behavior: 'smooth' })
}

function onKeydown(ev: Event | KeyboardEvent) {
  const e = ev as KeyboardEvent
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
    e.preventDefault()
    void send()
  }
}
</script>

<template>
  <div class="sandbox lc-card">
    <div class="s-head">
      <div>
        <strong>試答沙盒</strong>
        <span>用目前的規則與知識庫試回覆，不會送給任何人（AI 開關關閉時也會試 AI）。</span>
      </div>
      <el-button v-if="lines.length" text :icon="RefreshLeft" @click="lines = []">清空</el-button>
    </div>

    <div ref="scroller" class="s-body">
      <div v-if="!lines.length" class="s-empty">
        <p>扮演客人傳一則訊息試試：</p>
        <div class="samples">
          <el-tag v-for="s in samples" :key="s" effect="plain" round class="sample" @click="send(s)">{{ s }}</el-tag>
        </div>
        <p v-if="!aiConfigured" class="warn">AI 未設定：只會測到關鍵字規則。</p>
      </div>

      <div v-for="(l, i) in lines" :key="i" class="msg" :class="l.fromCustomer ? 'in' : 'out'">
        <div v-if="l.text" class="bubble" :class="l.outcome?.kind">{{ l.text }}</div>
        <div v-if="l.outcome" class="trace">
          <span class="kind" :class="l.outcome.kind">{{ kindLabel[l.outcome.kind] }}</span>
          <span v-if="l.outcome.rule">命中「{{ l.outcome.rule.name }}」</span>
          <span v-if="l.outcome.ai">意圖：{{ intentLabels[l.outcome.ai.intent] ?? l.outcome.ai.intent }}</span>
          <span v-if="l.elapsedMs != null">{{ (l.elapsedMs / 1000).toFixed(1) }} 秒</span>
          <div v-if="l.outcome.ai?.reason" class="reason">AI 判斷：{{ l.outcome.ai.reason }}</div>
          <div v-if="l.outcome.kind === 'block'" class="reason">
            {{ l.outcome.detail }}。正式環境會把這位顧客列入黑名單、不回覆（沙盒不會寫入）。
          </div>
          <div v-else-if="l.outcome.detail" class="reason">{{ l.outcome.detail }}</div>
        </div>
      </div>
      <div v-if="busy" class="msg out">
        <div class="bubble typing"><span /><span /><span /></div>
      </div>
    </div>

    <div class="s-foot">
      <el-input v-model="draft" placeholder="以客人身分輸入…" :disabled="busy" @keydown="onKeydown" />
      <el-button type="primary" circle :icon="Promotion" :disabled="!draft.trim() || busy" @click="send()" />
    </div>
  </div>
</template>

<style scoped lang="scss">
.sandbox {
  display: flex;
  flex-direction: column;
  height: min(640px, calc(100vh - 240px));
  min-height: 420px;
  max-width: 760px;
  overflow: hidden;
}
.s-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--lc-border);
  span {
    display: block;
    font-size: 12px;
    color: var(--lc-muted);
    margin-top: 2px;
  }
}
.s-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  background: var(--lc-bg-soft);
}
.s-empty {
  text-align: center;
  color: var(--lc-muted);
  font-size: 14px;
  padding-top: 24px;
  .warn {
    color: var(--lc-tone-amber-fg);
    font-size: 13px;
  }
}
.samples {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 8px;
  margin: 12px 0;
}
.sample {
  cursor: pointer;
}
.msg {
  display: flex;
  flex-direction: column;
  margin: 6px 0 10px;
  &.in {
    align-items: flex-end; // 沙盒裡「客人」是自己，放右邊
  }
  &.out {
    align-items: flex-start;
  }
}
.bubble {
  max-width: 78%;
  padding: 9px 13px;
  border-radius: 18px;
  font-size: 15px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}
.in .bubble {
  background: var(--lc-espresso);
  color: var(--lc-on-fill);
  border-bottom-right-radius: 6px;
}
.out .bubble {
  background: var(--lc-surface);
  border: 1px solid var(--lc-border);
  border-bottom-left-radius: 6px;
  &.handoff {
    background: var(--lc-warn-bg);
    border-color: var(--lc-warn-line);
  }
}
.trace {
  max-width: 78%;
  margin-top: 5px;
  font-size: 12px;
  color: var(--lc-muted);
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  .reason {
    flex-basis: 100%;
    line-height: 1.5;
  }
}
.kind {
  font-weight: 700;
  &.rule {
    color: var(--lc-roast);
  }
  &.ai {
    color: var(--lc-tone-sage-fg);
  }
  &.handoff {
    color: var(--lc-tone-caramel-fg);
  }
  &.block {
    color: var(--lc-fail-fg);
  }
}
.typing {
  display: flex;
  gap: 4px;
  padding: 13px 14px;
  span {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--lc-muted);
    animation: blink 1.2s infinite ease-in-out;
    &:nth-child(2) {
      animation-delay: 0.2s;
    }
    &:nth-child(3) {
      animation-delay: 0.4s;
    }
  }
}
@keyframes blink {
  0%,
  80%,
  100% {
    opacity: 0.25;
  }
  40% {
    opacity: 1;
  }
}
.s-foot {
  display: flex;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--lc-border);
  :deep(.el-input__wrapper) {
    border-radius: 20px;
  }
}
@media (max-width: 768px) {
  .sandbox {
    height: calc(100vh - 220px - var(--lc-tabbar-h));
    margin: 0 -4px;
  }
  .s-foot {
    padding-bottom: 10px;
  }
}
</style>
