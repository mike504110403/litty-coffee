<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { CopyDocument } from '@element-plus/icons-vue'
import { totpApi, type TotpSetup } from '@/api/totp'
import HelpTip from './HelpTip.vue'

// 綁定 Google Authenticator：掃 QR code（或手動輸入金鑰）→ 輸入 6 位數確認 → emit done(備援碼)
const emit = defineEmits<{ done: [codes: string[]] }>()

const setup = ref<TotpSetup | null>(null)
const loading = ref(true)
const code = ref('')
const submitting = ref(false)

async function load() {
  loading.value = true
  try {
    setup.value = await totpApi.setup()
  } catch {
    // 攔截器已顯示
  } finally {
    loading.value = false
  }
}
onMounted(load)

function onInput(v: string) {
  code.value = v.replace(/\D/g, '').slice(0, 6)
}

async function confirm() {
  if (code.value.length !== 6) return
  submitting.value = true
  try {
    emit('done', await totpApi.enable(code.value))
  } catch {
    code.value = ''
  } finally {
    submitting.value = false
  }
}

async function copySecret() {
  if (!setup.value) return
  try {
    await navigator.clipboard.writeText(setup.value.secret)
    ElMessage.success('已複製')
  } catch {
    ElMessage.warning('無法自動複製，請手動選取')
  }
}

const grouped = (s: string) => s.replace(/(.{4})/g, '$1 ').trim()
</script>

<template>
  <div v-loading="loading" class="bind">
    <template v-if="setup">
      <div class="scan">
        <!-- QR code 一律白底黑點（深色模式也一樣），掃描器才讀得到 -->
        <img :src="setup.qrCode" alt="Google Authenticator QR code" class="qr" width="180" height="180" />
        <div class="side">
          <p class="lead">
            用 Google Authenticator 掃描
            <HelpTip text="手機打開 Google Authenticator（或其他驗證器 App）→「＋」→「掃描 QR 圖碼」。無法掃描時改選「輸入設定金鑰」，帳戶名稱任填、金鑰貼下方這串。" />
          </p>
          <div class="secret-label">無法掃描時手動輸入金鑰</div>
          <button type="button" class="secret" @click="copySecret">
            <code>{{ grouped(setup.secret) }}</code>
            <el-icon><CopyDocument /></el-icon>
          </button>
        </div>
      </div>
      <form class="confirm" @submit.prevent="confirm">
        <label for="lc-totp-bind-code">輸入 App 上的 6 位數驗證碼</label>
        <div class="confirm-row">
          <el-input
            id="lc-totp-bind-code"
            :model-value="code"
            class="code-input"
            size="large"
            inputmode="numeric"
            autocomplete="one-time-code"
            placeholder="000000"
            maxlength="6"
            @update:model-value="onInput"
          />
          <el-button type="primary" size="large" native-type="submit" :loading="submitting" :disabled="code.length !== 6">
            確認綁定
          </el-button>
        </div>
      </form>
    </template>
    <el-empty v-else-if="!loading" description="無法產生 QR code">
      <el-button @click="load">重試</el-button>
    </el-empty>
  </div>
</template>

<style scoped lang="scss">
.bind {
  min-height: 200px;
}
.scan {
  display: flex;
  gap: 20px;
  align-items: center;
}
.qr {
  flex: none;
  width: 180px;
  height: 180px;
  padding: 8px;
  border-radius: 12px;
  background: #fff;
  border: 1px solid var(--lc-border);
  image-rendering: pixelated;
}
.side {
  min-width: 0;
}
.lead {
  display: flex;
  align-items: center;
  margin: 0 0 14px;
  font-size: 15px;
  font-weight: 600;
  color: var(--lc-text);
}
.secret-label {
  font-size: 12.5px;
  color: var(--lc-muted);
  margin-bottom: 6px;
}
.secret {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 10px;
  border: 1px dashed var(--lc-border);
  border-radius: 10px;
  background: var(--lc-bg-soft);
  color: var(--lc-text);
  font: inherit;
  cursor: pointer;
  text-align: left;
  code {
    flex: 1;
    min-width: 0;
    font-size: 13px;
    letter-spacing: 0.04em;
    word-break: break-all;
  }
  .el-icon {
    flex: none;
    color: var(--lc-muted);
  }
  &:hover {
    background: var(--lc-bg-hover);
  }
}
.confirm {
  margin-top: 20px;
  label {
    display: block;
    font-size: 14px;
    margin-bottom: 8px;
    color: var(--lc-text);
  }
}
.confirm-row {
  display: flex;
  gap: 10px;
}
.code-input {
  flex: 1;
  :deep(input) {
    font-size: 20px;
    letter-spacing: 0.4em;
    text-align: center;
    font-variant-numeric: tabular-nums;
  }
}
@media (max-width: 520px) {
  .scan {
    flex-direction: column;
    align-items: stretch;
  }
  .qr {
    align-self: center;
  }
  .confirm-row {
    flex-direction: column;
  }
}
</style>
