<script setup lang="ts">
import HelpTip from '@/components/HelpTip.vue'
import DistillHint from './DistillHint.vue'
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Edit, Delete, Promotion, User } from '@element-plus/icons-vue'
import { automationApi, matchTypeLabels, type MatchType, type Rule } from '@/api/automation'
import { useIsMobile } from '@/composables/useIsMobile'
import { shortTime } from '@/utils/time'

// 編輯的是這個 IG 帳號的資料（父層切換帳號時以 key 重建本元件）
const props = defineProps<{ accountId: number; distillPending?: number }>()
const emit = defineEmits<{ 'go-distill': [] }>()

const isMobile = useIsMobile()
const rules = ref<Rule[]>([])
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    rules.value = await automationApi.rules(props.accountId)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// ---- 編輯 ----
const dialog = ref(false)
const saving = ref(false)
const empty = () => ({
  id: 0,
  name: '',
  matchType: 'contains' as MatchType,
  keywords: [] as string[],
  reply: '',
  handoff: false,
  enabled: true,
  priority: 100,
})
const form = reactive(empty())
const kwInput = ref('')

function openEditor(r?: Rule) {
  Object.assign(form, r ? { ...r, keywords: [...r.keywords] } : empty())
  kwInput.value = ''
  dialog.value = true
}

function addKeyword() {
  for (const k of kwInput.value.split(/[,，\n]/)) {
    const v = k.trim()
    if (v && !form.keywords.includes(v)) form.keywords.push(v)
  }
  kwInput.value = ''
}

async function save() {
  if (kwInput.value.trim()) addKeyword()
  saving.value = true
  try {
    if (form.id) {
      await automationApi.updateRule(props.accountId, { ...form, hitCount: 0, lastHitAt: null })
    } else {
      await automationApi.createRule(props.accountId, form)
    }
    ElMessage.success('已儲存')
    dialog.value = false
    await load()
  } finally {
    saving.value = false
  }
}

async function toggle(r: Rule) {
  await automationApi.updateRule(props.accountId, r)
}

async function remove(r: Rule) {
  if (!(await ElMessageBox.confirm(`刪除規則「${r.name}」？`, '刪除規則', { type: 'warning', confirmButtonText: '刪除', cancelButtonText: '取消' }).catch(() => false))) return
  await automationApi.deleteRule(props.accountId, r.id)
  rules.value = rules.value.filter((x) => x.id !== r.id)
}

const examples = [
  { name: '菜單', keywords: ['菜單', 'menu', '價目'], reply: '菜單在我們主頁的精選限動「MENU」喔 ☕ 有想喝的也可以直接問我！' },
  { name: '客訴轉真人', keywords: ['退錢', '退款', '客訴', '很失望'], reply: '', handoff: true },
]
function useExample(e: (typeof examples)[number]) {
  Object.assign(form, empty(), { ...e, keywords: [...e.keywords], handoff: !!e.handoff })
  dialog.value = true
}
</script>

