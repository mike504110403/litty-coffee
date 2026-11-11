<script setup lang="ts">
import { MagicStick } from '@element-plus/icons-vue'

// 規則／知識庫頁上方的捷徑：跳到「蒸餾建議」分頁；有待審核的建議時顯示筆數
defineProps<{ pending: number }>()
const emit = defineEmits<{ go: [] }>()
</script>

<template>
  <button type="button" class="distill-hint" :class="{ has: pending > 0 }" @click="emit('go')">
    <el-icon><MagicStick /></el-icon>
    <span>{{ pending > 0 ? `${pending} 筆蒸餾建議待審核` : '從對話蒸餾' }}</span>
  </button>
</template>

<style scoped lang="scss">
.distill-hint {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px dashed var(--lc-border);
  background: transparent;
  color: var(--lc-muted);
  font: inherit;
  font-size: 13px;
  padding: 5px 12px;
  border-radius: 999px;
  cursor: pointer;
  &:hover {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary-light-5);
  }
  &.has {
    border-style: solid;
    border-color: transparent;
    background: var(--lc-tone-caramel-bg);
    color: var(--lc-tone-caramel-fg);
    font-weight: 500;
  }
}
</style>
