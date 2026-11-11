<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { ArrowDown, Flag } from '@element-plus/icons-vue'
import { flagsApi, type RiskResult } from '@/api/flags'
import { riskLabels, type Risk } from '@/api/inbox'

// 「標記」下拉：加入黑名單／標記問題顧客／解除，選了跳出原因輸入框再送出。收件匣與顧客頁共用。
const props = defineProps<{ contactId: number; risk: Risk; size?: 'small' | 'default' }>()
const emit = defineEmits<{ changed: [r: RiskResult] }>()

const target = ref<Risk | null>(null)
const reason = ref('')
const saving = ref(false)
const open = computed({
  get: () => target.value !== null,
  set: (v) => {
    if (!v) target.value = null
  },
})

const titles: Record<Risk, string> = { blocked: '加入黑名單', watch: '標記問題顧客', '': '解除標記' }
const hints: Record<Risk, string> = {
  blocked: '訊息仍會進收件匣，但 AI 不會回覆、也不會發轉真人通知；你仍可手動回覆。',
  watch: '只是提醒同事注意，AI 照常回覆。',
  '': '恢復為一般顧客，AI 照常回覆。會記錄是誰解除的。',
}

function pick(cmd: Risk) {
  target.value = cmd
  reason.value = ''
}

async function submit() {
  if (target.value === null) return
  saving.value = true
  try {
    const r = await flagsApi.setRisk(props.contactId, target.value, reason.value.trim())
    emit('changed', r)
    ElMessage.success(target.value ? `已${titles[target.value]}` : '已解除標記')
    target.value = null
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-dropdown trigger="click" @command="pick">
    <el-button :size="size" round :icon="Flag">
      <span class="label">標記</span><el-icon class="el-icon--right"><ArrowDown /></el-icon>
    </el-button>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item command="blocked" :disabled="risk === 'blocked'">加入黑名單</el-dropdown-item>
        <el-dropdown-item command="watch" :disabled="risk === 'watch'">標記問題顧客</el-dropdown-item>
        <el-dropdown-item command="" :disabled="!risk" divided>解除{{ risk ? riskLabels[risk] : '' }}</el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>

  <el-dialog v-model="open" :title="target !== null ? titles[target] : ''" width="min(420px, 92vw)" append-to-body align-center>
    <p class="hint">{{ target !== null ? hints[target] : '' }}</p>
    <el-input
      v-model="reason"
      type="textarea"
      :autosize="{ minRows: 2, maxRows: 4 }"
      maxlength="200"
      show-word-limit
      :placeholder="target ? '原因（選填），例如：一直傳廣告連結' : '解除原因（選填）'"
    />
    <template #footer>
      <el-button @click="open = false">取消</el-button>
      <el-button :type="target === 'blocked' ? 'danger' : 'primary'" :loading="saving" @click="submit">確定</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
@media (max-width: 768px) {
  .label {
    display: none;
  }
}
.hint {
  margin: 0 0 12px;
  color: var(--lc-muted);
  font-size: 13px;
  line-height: 1.6;
}
</style>
