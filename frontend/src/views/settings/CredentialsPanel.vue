<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import HelpTip from '@/components/HelpTip.vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  credentialsApi,
  type CredentialKey,
  type CredentialSource,
  type Credentials,
} from '@/api/credentials'

// 每張卡共用的存檔說明（放在 ? 提示裡）
const saveNote = ' 存檔後立即生效，不用重啟；後台儲存的值優先於 .env，「清除後台設定」會退回 .env；機密欄位加密存放，只顯示尾 4 碼。'

interface FieldDef {
  key: CredentialKey
  label: string
  secret?: boolean
  select?: string[]
  placeholder?: string
  help?: string
}
interface GroupDef {
  id: 'meta' | 'claude'
  title: string
  desc: string
  fields: FieldDef[]
}

const emit = defineEmits<{ saved: [] }>()

const groups: GroupDef[] = [
  {
    id: 'meta',
    title: 'Meta App（Instagram API with Unified Login）',
    desc: '連結 IG 帳號、接收私訊 webhook 用。填 Meta App ID／App Secret（應用程式設定 → 基本資料），以及 Facebook 登入「配置」的配置 ID。',
    fields: [
      {
        key: 'igAppId',
        label: 'Meta App ID',
        placeholder: '純數字',
        help: '應用程式設定 → 基本資料；設定頁「連結 IG 帳號」的 Facebook 登入授權用',
      },
      {
        key: 'igAppSecret',
        label: 'Meta App Secret',
        secret: true,
        help: '應用程式設定 → 基本資料；換 token 與驗證 webhook 簽章用',
      },
      { key: 'igConfigId', label: '配置 ID', placeholder: '純數字', help: 'Facebook 登入 → 配置（勾好 IG 權限的那一個）的 ID' },
      { key: 'igVerifyToken', label: 'Webhook 驗證 Token', secret: true, help: '要和 Meta 後台 webhook 欄位填的「驗證權杖」一致' },
    ],
  },
  {
    id: 'claude',
    title: 'Claude AI',
    desc: '自動回覆與 AI 寫文案用。沒有 API Key 時只會跑關鍵字規則。',
    fields: [
      { key: 'anthropicApiKey', label: 'Claude API Key', secret: true, placeholder: 'sk-ant-…' },
      { key: 'aiModel', label: '模型', placeholder: '例如 claude-opus-5-5' },
      { key: 'aiEffort', label: '推理強度', select: ['low', 'medium', 'high'], help: '越高越慢越貴；自動回覆建議 low' },
    ],
  },
]

const data = ref<Credentials | null>(null)
const loading = ref(true)
const draft = reactive<Record<CredentialKey, string>>({
  igAppId: '',
  igAppSecret: '',
  igConfigId: '',
  igVerifyToken: '',
  anthropicApiKey: '',
  aiModel: '',
  aiEffort: '',
})
// secret 欄位已有值時預設只顯示 ••••1234，按「更換」才出現輸入框
const replacing = reactive<Partial<Record<CredentialKey, boolean>>>({})
const saving = reactive<Record<GroupDef['id'], boolean>>({ meta: false, claude: false })
const testing = ref(false)
const testResult = ref<{ ok: boolean; text: string } | null>(null)

function syncDrafts(c: Credentials) {
  for (const g of groups) {
    for (const f of g.fields) {
      draft[f.key] = f.secret ? '' : (c[f.key].value ?? '')
      replacing[f.key] = false
    }
  }
}

async function load() {
  loading.value = true
  try {
    data.value = await credentialsApi.get()
    syncDrafts(data.value)
  } finally {
    loading.value = false
  }
}
onMounted(load)

const sourceMeta: Record<CredentialSource, { label: string; type: 'success' | 'info' | 'warning' }> = {
  db: { label: '後台設定', type: 'success' },
  env: { label: '.env', type: 'info' },
  none: { label: '未設定', type: 'warning' },
}

const masked = (f: FieldDef) => `••••${data.value?.[f.key].hint || '••••'}`
const showInput = (f: FieldDef) => !f.secret || !data.value?.[f.key].set || replacing[f.key]

function startReplace(f: FieldDef) {
  replacing[f.key] = true
  draft[f.key] = ''
}
function cancelReplace(f: FieldDef) {
  replacing[f.key] = false
  draft[f.key] = ''
}

// 這個欄位有沒有要送的變更：secret 有輸入就算；非 secret 和目前值不同且非空才算（空＝不變）
function dirty(f: FieldDef): boolean {
  const v = draft[f.key].trim()
  if (!v) return false
  return f.secret ? true : v !== (data.value?.[f.key].value ?? '')
}
const groupDirty = (g: GroupDef) => g.fields.some(dirty)

async function save(g: GroupDef) {
  const patch: Partial<Record<CredentialKey, string>> = {}
  for (const f of g.fields) if (dirty(f)) patch[f.key] = draft[f.key].trim()
  saving[g.id] = true
  try {
    data.value = await credentialsApi.update(patch)
    syncDrafts(data.value)
    testResult.value = null
    ElMessage.success('已儲存，立即生效')
    emit('saved')
  } catch {
    // 錯誤訊息已由 http 攔截器顯示
  } finally {
    saving[g.id] = false
  }
}