<template>
  <div>
    <div class="bar">
      <span class="count">{{ rules.length }} 條規則</span>
      <div class="bar-right">
        <DistillHint :pending="distillPending ?? 0" @go="emit('go-distill')" />
        <el-button type="primary" :icon="Plus" @click="openEditor()">新增規則</el-button>
      </div>
    </div>

    <div v-loading="loading" class="grid">
      <div v-for="r in rules" :key="r.id" class="rule lc-card" :class="{ off: !r.enabled }">
        <div class="r-head">
          <span class="prio">#{{ r.priority }}</span>
          <span class="name">{{ r.name }}</span>
          <el-switch v-model="r.enabled" size="small" @change="toggle(r)" />
        </div>
        <div class="kws">
          <span class="mt">{{ matchTypeLabels[r.matchType] }}</span>
          <el-tag v-for="k in r.keywords" :key="k" size="small" effect="plain" round>{{ k }}</el-tag>
        </div>
        <div v-if="r.reply" class="reply">
          <el-icon><Promotion /></el-icon>
          <span>{{ r.reply }}</span>
        </div>
        <div v-if="r.handoff" class="handoff">
          <el-icon><User /></el-icon>
          命中後轉真人處理
        </div>
        <div class="r-foot">
          <span>命中 {{ r.hitCount }} 次<template v-if="r.lastHitAt"> · 最近 {{ shortTime(r.lastHitAt) }}</template></span>
          <span class="actions">
            <el-button text circle :icon="Edit" @click="openEditor(r)" />
            <el-button text circle :icon="Delete" @click="remove(r)" />
          </span>
        </div>
      </div>

      <div v-if="!loading && !rules.length" class="empty lc-card">
        <p>還沒有規則。從範例開始，或<a class="link" @click="emit('go-distill')">從對話蒸餾</a>：</p>
        <div class="examples">
          <button v-for="e in examples" :key="e.name" class="example" @click="useExample(e)">
            <strong>{{ e.name }}</strong>
            <span>{{ e.keywords.join('、') }}</span>
          </button>
        </div>
      </div>
    </div>

    <el-dialog
      v-model="dialog"
      :title="form.id ? '編輯規則' : '新增規則'"
      :fullscreen="isMobile"
      width="560px"
      align-center
      destroy-on-close
    >
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="規則名稱">
          <el-input v-model="form.name" placeholder="例如：菜單" maxlength="40" />
        </el-form-item>
        <el-form-item label="比對方式">
          <el-radio-group v-model="form.matchType">
            <el-radio-button value="contains">包含關鍵字</el-radio-button>
            <el-radio-button value="exact">完全相同</el-radio-button>
            <el-radio-button value="regex">正則</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item>
          <template #label>關鍵字<HelpTip text="任一命中即可；不分大小寫與全半形。" /></template>
          <div class="kw-edit">
            <el-tag
              v-for="k in form.keywords"
              :key="k"
              closable
              round
              @close="form.keywords = form.keywords.filter((x) => x !== k)"
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
            v-model="form.reply"
            type="textarea"
            :autosize="{ minRows: 3, maxRows: 8 }"
            maxlength="1000"
            show-word-limit
            placeholder="命中時送出的訊息；勾選轉真人時可留空"
          />
        </el-form-item>
        <div class="inline">
          <el-checkbox v-model="form.handoff">命中後轉真人處理（AI 暫停）</el-checkbox>
          <div class="prio-field">
            <span>優先順序<HelpTip text="數字小的先比對。" /></span>
            <el-input-number v-model="form.priority" :min="0" :max="9999" size="small" controls-position="right" />
          </div>
        </div>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">儲存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.bar-right {
  display: flex;
  align-items: center;
  gap: 10px;
}
.link {
  color: var(--el-color-primary);
  cursor: pointer;
  margin: 0 2px;
}
.bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
}
.count {
  font-size: 13px;
  color: var(--lc-muted);
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 14px;
  min-height: 80px;
}
.rule {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  &.off {
    opacity: 0.55;
  }
}
.r-head {
  display: flex;
  align-items: center;
  gap: 8px;
  .prio {
    font-size: 12px;
    color: var(--lc-muted);
    font-variant-numeric: tabular-nums;
  }
  .name {
    flex: 1;
    font-weight: 700;
  }
}
.kws {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
  .mt {
    font-size: 12px;
    color: var(--lc-muted);
    margin-right: 2px;
  }
}
.reply,
.handoff {
  display: flex;
  gap: 8px;
  font-size: 14px;
  line-height: 1.5;
  .el-icon {
    margin-top: 3px;
    flex-shrink: 0;
    color: var(--lc-caramel);
  }
}
.reply span {
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
  white-space: pre-wrap;
}
.handoff {
  color: var(--lc-tone-caramel-fg);
  font-size: 13px;
}
.r-foot {
  margin-top: auto;
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 12px;
  color: var(--lc-muted);
  .actions {
    margin-right: -6px;
  }
}
.empty {
  grid-column: 1 / -1;
  padding: 24px;
  p {
    margin: 0 0 12px;
    color: var(--lc-muted);
  }
}
.examples {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
.example {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  padding: 12px 16px;
  border: 1px dashed var(--el-color-primary-light-5);
  border-radius: 12px;
  background: var(--el-color-primary-light-9);
  font: inherit;
  cursor: pointer;
  span {
    font-size: 12px;
    color: var(--lc-muted);
  }
}
.kw-edit {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  width: 100%;
  align-items: center;
}
.kw-input {
  flex: 1;
  min-width: 200px;
}
.inline {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
}
.prio-field {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--lc-muted);
}
@media (max-width: 768px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
