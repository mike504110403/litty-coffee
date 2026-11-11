<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { MagicStick, Check, Edit, Close, ChatLineSquare, User, Promotion, Warning } from '@element-plus/icons-vue'
import HelpTip from '@/components/HelpTip.vue'
import {
  confidenceLabels,
  distillApi,
  matchTypeLabels,
  type DistillDays,
  type DistillItem,
  type DistillKnowledgeDraft,
  type DistillRuleDraft,
  type DistillRun,
  type MatchType,
} from '@/api/automation'
import { useIsMobile } from '@/composables/useIsMobile'
import { shortTime } from '@/utils/time'

// 蒸餾建議：AI 從近期對話整理出的規則／知識庫建議，店家逐筆採用、編輯後採用或略過；不採用就不會生效
const props = defineProps<{ accountId: number; aiConfigured: boolean }>()
const emit = defineEmits<{ changed: [pending: number] }>()

const isMobile = useIsMobile()
const days = ref<DistillDays>(30)
const status = ref<DistillItem['status']>('pending')
const items = ref<DistillItem[]>([])
const runs = ref<DistillRun[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const [list, rs] = await Promise.all([distillApi.suggestions(props.accountId, status.value), distillApi.runs(props.accountId)])
    items.value = list
    runs.value = rs
    selected.clear()
    if (status.value === 'pending') emit('changed', list.length)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(status, load)

// ---- 蒸餾 ----
const running = ref(false)
const elapsed = ref(0)
let timer: ReturnType<typeof setInterval> | undefined
onBeforeUnmount(() => clearInterval(timer))

async function distill() {
  running.value = true
  elapsed.value = 0
  const start = Date.now()
  timer = setInterval(() => (elapsed.value = Math.floor((Date.now() - start) / 1000)), 1000)
  try {
    const run = await distillApi.run(props.accountId, days.value)
    if (run.status === 'empty') ElMessage.info('這段期間沒有真人回覆或待改進問答可以整理')
    else ElMessage.success(`整理出 ${run.suggestionCount} 筆建議`)
    status.value = 'pending'
    await load()
  } catch {
    await load() // 失敗也會留下一筆紀錄
  } finally {
    clearInterval(timer)
    running.value = false
  }
}

const lastRun = computed(() => runs.value[0])
const runStatusText: Record<DistillRun['status'], string> = { running: '進行中', done: '完成', failed: '失敗', empty: '沒有素材' }
const lastRunText = computed(() => {
  const r = lastRun.value
  if (!r) return ''
  const parts = [`${shortTime(r.createdAt)}${r.trigger === 'weekly' ? '（每週自動）' : ''}`, `近 ${r.days} 天`]
  if (r.status === 'done') parts.push(`${r.suggestionCount} 筆建議`, `${r.pairCount} 組問答、${r.reviewCount} 筆待改進`)
  else parts.push(runStatusText[r.status])
  return parts.join(' · ')
})

// ---- 清單 ----
const rules = computed(() => items.value.filter((i) => i.suggestion.type === 'rule'))
const knowledge = computed(() => items.value.filter((i) => i.suggestion.type === 'knowledge'))
const groups = computed(() =>
  [
    { key: 'rule', title: '關鍵字規則', list: rules.value },
    { key: 'knowledge', title: '知識庫', list: knowledge.value },
  ].filter((g) => g.list.length),
)

const expanded = reactive(new Set<number>())
const toggleEvidence = (id: number) => (expanded.has(id) ? expanded.delete(id) : expanded.add(id))

// 批次：只選得到不必補內容的
const selected = reactive(new Set<number>())
const selectable = computed(() => items.value.filter((i) => i.status === 'pending' && !i.suggestion.needsInput))
const allChecked = computed({
  get: () => selectable.value.length > 0 && selectable.value.every((i) => selected.has(i.id)),
  set: (v: boolean) => {
    selected.clear()
    if (v) selectable.value.forEach((i) => selected.add(i.id))
  },
})
const someChecked = computed(() => selected.size > 0 && !allChecked.value)
const toggleSelect = (id: number, v: boolean) => (v ? selected.add(id) : selected.delete(id))

const busy = ref<number | null>(null)
const batching = ref(false)

function settle(done: DistillItem) {
  selected.delete(done.id)
  if (status.value === 'pending') items.value = items.value.filter((x) => x.id !== done.id)
  else items.value = items.value.map((x) => (x.id === done.id ? done : x))
  if (status.value === 'pending') emit('changed', items.value.length)
}

async function accept(it: DistillItem) {
  busy.value = it.id
  try {
    settle(await distillApi.accept(it.id))
    ElMessage.success(it.suggestion.type === 'rule' ? '已套用到關鍵字規則' : '已套用到知識庫')
  } finally {
    busy.value = null
  }
}

async function dismiss(it: DistillItem) {
  busy.value = it.id
  try {
    settle(await distillApi.dismiss(it.id))
  } finally {
    busy.value = null
  }
}

// 沒勾任何一筆＝全部採用；有勾＝只採用勾選的
const batchTargets = computed(() =>
  selected.size ? selectable.value.filter((i) => selected.has(i.id)) : selectable.value,
)
async function acceptSelected() {
  const list = batchTargets.value
  if (!list.length) return
  const ok0 = await ElMessageBox.confirm(`採用 ${list.length} 筆建議，立即套用到規則與知識庫？`, selected.size ? '採用已選' : '全部採用', {
    confirmButtonText: '採用',
    cancelButtonText: '取消',
  }).catch(() => false)
  if (!ok0) return
  batching.value = true
  let ok = 0
  try {
    for (const it of list) {
      try {
        settle(await distillApi.accept(it.id))
        ok++
      } catch {
        /* 錯誤訊息已由 http 攔截器顯示，繼續下一筆 */
      }
    }
  } finally {
    batching.value = false
  }
  if (ok) ElMessage.success(`已採用 ${ok} 筆`)
}

// ---- 編輯後採用 ----
const dialog = ref(false)
const editing = ref<DistillItem | null>(null)
const ruleForm = reactive<DistillRuleDraft>({ name: '', keywords: [], matchType: 'contains', reply: '', handoff: false })
const kbForm = reactive<DistillKnowledgeDraft>({ title: '', content: '' })
const kwInput = ref('')
const saving = ref(false)

function openEditor(it: DistillItem) {
  editing.value = it
  const s = it.suggestion
  if (s.rule) Object.assign(ruleForm, { ...s.rule, keywords: [...s.rule.keywords] })
  if (s.knowledge) Object.assign(kbForm, { ...s.knowledge })
  kwInput.value = ''
  dialog.value = true
}

function addKeyword() {
  for (const k of kwInput.value.split(/[,，\n]/)) {
    const v = k.trim()
    if (v && !ruleForm.keywords.includes(v)) ruleForm.keywords.push(v)
  }
  kwInput.value = ''
}

const placeholderRe = /請填寫|\[已遮蔽\]/
// needsInput：內容一定要補（不能留「請填寫」「[已遮蔽]」）
const missing = computed(() => {
  const it = editing.value
  if (!it) return ''
  if (it.suggestion.type === 'knowledge') {
    if (!kbForm.title.trim()) return '請填標題'
    if (!kbForm.content.trim()) return '請填內容'
    if (placeholderRe.test(kbForm.content)) return '還有「請填寫」或「[已遮蔽]」的地方'
  } else {
    if (!ruleForm.name.trim()) return '請填規則名稱'
    if (!ruleForm.keywords.length && !kwInput.value.trim()) return '至少要一個關鍵字'
    if (!ruleForm.reply.trim() && !ruleForm.handoff) return '請填回覆內容，或勾選轉真人'
    if (placeholderRe.test(ruleForm.reply)) return '還有「請填寫」或「[已遮蔽]」的地方'
  }
  return ''
})

async function saveEdit() {
  const it = editing.value
  if (!it) return
  if (kwInput.value.trim()) addKeyword()
  if (missing.value) return
  saving.value = true
  try {
    const edit =
      it.suggestion.type === 'rule'
        ? { rule: { ...ruleForm, keywords: [...ruleForm.keywords] } }
        : { knowledge: { ...kbForm } }
    settle(await distillApi.accept(it.id, edit))
    ElMessage.success('已採用')
    dialog.value = false
  } finally {
    saving.value = false
  }
}

// ---- 差異 ----
type DiffLine = { kind: 'same' | 'add' | 'del'; text: string }
// 逐行 LCS（知識庫內容通常幾十行以內）
function lineDiff(before: string, after: string): DiffLine[] {
  const a = before.split('\n')
  const b = after.split('\n')
  const dp = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0))
  for (let i = a.length - 1; i >= 0; i--)
    for (let j = b.length - 1; j >= 0; j--)
      dp[i]![j] = a[i] === b[j] ? dp[i + 1]![j + 1]! + 1 : Math.max(dp[i + 1]![j]!, dp[i]![j + 1]!)
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ kind: 'same', text: a[i]! })
      i++
      j++
    } else if (dp[i + 1]![j]! >= dp[i]![j + 1]!) out.push({ kind: 'del', text: a[i++]! })
    else out.push({ kind: 'add', text: b[j++]! })
  }
  while (i < a.length) out.push({ kind: 'del', text: a[i++]! })
  while (j < b.length) out.push({ kind: 'add', text: b[j++]! })
  return out
}

