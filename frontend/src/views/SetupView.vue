<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { settingsApi } from '@/api/settings'
import { useAuthStore } from '@/stores/auth'
import TotpBind from '@/components/TotpBind.vue'
import RecoveryCodes from '@/components/RecoveryCodes.vue'
import HelpTip from '@/components/HelpTip.vue'

// 首次登入鎖定：必須改密碼（例如預設超管）、超管必須綁 GA；完成前其他 API 一律 403 setup_required
const auth = useAuthStore()
const router = useRouter()

type Step = 'password' | 'totp' | 'codes'
const u = auth.user
const steps: Step[] = [
  ...(u?.mustChangePassword ? (['password'] as Step[]) : []),
  ...(u?.isSuper && !u.totpEnabled ? (['totp', 'codes'] as Step[]) : []),
]
const idx = ref(0)
const step = computed<Step | undefined>(() => steps[idx.value])
const labels: Record<Step, string> = { password: '修改密碼', totp: '綁定 GA', codes: '備援碼' }

// ---- 步驟 1：修改密碼 ----
const formRef = ref<FormInstance>()
const pw = reactive({ current: '', next: '', confirm: '' })
const saving = ref(false)
const rules: FormRules = {
  current: [{ required: true, message: '請輸入目前密碼', trigger: 'blur' }],
  next: [
    { required: true, message: '請輸入新密碼', trigger: 'blur' },
    { min: 8, message: '至少 8 個字元', trigger: 'blur' },
    { validator: (_r, v: string, cb) => (v && v === pw.current ? cb(new Error('不能與目前密碼相同')) : cb()), trigger: 'blur' },
  ],
  confirm: [
    { required: true, message: '請再輸入一次新密碼', trigger: 'blur' },
    { validator: (_r, v: string, cb) => (v === pw.next ? cb() : cb(new Error('兩次輸入不一致'))), trigger: 'blur' },
  ],
}

async function savePassword() {
  if (!(await formRef.value?.validate().catch(() => false))) return
  saving.value = true
  try {
    await settingsApi.changePassword(pw.current, pw.next)
    ElMessage.success('密碼已更新')
    await next()
  } catch {
    // 攔截器已顯示
  } finally {
    saving.value = false
  }
}

// ---- 步驟 2、3：綁 GA、備援碼 ----
const codes = ref<string[]>([])
function onBound(c: string[]) {
  codes.value = c
  idx.value++
}

async function next() {
  if (idx.value < steps.length - 1) {
    idx.value++
    return
  }
  await finish()
}

async function finish() {
  const me = await auth.fetchMe()
  router.replace(me && !me.setupRequired ? '/' : { name: 'login' })
}

async function logout() {
  await auth.logout()
  router.replace({ name: 'login' })
}
</script>

<template>
  <div class="setup">
    <div class="card lc-card">
      <header class="top">
        <img src="/favicon.svg" alt="" class="logo" />
        <div class="titles">
          <h1>
            完成帳號設定
            <HelpTip text="為了帳號安全，第一次登入要先改掉預設密碼；超級管理員還要綁定 Google Authenticator，之後每次登入都要輸入驗證碼。完成前無法使用後台其他功能。" />
          </h1>
          <p class="who">{{ u?.displayName || u?.username }}</p>
        </div>
        <el-button text class="logout" @click="logout">登出</el-button>
      </header>

      <el-steps v-if="steps.length > 1" :active="idx" finish-status="success" align-center class="steps">
        <el-step v-for="s in steps" :key="s" :title="labels[s]" />
      </el-steps>

      <section v-if="step === 'password'" class="body">
        <h2>修改密碼</h2>
        <el-form ref="formRef" :model="pw" :rules="rules" label-position="top" size="large" @submit.prevent="savePassword">
          <el-form-item label="目前密碼" prop="current">
            <el-input v-model="pw.current" type="password" show-password autocomplete="current-password" />
          </el-form-item>
          <el-form-item label="新密碼" prop="next">
            <el-input v-model="pw.next" type="password" show-password autocomplete="new-password" placeholder="至少 8 個字元" />
          </el-form-item>
          <el-form-item label="再輸入一次" prop="confirm">
            <el-input v-model="pw.confirm" type="password" show-password autocomplete="new-password" />
          </el-form-item>
          <el-button type="primary" native-type="submit" :loading="saving" class="wide">
            {{ idx < steps.length - 1 ? '下一步' : '完成' }}
          </el-button>
        </el-form>
      </section>

      <section v-else-if="step === 'totp'" class="body">
        <h2>綁定 Google Authenticator</h2>
        <TotpBind @done="onBound" />
      </section>

      <section v-else-if="step === 'codes'" class="body">
        <h2>保存備援碼</h2>
        <RecoveryCodes :codes="codes" :username="u?.username ?? ''" confirm-text="進入後台" @confirm="finish" />
      </section>

      <section v-else class="body">
        <el-button type="primary" class="wide" @click="finish">進入後台</el-button>
      </section>
    </div>
  </div>
</template>

<style scoped lang="scss">
.setup {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 32px 16px;
  background:
    radial-gradient(circle at 15% 10%, var(--lc-hero-glow-1), transparent 45%),
    var(--lc-cream);
}
.card {
  width: 100%;
  max-width: 560px;
  padding: 28px 32px 32px;
}
.top {
  display: flex;
  align-items: center;
  gap: 12px;
  .logo {
    width: 40px;
    height: 40px;
    border-radius: 12px;
    flex: none;
  }
  .titles {
    flex: 1;
    min-width: 0;
  }
  h1 {
    display: flex;
    align-items: center;
    margin: 0;
    font-size: 20px;
  }
  .who {
    margin: 2px 0 0;
    font-size: 13px;
    color: var(--lc-muted);
  }
  .logout {
    flex: none;
  }
}
.steps {
  margin: 24px 0 8px;
}
.body {
  margin-top: 20px;
  h2 {
    margin: 0 0 16px;
    font-size: 17px;
  }
}
.wide {
  width: 100%;
}
@media (max-width: 520px) {
  .setup {
    place-items: start center;
    padding: 16px 12px;
  }
  .card {
    padding: 20px 18px 24px;
  }
  .steps :deep(.el-step__title) {
    font-size: 13px;
  }
}
</style>
