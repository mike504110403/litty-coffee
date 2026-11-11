<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { CopyDocument } from '@element-plus/icons-vue'
import { settingsApi, type SettingsInfo } from '@/api/settings'
import { useThemeStore, type ThemeMode } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import AdminUsersPanel from '@/views/settings/AdminUsersPanel.vue'
import CredentialsPanel from '@/views/settings/CredentialsPanel.vue'
import IgAccountsPanel from '@/views/settings/IgAccountsPanel.vue'
import { useAccountsStore } from '@/stores/accounts'

const theme = useThemeStore()
const themeOptions: { label: string; value: ThemeMode }[] = [
  { label: '跟隨系統', value: 'system' },
  { label: '淺色', value: 'light' },
  { label: '深色', value: 'dark' },
]

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
// IG 帳號與串接資訊需要 ig_accounts；外觀與自己的密碼所有人都能用
const canIg = computed(() => auth.can('ig_accounts'))
const subtitle = computed(() =>
  [
    canIg.value && 'IG 帳號連結',
    auth.can('credentials') && '憑證',
    auth.can('admins') && '管理員',
    '外觀與密碼',
  ]
    .filter(Boolean)
    .join('、'),
)

const info = ref<SettingsInfo | null>(null)
const accountsStore = useAccountsStore()

// 憑證存檔後 IG 卡片的「能否連結」要跟著更新
function onCredsSaved() {
  if (canIg.value) void load()
}

async function load() {
  info.value = await settingsApi.get()
}

onMounted(async () => {
  // OAuth callback 導回來時帶的結果
  const ok = route.query.ig === 'connected'
  const err = typeof route.query.ig_error === 'string' ? route.query.ig_error : ''
  if (ok || err) {
    router.replace({ query: {} })
    void accountsStore.fetch()
    if (ok) ElMessage.success('IG 帳號已連結')
    else ElMessage({ type: 'error', message: err, duration: 8000, showClose: true })
  }
  if (canIg.value) await load()
})

const integ = computed(() => info.value?.integration)

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已複製')
  } catch {
    ElMessage.warning('無法自動複製，請手動選取')
  }
}

const insecurePublic = computed(() => !!integ.value && !integ.value.publicBaseUrl.startsWith('https://'))

// ---- 改密碼 ----
const pwForm = ref<FormInstance>()
const pw = reactive({ current: '', next: '', confirm: '' })
const pwSaving = ref(false)
const pwRules: FormRules = {
  current: [{ required: true, message: '請輸入目前密碼', trigger: 'blur' }],
  next: [
    { required: true, message: '請輸入新密碼', trigger: 'blur' },
    { min: 8, message: '至少 8 個字元', trigger: 'blur' },
  ],
  confirm: [
    { required: true, message: '請再輸入一次新密碼', trigger: 'blur' },
    {
      validator: (_r, v: string, cb) => (v === pw.next ? cb() : cb(new Error('兩次輸入不一致'))),
      trigger: 'blur',
    },
  ],
}

async function savePassword() {
  if (!(await pwForm.value?.validate().catch(() => false))) return
  pwSaving.value = true
  try {
    await settingsApi.changePassword(pw.current, pw.next)
    ElMessage.success('密碼已更新')
    pwForm.value?.resetFields()
  } finally {
    pwSaving.value = false
  }
}
</script>

