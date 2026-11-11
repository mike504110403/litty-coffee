<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { CopyDocument } from '@element-plus/icons-vue'
import { settingsApi, type IgAccount, type SettingsInfo } from '@/api/settings'
import { useThemeStore, type ThemeMode } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import AdminUsersPanel from '@/views/settings/AdminUsersPanel.vue'
import CredentialsPanel from '@/views/settings/CredentialsPanel.vue'

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
const busy = ref<'' | 'refresh' | 'subscribe' | 'disconnect'>('')
const avatarFailed = ref(false)

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
    if (ok) ElMessage.success('IG 帳號已連結')
    else ElMessage({ type: 'error', message: err, duration: 8000, showClose: true })
  }
  if (canIg.value) await load()
})

const account = computed(() => info.value?.account ?? null)
const integ = computed(() => info.value?.integration)

const DAY = 24 * 3600 * 1000
const daysLeft = computed(() => {
  const exp = account.value?.tokenExpires
  return exp ? Math.floor((new Date(exp).getTime() - Date.now()) / DAY) : null
})
const tokenState = computed<{ type: 'success' | 'warning' | 'danger'; text: string }>(() => {
  const d = daysLeft.value
  if (d == null) return { type: 'warning', text: '未知' }
  if (d < 0) return { type: 'danger', text: '已過期' }
  if (d < 7) return { type: 'danger', text: `剩 ${d} 天` }
  if (d < 20) return { type: 'warning', text: `剩 ${d} 天` }
  return { type: 'success', text: `剩 ${d} 天` }
})

const fmtDate = (iso: string | null) =>
  iso ? new Date(iso).toLocaleDateString('zh-TW', { year: 'numeric', month: 'long', day: 'numeric' }) : '—'

const accountTypeLabel: Record<string, string> = { BUSINESS: '商家帳號', MEDIA_CREATOR: '創作者帳號' }

function connect() {
  window.location.href = settingsApi.connectUrl
}

async function run(kind: 'refresh' | 'subscribe', fn: () => Promise<IgAccount>, okMsg: string) {
  busy.value = kind
  try {
    const a = await fn()
    if (info.value) info.value.account = a
    ElMessage.success(okMsg)
  } catch {
    await load() // 失敗原因後端寫在 lastError
  } finally {
    busy.value = ''
  }
}

