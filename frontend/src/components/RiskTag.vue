<script setup lang="ts">
import { computed } from 'vue'
import { riskLabels, riskSourceLabels, type Risk, type RiskSource } from '@/api/inbox'

// 顧客標記小標籤：黑名單（紅）／問題顧客（琥珀）；一般顧客不顯示
const props = defineProps<{ risk: Risk; reason?: string; source?: RiskSource }>()
const title = computed(() => {
  if (!props.risk) return ''
  const parts = [riskLabels[props.risk]]
  if (props.reason) parts.push(`原因：${props.reason}`)
  if (props.source) parts.push(`來源：${riskSourceLabels[props.source]}`)
  return parts.join('｜')
})
</script>

<template>
  <span v-if="risk" class="risk-tag" :class="risk" :title="title">{{ riskLabels[risk] }}</span>
</template>

<style scoped>
.risk-tag {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  padding: 0 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  line-height: 18px;
  white-space: nowrap;
  vertical-align: middle;
}
.risk-tag.blocked {
  background: var(--lc-fail-bg);
  color: var(--lc-fail-fg);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--lc-fail-fg) 35%, transparent);
}
.risk-tag.watch {
  background: var(--lc-warn-bg);
  color: var(--lc-warn-fg);
  box-shadow: inset 0 0 0 1px var(--lc-warn-line);
}
@media (max-width: 768px) {
  .risk-tag {
    font-size: 13px;
    line-height: 20px;
  }
}
</style>
