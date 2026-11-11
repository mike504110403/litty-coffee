<script setup lang="ts">
import { useIsMobile } from '@/composables/useIsMobile'
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '@/stores/auth'
import TotpBind from './TotpBind.vue'
import RecoveryCodes from './RecoveryCodes.vue'

const isMobile = useIsMobile()

// 頭像選單「綁定 Google Authenticator」（一般管理員自選；超管在 /setup 強制綁）
const visible = defineModel<boolean>({ required: true })
const auth = useAuthStore()

const codes = ref<string[]>([])

// 每次打開重新產生 QR code
watch(visible, (v) => {
  if (v) codes.value = []
})

function onBound(c: string[]) {
  codes.value = c
}

async function done() {
  visible.value = false
  await auth.fetchMe()
  ElMessage.success('已綁定，之後登入要輸入驗證碼')
}
</script>

<template>
  <el-dialog class="lc-fs-dialog" :fullscreen="isMobile"
    v-model="visible"
    :title="codes.length ? '保存備援碼' : '綁定 Google Authenticator'"
    width="min(520px, 92vw)"
    append-to-body
    destroy-on-close
    :close-on-click-modal="false"
    :show-close="!codes.length"
  >
    <RecoveryCodes v-if="codes.length" :codes="codes" :username="auth.user?.username ?? ''" @confirm="done" />
    <TotpBind v-else @done="onBound" />
  </el-dialog>
</template>