async function clearField(f: FieldDef) {
  try {
    await ElMessageBox.confirm(
      `會刪除後台儲存的「${f.label}」，改用 .env 的值${data.value?.[f.key].source === 'db' ? '（若 .env 也沒設就是未設定）' : ''}。`,
      '清除後台設定？',
      { confirmButtonText: '清除', cancelButtonText: '取消', type: 'warning' },
    )
  } catch {
    return
  }
  try {
    data.value = await credentialsApi.update({ clear: [f.key] })
    syncDrafts(data.value)
    testResult.value = null
    ElMessage.success('已清除後台設定')
    emit('saved')
  } catch {
    // 攔截器已顯示
  }
}

const keyDraft = computed(() => draft.anthropicApiKey.trim())
const canTest = computed(() => !!keyDraft.value || !!data.value?.anthropicApiKey.set)

async function testAi() {
  testing.value = true
  testResult.value = null
  try {
    const r = await credentialsApi.testAi(keyDraft.value || undefined)
    testResult.value = r.ok
      ? { ok: true, text: keyDraft.value ? '連線成功（這把 key 尚未儲存）' : '連線成功，API Key 有效' }
      : { ok: false, text: r.error || '測試失敗' }
  } catch {
    // 攔截器已顯示
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <div v-loading="loading" class="creds">
    <el-card v-for="g in groups" :key="g.id" shadow="never" class="card">
      <template #header>
        <div class="card-head">
          <span class="title">{{ g.title }}</span>
          <HelpTip :text="g.desc + saveNote" />
        </div>
      </template>

      <el-form label-position="top" @submit.prevent="save(g)">
        <el-form-item v-for="f in g.fields" :key="f.key">
          <template #label>
            {{ f.label }}
            <HelpTip v-if="f.help" :text="f.help" />
          </template>
          <div class="field">
            <div class="input-row">
              <template v-if="data && showInput(f)">
                <el-select v-if="f.select" v-model="draft[f.key]" class="grow">
                  <el-option v-for="o in f.select" :key="o" :label="o" :value="o" />
                </el-select>
                <el-input
                  v-else
                  v-model="draft[f.key]"
                  class="grow"
                  :type="f.secret ? 'password' : 'text'"
                  :show-password="f.secret"
                  :placeholder="f.placeholder"
                  :autocomplete="f.secret ? 'new-password' : 'off'"
                />
                <el-button v-if="f.secret && replacing[f.key]" text @click="cancelReplace(f)">取消</el-button>
              </template>
              <template v-else-if="data">
                <span class="masked">{{ masked(f) }}</span>
                <el-button size="small" @click="startReplace(f)">更換</el-button>
              </template>
            </div>
            <div v-if="data" class="meta">
              <el-tag size="small" effect="light" :type="sourceMeta[data[f.key].source].type">
                {{ sourceMeta[data[f.key].source].label }}
              </el-tag>
              <el-button
                v-if="data[f.key].source === 'db'"
                class="clear"
                size="small"
                text
                type="danger"
                @click="clearField(f)"
              >
                清除後台設定
              </el-button>
            </div>
          </div>
        </el-form-item>
      </el-form>

      <div class="actions">
        <el-button
          v-if="g.id === 'claude'"
          :loading="testing"
          :disabled="!canTest"
          @click="testAi"
        >
          測試連線
        </el-button>
        <el-button type="primary" :loading="saving[g.id]" :disabled="!groupDirty(g)" @click="save(g)">儲存</el-button>
      </div>
      <p v-if="g.id === 'claude' && testResult" class="result" :class="testResult.ok ? 'ok' : 'bad'" role="status">
        {{ testResult.text }}
      </p>
    </el-card>
  </div>
</template>

<style scoped lang="scss">
.creds {
  // 桌機並排兩張卡；窗格不夠寬（< 2×380px）自動退成單欄
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(380px, 1fr));
  gap: 16px;
  align-items: start;
}
@media (max-width: 480px) {
  .creds {
    grid-template-columns: minmax(0, 1fr);
  }
}
.card {
  border-radius: var(--lc-radius);
  background: var(--lc-surface);
  border-color: var(--lc-border);
}
.title {
  font-weight: 600;
  color: var(--lc-text);
}
.muted {
  color: var(--lc-muted);
  font-size: 13px;
}
.small {
  font-size: 12.5px;
}
.field {
  width: 100%;
}
.input-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 32px;
}
.grow {
  flex: 1;
  min-width: 0;
}
.masked {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  letter-spacing: 1px;
  color: var(--lc-text);
}
.meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 6px;
  line-height: 1.4;
  .clear {
    margin-left: auto;
  }
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 4px;
}
.result {
  margin: 10px 0 0;
  font-size: 13px;
  line-height: 1.5;
  text-align: right;
  word-break: break-word;
  &.ok {
    color: var(--el-color-success);
  }
  &.bad {
    color: var(--el-color-danger);
  }
}
@media (max-width: 640px) {
  .meta .clear {
    margin-left: 0;
  }
  .actions {
    flex-wrap: wrap;
  }
}
</style>
