<script setup lang="ts">
import { nextTick, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { isAxiosError } from 'axios'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { ArrowLeft } from '@element-plus/icons-vue'
import { useAuthStore, type AdminUser } from '@/stores/auth'
import HelpTip from '@/components/HelpTip.vue'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const formRef = ref<FormInstance>()
const form = reactive({ username: '', password: '' })
const rules: FormRules = {
  username: [{ required: true, message: '請輸入帳號', trigger: 'blur' }],
  password: [{ required: true, message: '請輸入密碼', trigger: 'blur' }],
}
const loading = ref(false)

// 兩步驟：密碼正確且已綁 GA → 輸入 6 位數驗證碼（或備援碼）
const stage = ref<'password' | 'code'>('password')
const useRecovery = ref(false)
const code = ref('')
const recovery = ref('')
const codeInput = ref<{ focus: () => void }>()

function enter(u: AdminUser) {
  if (u.setupRequired) {
    router.replace({ name: 'setup' })
    return
  }
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
  router.replace(redirect)
}

async function toCode() {
  stage.value = 'code'
  useRecovery.value = false
  code.value = ''
  recovery.value = ''
  await nextTick()
  codeInput.value?.focus()
}

function backToPassword() {
  stage.value = 'password'
  form.password = ''
}

async function toggleRecovery() {
  useRecovery.value = !useRecovery.value
  await nextTick()
  codeInput.value?.focus()
}

function onCode(v: string) {
  code.value = v.replace(/\D/g, '').slice(0, 6)
  if (code.value.length === 6) void verify()
}

async function verify() {
  const input = useRecovery.value ? { recoveryCode: recovery.value.trim() } : { code: code.value }
  if (useRecovery.value ? !input.recoveryCode : code.value.length !== 6) return
  if (loading.value) return
  loading.value = true
  try {
    enter(await auth.verifyCode(input))
  } catch (e) {
    code.value = ''
    // 400／429 的訊息由 http 攔截器顯示；401＝待驗證逾時，回到密碼步驟
    if (isAxiosError(e) && e.response?.status === 401) {
      ElMessage.warning('驗證已逾時，請重新登入')
      backToPassword()
    }
  } finally {
    loading.value = false
  }
}

async function submit() {
  if (!(await formRef.value?.validate().catch(() => false))) return
  loading.value = true
  try {
    const res = await auth.login(form.username, form.password)
    if (res.mfaRequired) await toCode()
    else enter(res.user)
  } catch {
    // 錯誤訊息由 http 攔截器顯示；401 在登入頁另外提示
    ElMessage.error('帳號或密碼錯誤')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login">
    <div class="hero">
      <div class="hero-inner">
        <img src="/favicon.svg" alt="" class="logo" />
        <h1 class="lc-brand-font">Litty Coffee</h1>
        <p>每一則訊息，都是一杯好咖啡的開始。</p>
      </div>
    </div>
    <div class="panel">
      <div class="card lc-card">
        <template v-if="stage === 'code'">
          <button type="button" class="back" @click="backToPassword">
            <el-icon><ArrowLeft /></el-icon>返回
          </button>
          <h2>
            兩步驟驗證
            <HelpTip text="打開手機上的 Google Authenticator，輸入「Litty Coffee」這一項目前顯示的 6 位數。手機不在身邊時改用備援碼。" />
          </h2>
          <p class="sub">{{ useRecovery ? '輸入一組備援碼' : '輸入 Google Authenticator 上的 6 位數驗證碼' }}</p>
          <form @submit.prevent="verify">
            <el-input
              v-if="!useRecovery"
              ref="codeInput"
              :model-value="code"
              class="code-input"
              size="large"
              inputmode="numeric"
              autocomplete="one-time-code"
              placeholder="000000"
              maxlength="6"
              aria-label="6 位數驗證碼"
              @update:model-value="onCode"
            />
            <el-input
              v-else
              ref="codeInput"
              v-model="recovery"
              class="recovery-input"
              size="large"
              autocomplete="off"
              placeholder="xxxxx-xxxxx"
              aria-label="備援碼"
            />
            <el-button
              type="primary"
              size="large"
              native-type="submit"
              :loading="loading"
              :disabled="useRecovery ? !recovery.trim() : code.length !== 6"
              class="submit"
            >
              驗證
            </el-button>
          </form>
          <el-button text type="primary" class="alt" @click="toggleRecovery">
            {{ useRecovery ? '改用驗證碼' : '使用備援碼' }}
          </el-button>
        </template>
        <template v-else>
          <h2>登入後台</h2>
          <p class="sub">管理 IG 訊息、自動回覆與排程</p>
          <el-form ref="formRef" :model="form" :rules="rules" size="large" @submit.prevent="submit">
            <el-form-item prop="username">
              <el-input v-model="form.username" placeholder="帳號" autocomplete="username" />
            </el-form-item>
            <el-form-item prop="password">
              <el-input
                v-model="form.password"
                type="password"
                placeholder="密碼"
                show-password
                autocomplete="current-password"
              />
            </el-form-item>
            <el-button type="primary" native-type="submit" :loading="loading" class="submit">登入</el-button>
          </el-form>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.login {
  min-height: 100vh;
  display: grid;
  grid-template-columns: 1.1fr 1fr;
}
.hero {
  display: grid;
  place-items: center;
  padding: 40px;
  color: var(--lc-latte);
  background:
    radial-gradient(circle at 20% 20%, var(--lc-hero-glow-1), transparent 50%),
    radial-gradient(circle at 80% 90%, var(--lc-hero-glow-2), transparent 55%),
    var(--lc-hero-base);
}
.hero-inner {
  max-width: 360px;
  .logo {
    width: 64px;
    height: 64px;
    border-radius: 18px;
  }
  h1 {
    margin: 20px 0 8px;
    font-size: 44px;
    color: var(--lc-on-fill);
  }
  p {
    margin: 0;
    font-size: 16px;
    opacity: 0.75;
    line-height: 1.6;
  }
}
.panel {
  display: grid;
  place-items: center;
  padding: 24px;
}
.card {
  width: 100%;
  max-width: 380px;
  padding: 36px 32px 28px;
  h2 {
    margin: 0;
    font-size: 22px;
  }
  .sub {
    margin: 6px 0 24px;
    color: var(--lc-muted);
    font-size: 14px;
  }
}
.submit {
  width: 100%;
  margin-top: 4px;
}
.card h2 {
  display: flex;
  align-items: center;
}
.back {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin: -8px 0 12px -4px;
  padding: 4px;
  border: 0;
  background: transparent;
  color: var(--lc-muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
  &:hover {
    color: var(--lc-text);
  }
}
.code-input,
.recovery-input {
  margin-bottom: 14px;
}
.code-input :deep(input) {
  font-size: 22px;
  letter-spacing: 0.5em;
  text-align: center;
  font-variant-numeric: tabular-nums;
}
.recovery-input :deep(input) {
  font-size: 17px;
  letter-spacing: 0.08em;
  text-align: center;
}
.alt {
  display: flex;
  margin: 10px auto 0;
}

@media (max-width: 768px) {
  .login {
    grid-template-columns: 1fr;
    grid-template-rows: auto 1fr;
  }
  .hero {
    padding: 48px 24px 72px;
    text-align: center;
    .hero-inner h1 {
      font-size: 34px;
    }
  }
  .panel {
    align-items: start;
    margin-top: -48px;
  }
}
</style>