<template>
  <div class="settings">
    <h1 class="lc-page-title">設定</h1>
    <p class="lc-page-sub">{{ subtitle }}</p>

    <!-- IG 帳號 -->
    <section v-if="canIg" v-loading="!info" class="lc-card block">
      <h2>IG 帳號</h2>
      <IgAccountsPanel v-if="info" :info="info" @reload="load" />
    </section>

    <!-- 串接資訊 -->
    <section v-if="canIg && integ && info" class="lc-card block">
      <h2>串接資訊</h2>
      <p class="hint">
        設定 Meta 開發者後台時要填的值。使用 Instagram API with Unified Login（官方建議新整合採用的設定）：在 App 的
        Facebook 登入建立「配置」，勾選下列權限，把配置 ID 填進下方「憑證」。
      </p>
      <el-alert
        v-if="insecurePublic && !integ.simulator"
        type="warning"
        :closable="false"
        show-icon
        title="公開網址不是 HTTPS：IG 收不到 webhook、也抓不到要發佈的圖片"
        class="alert"
      />
      <dl class="facts copyable">
        <div>
          <dt>Webhook 網址</dt>
          <dd>
            <code>{{ integ.webhookUrl }}</code>
            <el-button link :icon="CopyDocument" aria-label="複製 Webhook 網址" @click="copy(integ.webhookUrl)" />
          </dd>
        </div>
        <div>
          <dt>OAuth 重新導向網址</dt>
          <dd>
            <code>{{ integ.redirectUri }}</code>
            <el-button link :icon="CopyDocument" aria-label="複製重新導向網址" @click="copy(integ.redirectUri)" />
          </dd>
        </div>
        <div>
          <dt>Webhook 驗證碼</dt>
          <dd class="muted">在下方「憑證」的 Meta App 卡片設定，Meta 後台填同一個值</dd>
        </div>
        <div>
          <dt>Webhook 欄位</dt>
          <dd class="wrap"><code>messages</code><code>messaging_seen</code><code>message_reactions</code></dd>
        </div>
        <div>
          <dt>配置要勾的權限</dt>
          <dd class="wrap">
            <code v-for="p in integ.permissions" :key="p">{{ p }}</code>
          </dd>
        </div>
        <div>
          <dt>Graph API</dt>
          <dd><code>{{ integ.apiBase || '—' }}</code></dd>
        </div>
        <div>
          <dt>店家時區</dt>
          <dd>{{ info.timezone }}</dd>
        </div>
      </dl>
    </section>

    <!-- 憑證 -->
    <section v-if="auth.can('credentials')" class="lc-card block">
      <h2>憑證</h2>
      <CredentialsPanel @saved="onCredsSaved" />
    </section>

    <!-- 管理員 -->
    <section v-if="auth.can('admins')" class="lc-card block">
      <h2>管理員</h2>
      <AdminUsersPanel />
    </section>

    <!-- 外觀 -->
    <section class="lc-card block">
      <h2>外觀</h2>
      <el-segmented
        :model-value="theme.mode"
        :options="themeOptions"
        aria-label="外觀模式"
        @update:model-value="(v: string | number | boolean) => theme.setMode(v as ThemeMode)"
      />
      <p class="muted theme-hint">「跟隨系統」會隨裝置的淺色／深色設定自動切換；選擇只記在這個瀏覽器。</p>
    </section>

    <!-- 管理員密碼 -->
    <section class="lc-card block">
      <h2>管理員密碼</h2>
      <el-form ref="pwForm" :model="pw" :rules="pwRules" label-position="top" class="pw" @submit.prevent="savePassword">
        <el-form-item label="目前密碼" prop="current">
          <el-input v-model="pw.current" type="password" show-password autocomplete="current-password" />
        </el-form-item>
        <el-form-item label="新密碼" prop="next">
          <el-input v-model="pw.next" type="password" show-password autocomplete="new-password" />
        </el-form-item>
        <el-form-item label="再輸入一次" prop="confirm">
          <el-input v-model="pw.confirm" type="password" show-password autocomplete="new-password" />
        </el-form-item>
        <el-button type="primary" native-type="submit" :loading="pwSaving">更新密碼</el-button>
      </el-form>
    </section>
  </div>
</template>

<style scoped lang="scss">
.settings {
  max-width: 760px;
}
.block {
  padding: 20px;
  margin-bottom: 16px;
  min-height: 80px;
  h2 {
    margin: 0 0 14px;
    font-size: 16px;
    font-weight: 600;
  }
}
.hint {
  margin: -8px 0 12px;
  font-size: 13px;
  color: var(--lc-muted);
}
.alert {
  margin-bottom: 14px;
}
.facts {
  margin: 0 0 16px;
  > div {
    display: grid;
    grid-template-columns: 140px 1fr;
    gap: 12px;
    padding: 10px 0;
    border-top: 1px solid var(--lc-border);
    &:first-child {
      border-top: 0;
    }
  }
  dt {
    color: var(--lc-muted);
    font-size: 13px;
    padding-top: 2px;
  }
  dd {
    margin: 0;
    font-size: 14px;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    min-width: 0;
  }
  code {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 12.5px;
    background: var(--lc-cream);
    padding: 2px 6px;
    border-radius: 6px;
    word-break: break-all;
  }
}
.copyable dd {
  flex-wrap: nowrap;
  align-items: flex-start;
  code {
    flex: 0 1 auto;
    min-width: 0;
  }
  .el-button {
    flex: none;
  }
}
.copyable dd.wrap {
  flex-wrap: wrap;
}
.theme-hint {
  margin: 10px 0 0;
}
.missing,
.muted {
  color: var(--lc-muted);
  font-size: 13px;
}
.pw {
  max-width: 360px;
}
@media (max-width: 768px) {
  .block {
    padding: 16px;
  }
  .facts > div {
    grid-template-columns: 1fr;
    gap: 4px;
  }
}
</style>
