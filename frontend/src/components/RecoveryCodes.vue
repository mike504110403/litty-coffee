<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { CopyDocument, Download } from '@element-plus/icons-vue'
import HelpTip from './HelpTip.vue'

// 備援碼（只顯示這一次）：複製／下載，勾選「已妥善保存」後才能繼續
const props = defineProps<{ codes: string[]; username: string; confirmText?: string }>()
const emit = defineEmits<{ confirm: [] }>()

const saved = ref(false)

function text() {
  return `Litty Coffee 後台備援碼（${props.username}）\n每組只能用一次\n\n${props.codes.join('\n')}\n`
}

async function copy() {
  try {
    await navigator.clipboard.writeText(text())
    ElMessage.success('已複製')
  } catch {
    ElMessage.warning('無法自動複製，請手動選取')
  }
}

function download() {
  const url = URL.createObjectURL(new Blob([text()], { type: 'text/plain;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `litty-coffee-recovery-${props.username}.txt`
  a.click()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div class="recovery">
    <p class="lead">
      只會顯示這一次，請立即保存
      <HelpTip text="手機遺失或換機時，在登入的驗證碼步驟選「使用備援碼」輸入其中一組。每組只能用一次；都用完或遺失請找超級管理員重設 GA。" />
    </p>
    <ol class="codes">
      <li v-for="c in codes" :key="c"><code>{{ c }}</code></li>
    </ol>
    <div class="actions">
      <el-button :icon="CopyDocument" @click="copy">複製</el-button>
      <el-button :icon="Download" @click="download">下載</el-button>
    </div>
    <el-checkbox v-model="saved" class="saved">我已妥善保存備援碼</el-checkbox>
    <el-button type="primary" size="large" class="go" :disabled="!saved" @click="emit('confirm')">
      {{ confirmText ?? '完成' }}
    </el-button>
  </div>
</template>

<style scoped lang="scss">
.lead {
  display: flex;
  align-items: center;
  margin: 0 0 12px;
  font-size: 15px;
  font-weight: 600;
  color: var(--lc-text);
}
.codes {
  list-style: none;
  margin: 0;
  padding: 14px 16px;
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 8px 16px;
  border-radius: 12px;
  background: var(--lc-bg-soft);
  border: 1px solid var(--lc-border);
  li {
    font-size: 15px;
    color: var(--lc-text);
  }
  code {
    font-variant-numeric: tabular-nums;
    letter-spacing: 0.06em;
  }
}
.actions {
  display: flex;
  gap: 8px;
  margin-top: 12px;
  .el-button + .el-button {
    margin-left: 0;
  }
}
.saved {
  margin-top: 16px;
}
.go {
  display: flex;
  width: 100%;
  margin-top: 12px;
}
</style>
