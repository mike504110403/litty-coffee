<script setup lang="ts">
import { computed } from 'vue'

// 排行用的水平長條：名稱在左、長條＋數值在右；單一系列不需圖例
const props = withDefaults(
  defineProps<{ items: { label: string; value: number }[]; color?: string; emptyText?: string }>(),
  { color: '#2a78d6', emptyText: '還沒有資料' },
)

const max = computed(() => Math.max(...props.items.map((i) => i.value), 1))
const total = computed(() => props.items.reduce((n, i) => n + i.value, 0))
</script>

<template>
  <ul v-if="items.length" class="bar-list">
    <li
      v-for="it in items"
      :key="it.label"
      :title="`${it.label}：${it.value}（${Math.round((it.value / (total || 1)) * 100)}%）`"
    >
      <span class="label">{{ it.label }}</span>
      <span class="track">
        <span class="bar" :style="{ width: `${(it.value / max) * 100}%`, background: color }" />
        <span class="val">{{ it.value }}</span>
      </span>
    </li>
  </ul>
  <div v-else class="empty">{{ emptyText }}</div>
</template>

<style scoped lang="scss">
.bar-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 10px;
}
li {
  display: grid;
  grid-template-columns: minmax(64px, 30%) 1fr;
  align-items: center;
  gap: 10px;
  font-size: 13px;
}
.label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.track {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.bar {
  height: 14px;
  min-width: 2px;
  border-radius: 0 4px 4px 0; // 貼基線端方、資料端圓
  max-width: calc(100% - 36px);
}
.val {
  font-variant-numeric: tabular-nums;
  color: var(--lc-text);
  font-weight: 500;
}
.empty {
  font-size: 13px;
  color: var(--lc-muted);
  padding: 12px 0;
}
</style>
