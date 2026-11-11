<script setup lang="ts">
import { computed, ref, watch } from 'vue'

// 排行用的水平長條：名稱在左、長條＋數值在右；單一系列不需圖例
const props = withDefaults(
  defineProps<{ items: { label: string; value: number }[]; color?: string; emptyText?: string; pageSize?: number }>(),
  { color: 'var(--lc-chart-1)', emptyText: '還沒有資料', pageSize: 7 },
)

// 項目多時在卡片內分頁，卡片高度不變；長條長度仍以全部資料的最大值為準，換頁時比例一致
const page = ref(1)
watch(
  () => props.items.length,
  () => (page.value = 1),
)
const pageItems = computed(() => props.items.slice((page.value - 1) * props.pageSize, page.value * props.pageSize))

const max = computed(() => Math.max(...props.items.map((i) => i.value), 1))
const total = computed(() => props.items.reduce((n, i) => n + i.value, 0))
</script>

<template>
  <div v-if="items.length" class="bar-wrap">
  <ul class="bar-list">
    <li
      v-for="it in pageItems"
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
  <el-pagination
    v-if="items.length > pageSize"
    v-model:current-page="page"
    :page-size="pageSize"
    :total="items.length"
    layout="prev, pager, next"
    size="small"
    :pager-count="5"
    class="pager"
  />
  </div>
  <div v-else class="empty">{{ emptyText }}</div>
</template>

<style scoped lang="scss">
.bar-wrap {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.pager {
  margin-top: auto;
  padding-top: 4px;
  justify-content: center;
}
.bar-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 8px;
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
