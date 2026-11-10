<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { useAuthStore } from '@/stores/auth'

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

async function submit() {
  if (!(await formRef.value?.validate().catch(() => false))) return
  loading.value = true
  try {
    await auth.login(form.username, form.password)
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    router.replace(redirect)
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
    radial-gradient(circle at 20% 20%, rgba(200, 145, 90, 0.35), transparent 50%),
    radial-gradient(circle at 80% 90%, rgba(107, 74, 53, 0.6), transparent 55%),
    var(--lc-espresso);
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
    color: #fff;
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