function keywordDiff(it: DistillItem) {
  const now = it.suggestion.rule?.keywords ?? []
  const before = it.suggestion.beforeRule?.keywords
  if (!before) return now.map((k) => ({ k, kind: 'same' as const }))
  return [
    ...now.map((k) => ({ k, kind: before.includes(k) ? ('same' as const) : ('add' as const) })),
    ...before.filter((k) => !now.includes(k)).map((k) => ({ k, kind: 'del' as const })),
  ]
}

const decidedText = (it: DistillItem) =>
  `${it.status === 'accepted' ? (it.suggestion.edited ? '已編輯後採用' : '已採用') : '已略過'}${
    it.decidedAt ? ` · ${shortTime(it.decidedAt)}` : ''
  }${it.decidedBy ? ` · ${it.decidedBy}` : it.status === 'dismissed' ? ' · 新一批建議取代' : ''}`

const matchTypeOptions: MatchType[] = ['contains', 'exact', 'regex']
</script>

<template>
  <div class="distill">
    <section class="head lc-card">
      <div class="run-row">
        <el-segmented
          v-model="days"
          :disabled="running"
          :options="[
            { label: '近 7 天', value: 7 },
            { label: '近 30 天', value: 30 },
            { label: '近 90 天', value: 90 },
          ]"
        />
        <el-button
          type="primary"
          :icon="MagicStick"
          :loading="running"
          :disabled="!aiConfigured"
          class="run-btn"
          @click="distill"
        >
          {{ running ? `蒸餾中… ${elapsed} 秒` : '從對話蒸餾' }}
        </el-button>
        <HelpTip
          v-if="!aiConfigured"
          text="尚未設定 Claude API Key：請超管到設定頁「憑證」填寫後才能蒸餾。"
        />
        <HelpTip
          v-else
          text="把這段期間店員的真人回覆、標記「回答有誤」與 AI 沒把握的問題交給 AI，整理成規則與知識庫建議（約一分鐘）。只送文字，顧客名稱不送、電話與 Email 會遮蔽；建議要你採用後才生效。每個帳號 10 分鐘最多一次。"
        />
      </div>
      <div v-if="lastRun" class="last" :class="lastRun.status">
        <span class="lbl">上次</span>
        <span>{{ lastRunText }}</span>
        <span v-if="lastRun.status === 'failed' && lastRun.error" class="err">{{ lastRun.error }}</span>
      </div>
    </section>

    <div class="bar">
      <el-segmented
        v-model="status"
        :options="[
          { label: '待審核', value: 'pending' },
          { label: '已採用', value: 'accepted' },
          { label: '已略過', value: 'dismissed' },
        ]"
      />
      <div v-if="status === 'pending' && selectable.length" class="batch">
        <el-checkbox v-model="allChecked" :indeterminate="someChecked">全選</el-checkbox>
        <el-button
          type="primary"
          plain
          :icon="Check"
          :loading="batching"
          @click="acceptSelected"
        >
          {{ selected.size && !allChecked ? '採用已選' : '全部採用' }}（{{ batchTargets.length }}）
        </el-button>
        <HelpTip text="需要補內容的建議不會被批次採用，請用「編輯後採用」。" />
      </div>
    </div>

    <div v-loading="loading" class="groups">
      <section v-for="g in groups" :key="g.key" class="group">
        <h3 class="g-title">{{ g.title }}<span class="n">{{ g.list.length }}</span></h3>
        <div class="cards">
          <article
            v-for="it in g.list"
            :key="it.id"
            class="card lc-card"
            :class="{ decided: it.status !== 'pending', picked: selected.has(it.id) }"
          >
            <header>
              <el-checkbox
                v-if="it.status === 'pending' && !it.suggestion.needsInput"
                :model-value="selected.has(it.id)"
                @update:model-value="(v) => toggleSelect(it.id, !!v)"
              />
              <span class="act" :class="it.suggestion.action">{{ it.suggestion.action === 'update' ? '修改' : '新增' }}</span>
              <span v-if="it.suggestion.needsInput && it.status === 'pending'" class="need"><el-icon><Warning /></el-icon>待補內容</span>
              <span class="conf" :class="it.suggestion.confidence">{{ confidenceLabels[it.suggestion.confidence] }}</span>
              <span class="time">{{ shortTime(it.createdAt) }}</span>
            </header>

            <!-- 規則 -->
            <div v-if="it.suggestion.rule" class="body">
              <div class="title">
                <template v-if="it.suggestion.beforeRule && it.suggestion.beforeRule.name !== it.suggestion.rule.name">
                  <del>{{ it.suggestion.beforeRule.name }}</del> →
                </template>
                {{ it.suggestion.rule.name }}
              </div>
              <div class="kws">
                <span class="mt">{{ matchTypeLabels[it.suggestion.rule.matchType] }}</span>
                <span v-for="k in keywordDiff(it)" :key="k.kind + k.k" class="kw" :class="k.kind">{{ k.k }}</span>
              </div>
              <div
                v-if="it.suggestion.beforeRule && it.suggestion.beforeRule.reply !== it.suggestion.rule.reply && it.suggestion.beforeRule.reply"
                class="reply old"
              >
                <el-icon><Promotion /></el-icon><del>{{ it.suggestion.beforeRule.reply }}</del>
              </div>
              <div v-if="it.suggestion.rule.reply" class="reply">
                <el-icon><Promotion /></el-icon><span>{{ it.suggestion.rule.reply }}</span>
              </div>
              <div v-if="it.suggestion.rule.handoff" class="handoff"><el-icon><User /></el-icon>命中後轉真人處理</div>
            </div>

            <!-- 知識庫 -->
            <div v-else-if="it.suggestion.knowledge" class="body">
              <div class="title">
                <template v-if="it.suggestion.beforeKnowledge && it.suggestion.beforeKnowledge.title !== it.suggestion.knowledge.title">
                  <del>{{ it.suggestion.beforeKnowledge.title }}</del> →
                </template>
                {{ it.suggestion.knowledge.title }}
              </div>
              <div v-if="it.suggestion.beforeKnowledge" class="diff">
                <div
                  v-for="(l, i) in lineDiff(it.suggestion.beforeKnowledge.content, it.suggestion.knowledge.content)"
                  :key="i"
                  class="dl"
                  :class="l.kind"
                >
                  <span class="sign">{{ l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : '' }}</span>{{ l.text || ' ' }}
                </div>
              </div>
              <p v-else class="content">{{ it.suggestion.knowledge.content || '（待填寫）' }}</p>
            </div>

            <p v-if="it.suggestion.reason" class="reason">{{ it.suggestion.reason }}</p>

            <div v-if="it.suggestion.evidence.length" class="evidence">
              <button type="button" class="ev-toggle" @click="toggleEvidence(it.id)">
                <el-icon><ChatLineSquare /></el-icon>
                對話節錄（{{ it.suggestion.evidence.length }}）
              </button>
              <ul v-if="expanded.has(it.id)">
                <li v-for="(e, i) in it.suggestion.evidence" :key="i">{{ e }}</li>
              </ul>
            </div>

            <footer>
              <span v-if="it.status !== 'pending'" class="done" :class="it.status">{{ decidedText(it) }}</span>
              <div v-else class="actions">
                <el-button
                  v-if="!it.suggestion.needsInput"
                  size="small"
                  round
                  type="primary"
                  :icon="Check"
                  :loading="busy === it.id"
                  :disabled="batching"
                  @click="accept(it)"
                >
                  採用
                </el-button>
                <el-button
                  size="small"
                  round
                  :type="it.suggestion.needsInput ? 'primary' : 'default'"
                  :icon="Edit"
                  :disabled="batching || busy === it.id"
                  @click="openEditor(it)"
                >
                  {{ it.suggestion.needsInput ? '補內容後採用' : '編輯後採用' }}
                </el-button>
                <el-button size="small" round text :icon="Close" :disabled="batching || busy === it.id" @click="dismiss(it)">
                  略過
                </el-button>
              </div>
            </footer>
          </article>
        </div>
      </section>
      <el-empty
        v-if="!loading && !items.length"
        :image-size="90"
        :description="status === 'pending' ? (aiConfigured ? '沒有待審核的建議：選好範圍後按「從對話蒸餾」' : '沒有待審核的建議') : '沒有符合的記錄'"
      />
    </div>

    <el-dialog
      class="lc-fs-dialog"
      v-model="dialog"
      :title="editing?.suggestion.type === 'rule' ? '編輯後採用：關鍵字規則' : '編輯後採用：知識庫'"
      :fullscreen="isMobile"
      width="580px"
      align-center
      destroy-on-close
    >
      <el-form v-if="editing" label-position="top" @submit.prevent>
        <template v-if="editing.suggestion.type === 'rule'">
          <el-form-item label="規則名稱">
            <el-input v-model="ruleForm.name" maxlength="40" />
          </el-form-item>
          <el-form-item label="比對方式">
            <el-radio-group v-model="ruleForm.matchType">
              <el-radio-button v-for="m in matchTypeOptions" :key="m" :value="m">{{ matchTypeLabels[m] }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item>
            <template #label>關鍵字<HelpTip text="任一命中即可；不分大小寫與全半形。" /></template>
            <div class="kw-edit">
              <el-tag
                v-for="k in ruleForm.keywords"
                :key="k"
                closable
                round
                @close="ruleForm.keywords = ruleForm.keywords.filter((x) => x !== k)"
              >
                {{ k }}
              </el-tag>
              <el-input
                v-model="kwInput"
                class="kw-input"
                placeholder="輸入後按 Enter，可用逗號一次加多個"
                @keydown.enter.prevent="addKeyword"
                @blur="kwInput.trim() && addKeyword()"
              />
            </div>
          </el-form-item>
          <el-form-item label="自動回覆內容">
            <el-input
              v-model="ruleForm.reply"
              type="textarea"
              :autosize="{ minRows: 3, maxRows: 8 }"
              maxlength="1000"
              show-word-limit
            />
          </el-form-item>
          <el-checkbox v-model="ruleForm.handoff">命中後轉真人處理（AI 暫停）</el-checkbox>
        </template>
        <template v-else>
          <el-form-item label="標題">
            <el-input v-model="kbForm.title" maxlength="60" />
          </el-form-item>
          <el-form-item>
            <template #label>
              內容
              <HelpTip
                v-if="editing.suggestion.needsInput"
                text="AI 在對話裡找不到這項資訊的答案，請補上實際內容（把「請填寫」換成真的資料）。"
              />
            </template>
            <el-input
              v-model="kbForm.content"
              type="textarea"
              :autosize="{ minRows: 5, maxRows: 14 }"
              maxlength="3000"
              show-word-limit
            />
          </el-form-item>
        </template>
      </el-form>
      <template #footer>
        <span v-if="missing" class="missing">{{ missing }}</span>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!!missing" @click="saveEdit">採用</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.head {
  padding: 14px 16px;
  margin-bottom: 14px;
}
.run-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}
.run-btn {
  min-width: 132px;
}
.last {
  margin-top: 10px;
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 8px;
  font-size: 13px;
  color: var(--lc-muted);
  .lbl {
    padding: 0 8px;
    border-radius: 999px;
    background: var(--lc-bg-chip);
    font-size: 12px;
  }
  &.failed {
    color: var(--lc-tone-rose-fg);
  }
  .err {
    color: var(--lc-tone-rose-fg);
  }
}
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 14px;
}
.batch {
  display: flex;
  align-items: center;
  gap: 10px;
}
.groups {
  min-height: 120px;
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.g-title {
  margin: 0 0 10px;
  font-size: 15px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 8px;
  .n {
    font-size: 12px;
    font-weight: 500;
    color: var(--lc-muted);
    background: var(--lc-bg-chip);
    padding: 0 8px;
    border-radius: 999px;
  }
}
.cards {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
@media (min-width: 1100px) {
  .cards {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    align-items: start;
  }
}
.card {
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  &.picked {
    box-shadow: 0 0 0 2px var(--el-color-primary-light-5);
  }
  &.decided {
    opacity: 0.8;
  }
  header {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    font-size: 12px;
  }
}
.act,
.need,
.conf {
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 600;
  display: inline-flex;
  align-items: center;
  gap: 3px;
}
.act {
  background: var(--lc-tone-sage-bg);
  color: var(--lc-tone-sage-fg);
  &.update {
    background: var(--lc-tone-caramel-bg);
    color: var(--lc-tone-caramel-fg);
  }
}
.need {
  color: var(--lc-tone-amber-fg);
  background: var(--lc-bg-chip);
}
.conf {
  font-weight: 500;
  color: var(--lc-muted);
  background: var(--lc-bg-chip);
  &.high {
    color: var(--lc-tone-sage-fg);
  }
  &.low {
    color: var(--lc-tone-rose-fg);
  }
}
.time {
  margin-left: auto;
  color: var(--lc-muted);
}
.body {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.title {
  font-weight: 700;
  font-size: 15px;
  del {
    font-weight: 400;
    color: var(--lc-muted);
  }
}
.kws {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.mt {
  font-size: 12px;
  color: var(--lc-muted);
}
.kw {
  font-size: 12px;
  padding: 1px 9px;
  border-radius: 999px;
  border: 1px solid var(--lc-border);
  &.add {
    border-color: transparent;
    background: var(--lc-tone-sage-bg);
    color: var(--lc-tone-sage-fg);
  }
  &.del {
    text-decoration: line-through;
    border-color: transparent;
    background: var(--lc-tone-rose-bg);
    color: var(--lc-tone-rose-fg);
  }
}
.reply {
  display: flex;
  gap: 6px;
  align-items: flex-start;
  padding: 6px 10px;
  border-radius: 10px;
  background: var(--el-color-primary-light-8);
  font-size: 14px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
  .el-icon {
    margin-top: 3px;
    flex-shrink: 0;
  }
  &.old {
    background: var(--lc-tone-rose-bg);
    color: var(--lc-tone-rose-fg);
  }
}
.handoff {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--lc-tone-caramel-fg);
}
.content {
  margin: 0;
  font-size: 14px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}
.diff {
  border-radius: 10px;
  border: 1px solid var(--lc-border);
  overflow: hidden;
  font-size: 13px;
  line-height: 1.6;
}
.dl {
  padding: 1px 10px;
  white-space: pre-wrap;
  word-break: break-word;
  .sign {
    display: inline-block;
    width: 14px;
    color: var(--lc-muted);
  }
  &.add {
    background: var(--lc-tone-sage-bg);
    color: var(--lc-tone-sage-fg);
  }
  &.del {
    background: var(--lc-tone-rose-bg);
    color: var(--lc-tone-rose-fg);
    text-decoration: line-through;
  }
}
.reason {
  margin: 0;
  font-size: 13px;
  color: var(--lc-muted);
  line-height: 1.5;
}
.evidence {
  font-size: 13px;
  ul {
    margin: 6px 0 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  li {
    padding: 4px 10px;
    border-left: 3px solid var(--lc-border);
    color: var(--lc-text);
    background: var(--lc-bg-soft);
    border-radius: 0 6px 6px 0;
    word-break: break-word;
  }
}
.ev-toggle {
  border: 0;
  background: none;
  padding: 0;
  font: inherit;
  font-size: 12px;
  color: var(--el-color-primary);
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  margin-top: 2px;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  .el-button + .el-button {
    margin-left: 0;
  }
}
.done {
  margin-right: auto;
  font-size: 12px;
  color: var(--lc-muted);
  &.accepted {
    color: var(--lc-tone-sage-fg);
  }
}
.kw-edit {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  width: 100%;
  .kw-input {
    flex: 1;
    min-width: 200px;
  }
}
.missing {
  margin-right: 12px;
  font-size: 12px;
  color: var(--lc-tone-amber-fg);
}
@media (max-width: 768px) {
  .run-row .el-segmented {
    width: 100%;
  }
  .run-btn {
    flex: 1;
  }
  .actions {
    width: 100%;
    .el-button {
      flex: 1;
    }
  }
}

@media (max-width: 768px) {
  // 手機字級 ≥13px（桌機維持原值）
  .last .lbl,
  .g-title .n,
  .card header,
  .conf,
  .mt,
  .kw,
  .ev-toggle,
  .done,
  .missing {
    font-size: 13px;
  }
}
</style>