async function disconnect() {
  try {
    await ElMessageBox.confirm(
      '斷開後收不到新私訊、自動回覆和排程發文都會停止。已收到的對話與顧客資料會保留，之後重新連結即可恢復。',
      `斷開 @${account.value?.username}？`,
      { confirmButtonText: '斷開連結', cancelButtonText: '取消', type: 'warning', confirmButtonClass: 'el-button--danger' },
    )
  } catch {
    return
  }
  busy.value = 'disconnect'
  try {
    await settingsApi.disconnect()
    ElMessage.success('已斷開')
    await load()
  } finally {
    busy.value = ''
  }
}

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
      <template v-if="info">
        <div v-if="account" class="account">
          <div class="who">
            <img
              v-if="account.profilePic && !avatarFailed"
              :src="account.profilePic"
              alt=""
              class="pic"
              @error="avatarFailed = true"
            />
            <div v-else class="pic fallback">{{ (account.username || '?').slice(0, 1).toUpperCase() }}</div>
            <div class="names">
              <div class="name">{{ account.name || account.username }}</div>
              <div class="handle">
                @{{ account.username }}
                <el-tag size="small" type="info" effect="plain" round>
                  {{ accountTypeLabel[account.accountType] ?? account.accountType }}
                </el-tag>
              </div>
            </div>
          </div>

          <el-alert v-if="account.lastError" type="error" :closable="false" show-icon :title="account.lastError" class="alert" />
          <el-alert
            v-if="daysLeft != null && daysLeft < 0"
            type="error"
            :closable="false"
            show-icon
            title="授權已過期：收發私訊和發文都會失敗，請重新連結"
            class="alert"
          />

          <dl class="facts">
            <div>
              <dt>授權到期</dt>
              <dd>
                {{ fmtDate(account.tokenExpires) }}
                <el-tag size="small" :type="tokenState.type" round>{{ tokenState.text }}</el-tag>
              </dd>
            </div>
            <div>
              <dt>自動續期</dt>
              <dd>剩 20 天內自動續期 60 天；上次 {{ fmtDate(account.tokenRefreshedAt) }}</dd>
            </div>
            <div>
              <dt>私訊通知</dt>
              <dd>
                <el-tag size="small" :type="account.webhookSubscribed ? 'success' : 'danger'" round>
                  {{ account.webhookSubscribed ? '已訂閱' : '未訂閱' }}
                </el-tag>
                <el-button
                  v-if="!account.webhookSubscribed"
                  link
                  type="primary"
                  :loading="busy === 'subscribe'"
                  @click="run('subscribe', settingsApi.resubscribe, '已訂閱私訊通知')"
                >
                  重試訂閱
                </el-button>
              </dd>
            </div>
            <div>
              <dt>連結時間</dt>
              <dd>{{ fmtDate(account.connectedAt) }}</dd>
            </div>
          </dl>

          <div class="actions">
            <el-button :loading="busy === 'refresh'" @click="run('refresh', settingsApi.refresh, '授權已續期')">
              立即續期
            </el-button>
            <el-button @click="connect">重新連結</el-button>
            <el-button type="danger" plain :loading="busy === 'disconnect'" @click="disconnect">斷開連結</el-button>
          </div>
        </div>

        <div v-else class="empty">
          <p>
            連結店家的 IG 商家帳號後，後台才能收發私訊、自動回覆和排程發文。按下按鈕會轉到 IG 登入並授權，完成後自動回到這裡。
          </p>
          <ul>
            <li>帳號必須是「商家」或「創作者」帳號（IG App →設定→帳號類型）</li>
            <li>會請求的權限：基本資料、管理私訊、發佈內容</li>
          </ul>
          <el-alert
            v-if="!integ?.oauthReady"
            type="warning"
            :closable="false"
            show-icon
            title="後端尚未設定 IG_APP_ID／IG_APP_SECRET"
            description="到 Meta 開發者後台建立 App、啟用 Instagram 登入後，把兩個值填進後端 .env 並重啟。"
            class="alert"
          />
          <el-alert
            v-else-if="integ?.simulator"
            type="info"
            :closable="false"
            show-icon
            title="模擬模式：不會真的跳到 IG，會直接連上模擬器的假帳號"
            class="alert"
          />
          <el-alert
            v-if="integ?.envTokenSet"
            type="info"
            :closable="false"
            show-icon
            title="目前發文暫用後端 .env 的 IG_ACCESS_TOKEN；連結帳號後改用連結的授權"
            class="alert"
          />
          <el-button type="primary" size="large" :disabled="!integ?.oauthReady" @click="connect">連結 IG 帳號</el-button>
        </div>
      </template>
    </section>

    <!-- 串接資訊 -->
    <section v-if="canIg && integ && info" class="lc-card block">
      <h2>串接資訊</h2>
      <p class="hint">設定 Meta 開發者後台時要填的值。</p>
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
.who {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 16px;
}
.pic {
  width: 56px;
  height: 56px;
  border-radius: 50%;
  object-fit: cover;
  flex: none;
  &.fallback {
    display: grid;
    place-items: center;
    background: var(--lc-caramel);
    color: var(--lc-on-fill);
    font-size: 22px;
    font-weight: 600;
  }
}
.name {
  font-weight: 600;
  font-size: 16px;
}
.handle {
  color: var(--lc-muted);
  font-size: 14px;
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 2px;
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
.theme-hint {
  margin: 10px 0 0;
}
.missing,
.muted {
  color: var(--lc-muted);
  font-size: 13px;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  .el-button + .el-button {
    margin-left: 0;
  }
}
.empty {
  p {
    margin: 0 0 8px;
    line-height: 1.6;
    font-size: 14px;
  }
  ul {
    margin: 0 0 16px;
    padding-left: 20px;
    color: var(--lc-muted);
    font-size: 13px;
    line-height: 1.8;
  }
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
